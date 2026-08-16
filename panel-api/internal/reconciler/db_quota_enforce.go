// JAB-243 — DB storage quota enforcement (the enforceable subset).
//
// Tenant database files live under /var/lib/mysql, outside every POSIX
// quota, so package disk limits never bounded them. A true hard cap
// needs per-tenant tablespaces / project-quota filesystems (tracked on
// the ticket); what IS enforceable today is the classic hosting
// pattern: when a tenant's summed DB footprint reaches the package disk
// quota, revoke the WRITE privileges on their database users — SELECT,
// DELETE and DROP stay, so the tenant can read and free space — and
// restore the owner-intended grants once usage drops below 90%
// (hysteresis: a tenant sitting exactly at quota must not flap grants
// every sweep).
//
// Desired-state reconciliation, grants are the state (per-tick
// idempotency rule): every sweep computes over/under from live sizes
// and applies the matching grant set. The restore path re-applies the
// grant ROW's stored level — a deliberately read-only db user is never
// promoted to rw by the quota loop (intersection of owner intent and
// quota permission).
//
// MariaDB only: PostgreSQL's grant model has no privilege-list revoke
// that keeps SELECT (db.postgres.revoke is all-or-nothing) — a pg
// write-freeze is part of the hard-cap follow-up on the ticket. pg
// database sizes are likewise excluded from the enforcement sum so an
// over-quota pg footprint cannot freeze a tenant's mariadb writes the
// tenant can't fix by deleting mariadb rows.
//
// The in-memory edge map only dedupes notifications; a panel restart
// re-detects the edge and re-applies idempotent grants (harmless) with
// at most one repeat notification per over-quota tenant.
package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

const (
	dbQuotaEnforceInterval = time.Hour
	dbQuotaRestorePercent  = 90.0
)

// dbQuotaWriteRevoke is the privilege set removed at quota. SELECT,
// DELETE and DROP deliberately survive — the tenant must be able to
// read their data and free space to get back under.
var dbQuotaWriteRevoke = []string{"INSERT", "UPDATE", "CREATE", "ALTER", "INDEX"}

// WithDBQuotaEnforce wires the JAB-243 DB-quota sweep. Databases repo
// required; nil disables the loop. The freeze/restore mechanism lives
// agent-side (db.writes.set, snapshot-and-replay) so the reconciler
// needs only the tenant's database list — not grant/db-user rows, which
// CLI- and migration-created grants never populate.
func (r *Reconciler) WithDBQuotaEnforce(dbs repository.DatabaseRepository) *Reconciler {
	r.databases = dbs
	return r
}

// reconcileDBQuotaEnforce is the hourly sweep. Cheap noop when disabled.
func (r *Reconciler) reconcileDBQuotaEnforce(ctx context.Context) {
	if r.databases == nil || r.users == nil || r.packages == nil || r.agent == nil {
		return
	}
	r.dbQuotaEnforceMu.Lock()
	if !r.dbQuotaEnforceLastRun.IsZero() && time.Since(r.dbQuotaEnforceLastRun) < dbQuotaEnforceInterval {
		r.dbQuotaEnforceMu.Unlock()
		return
	}
	r.dbQuotaEnforceLastRun = time.Now()
	r.dbQuotaEnforceMu.Unlock()

	// One agent call for every schema size on the host.
	raw, err := r.agent.Call(ctx, "db.usage.by_schema", map[string]any{})
	if err != nil {
		r.log.Warn("db_quota_enforce: usage query failed", "err", err)
		return
	}
	var usage struct {
		Schemas []struct {
			Schema string `json:"schema"`
			Bytes  int64  `json:"bytes"`
		} `json:"schemas"`
	}
	if err := json.Unmarshal(raw, &usage); err != nil {
		r.log.Warn("db_quota_enforce: usage decode failed", "err", err)
		return
	}
	bySchema := make(map[string]int64, len(usage.Schemas))
	for _, s := range usage.Schemas {
		bySchema[s.Schema] = s.Bytes
	}

	users, _, err := r.users.List(ctx, repository.ListOptions{Limit: 10000})
	if err != nil {
		r.log.Warn("db_quota_enforce: list users failed", "err", err)
		return
	}
	pkgByID := make(map[string]*models.HostingPackage)

	for i := range users {
		u := &users[i]
		if u.IsAdmin || u.PackageID == nil || *u.PackageID == "" {
			continue
		}
		pkg, ok := pkgByID[*u.PackageID]
		if !ok {
			p, perr := r.packages.FindByID(ctx, *u.PackageID)
			if perr != nil || p == nil {
				pkgByID[*u.PackageID] = nil
				continue
			}
			pkgByID[*u.PackageID] = p
			pkg = p
		}
		if pkg == nil || pkg.DiskQuotaMB <= 0 {
			continue
		}
		dbs, _, derr := r.databases.ListByUserID(ctx, u.ID, repository.ListOptions{Limit: 1000})
		if derr != nil || len(dbs) == 0 {
			continue
		}
		var total int64
		var mariaDBs []models.Database
		for _, d := range dbs {
			if d.Engine != "" && d.Engine != "mariadb" {
				continue
			}
			mariaDBs = append(mariaDBs, d)
			total += bySchema[d.Name]
		}
		if len(mariaDBs) == 0 {
			continue
		}
		quotaBytes := int64(pkg.DiskQuotaMB) * 1024 * 1024
		pct := float64(total) / float64(quotaBytes) * 100.0

		switch {
		case pct >= 100.0:
			r.applyDBQuotaState(ctx, u.ID, mariaDBs, false, total, quotaBytes)
		case pct < dbQuotaRestorePercent:
			r.applyDBQuotaState(ctx, u.ID, mariaDBs, true, total, quotaBytes)
			// 90–100%: hold whatever state the tenant is in.
		}
	}
}

