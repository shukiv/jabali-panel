package reconciler

import (
	"context"
	"sort"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/userops"
)

// JAB-236 — the reconciler is the retry engine behind durable domain
// deletion. Delete paths write a domain_teardowns tombstone BEFORE the row
// goes and attempt the teardown immediately; this sweep re-drives every
// pending tombstone (panel restarted mid-delete, agent was down) through
// the SAME executor until it succeeds. Runs BEFORE the orphan sweep so a
// just-torn-down site never gets warned about.

// domainTeardownRetryAfter throttles per-tombstone retries — a down agent
// fails everything anyway, and once it's back one retry finishes the job.
const domainTeardownRetryAfter = 5 * time.Minute

// WithDomainTeardowns wires the tombstone repo. Nil-safe: without it the
// sweep is a no-op and delete durability degrades to the immediate attempt.
func (r *Reconciler) WithDomainTeardowns(repo repository.DomainTeardownRepository) *Reconciler {
	r.domainTeardowns = repo
	return r
}

// processDomainTeardowns drives pending tombstones and returns the set of
// names still pending (the orphan sweep skips those — they are being
// handled, not orphaned).
func (r *Reconciler) processDomainTeardowns(ctx context.Context) map[string]bool {
	pending := map[string]bool{}
	if r.domainTeardowns == nil {
		return pending
	}
	rows, err := r.domainTeardowns.List(ctx)
	if err != nil {
		r.log.Error("domain teardowns: list failed", "err", err)
		return pending
	}
	now := time.Now().UTC()
	for i := range rows {
		row := &rows[i]
		name := row.DomainName

		// Live-row guard. A tombstone with a live domain row is stale —
		// either the row delete was refused after the tombstone was
		// written (crash in the gap), or the operator re-created the name
		// before a failed teardown retried. Acting on it would tear down
		// a LIVE site (worst case: the panel's own primary). Drop it.
		if live, ferr := r.domains.FindByName(ctx, name); ferr == nil && live != nil {
			r.log.Warn("domain teardowns: tombstone for a live domain — dropping without teardown",
				"domain", name)
			_ = r.domainTeardowns.Delete(ctx, name)
			continue
		}

		if row.LastAttemptAt != nil && now.Sub(*row.LastAttemptAt) < domainTeardownRetryAfter {
			pending[name] = true
			continue
		}
		if err := userops.ExecuteDomainTeardown(ctx, r.agent, name); err != nil {
			r.log.Warn("domain teardowns: retry failed — will retry again",
				"domain", name, "attempts", row.Attempts+1, "err", err)
			_ = r.domainTeardowns.MarkAttempt(ctx, name, err.Error())
			pending[name] = true
			continue
		}
		_ = r.domainTeardowns.Delete(ctx, name)
		r.log.Info("domain teardowns: host-side teardown completed", "domain", name, "attempts", row.Attempts+1)
	}
	return pending
}

// reportOrphanSites replaces the per-site-per-tick warning storm (the
// ticket's "800 warnings in 40 minutes") with ONE aggregated warning,
// emitted only when the orphan set CHANGES. The sweep's mandate stays
// deliberately conservative: it never auto-deletes an agent site it has no
// row for — an agent-known site with no DB row cannot be distinguished
// from out-of-band operator work with certainty, and deleting a serving
// site on a guess is worse than leaving it. Tombstoned deletes are the
// self-healing path; everything else is surfaced once, actionably.
func (r *Reconciler) reportOrphanSites(orphans []string) {
	sort.Strings(orphans)
	key := strings.Join(orphans, ",")
	if key == r.lastOrphanKey {
		return // unchanged set — stay quiet
	}
	r.lastOrphanKey = key
	if len(orphans) == 0 {
		r.log.Info("reconcile: no orphan sites remain in agent")
		return
	}
	r.log.Warn("reconcile: orphan sites found in agent with no DB rows — not auto-deleted; "+
		"if unwanted, re-create the domain in the panel and delete it (runs the durable teardown), "+
		"or insert the name into domain_teardowns for the reconciler to tear down",
		"count", len(orphans), "sites", strings.Join(orphans, ", "))
}