// applyDBQuotaState freezes or restores writes on every mariadb database
// the tenant owns via the agent's db.writes.set (snapshot-and-replay).
// writable=false freezes (revoke the write set, snapshot prior grants);
// writable=true restores (replay the snapshot verbatim — a read-only
// grantee stays read-only). Idempotent: the agent no-ops a second freeze
// and a restore with no snapshot.
func (r *Reconciler) applyDBQuotaState(ctx context.Context, userID string, dbs []models.Database, writable bool, total, quotaBytes int64) {
	// The agent's per-database freeze snapshot is the persistent source of
	// truth, so this drives entirely off the agent's `changed` reply — no
	// in-memory edge state to lose across a panel restart. Freeze and
	// restore are both idempotent agent-side (a second freeze re-asserts
	// without re-snapshotting; a restore with no snapshot is a no-op), so
	// calling every sweep is safe; `changed=true` marks the real
	// transition and is the only thing that notifies.
	anyChanged := false
	for _, d := range dbs {
		raw, aerr := r.agent.Call(ctx, "db.writes.set", map[string]any{
			"db_name": d.Name,
			"freeze":  !writable,
		})
		if aerr != nil {
			r.log.Warn("db_quota_enforce: writes.set failed", "db", d.Name, "freeze", !writable, "err", aerr)
			continue
		}
		var resp struct {
			Changed bool `json:"changed"`
		}
		if json.Unmarshal(raw, &resp) == nil && resp.Changed {
			anyChanged = true
		}
	}
	if !anyChanged {
		return // already in the desired state — no transition, no notification
	}
	if writable {
		r.log.Info("db_quota_enforce: writes restored", "user_id", userID, "bytes", total)
		r.publishDBQuotaTransition(ctx, userID, total, quotaBytes,
			"db.quota.restored", "info", "Database writes restored",
			"Database usage dropped below 90% of the package disk quota; write privileges were restored.")
	} else {
		r.log.Warn("db_quota_enforce: writes frozen at quota", "user_id", userID, "bytes", total, "quota", quotaBytes)
		r.publishDBQuotaTransition(ctx, userID, total, quotaBytes,
			"db.quota.frozen", "critical", "Database writes frozen at disk quota",
			"Database usage reached the package disk quota. INSERT/UPDATE/CREATE/ALTER/INDEX were revoked; SELECT and DELETE still work — free space (or upgrade the package) to restore writes.")
	}
}

func (r *Reconciler) publishDBQuotaTransition(ctx context.Context, userID string, total, quotaBytes int64, kind, sev, title, body string) {
	if r.notificationQueue == nil {
		return
	}
	_, _ = r.notificationQueue.Publish(ctx, notifications.Envelope{
		EventKind: kind,
		Severity:  sev,
		UserID:    userID,
		Title:     title,
		Body:      fmt.Sprintf("%s (usage %d MiB / quota %d MiB)", body, total>>20, quotaBytes>>20),
		Deeplink:  "/jabali-admin/users/" + userID,
	})
}
