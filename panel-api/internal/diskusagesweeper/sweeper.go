// Package diskusagesweeper keeps users.disk_used_kb fresh so the admin
// Users list can sort by disk usage.
//
// Why a sweeper and not a live read: sorting in this panel is server-side
// against a per-repo column whitelist (user_repository.go userListCols), so
// a sortable column has to BE a column. Disk usage previously existed only
// behind GET /users/:id/usage, which each table row called for itself after
// render — an 80-row page issued 80 agent round-trips and the table never
// held the numbers it would have needed to order by.
//
// Since JAB-376 a sweep is ONE host quota inventory (`system.quota_inventory`,
// a single `repquota` for the mount) plus one batched persist, instead of a
// `user.limits.report` per account. The per-user `du` fallback survives for
// accounts the inventory reports as 0 (empty homes / files created before
// quotaon — GH #1242), so the sorted column and the detail view still agree;
// that fallback is paced and bounded to the empty minority so it can't
// reintroduce the du storm the per-user loop risked. When the inventory is
// unavailable (quota disabled, agent down) every account falls back to the
// per-user path — correctness over speed during an outage.
//
// Cadence is minutes, not seconds: the figure comes from `quota` accounting,
// already minutes-stale at the kernel level.
package diskusagesweeper

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Interval is how often a sweep starts. Disk usage moves slowly and the
// underlying quota report is itself minutes-stale, so a tighter loop buys
// no accuracy and costs a fork per account.
const Interval = 15 * time.Minute

// PerUserTimeout bounds one agent call. The report shells out to `quota`
// and may fall back to `du -sk` on a large home, so it is generous — but
// finite, because one slow account must not stall the whole sweep.
const PerUserTimeout = 30 * time.Second

// InterUserDelay paces the per-user du FALLBACK path. Since JAB-376 the quota
// inventory is a single call, so this only gates the du walks for empty /
// quota-0 homes — measuring those back-to-back can still pin a busy disk.
const InterUserDelay = 250 * time.Millisecond

// InventoryTimeout bounds the one host quota inventory call. repquota reads the
// kernel quota state (fast) but the agent round-trip + a very large user table
// warrant headroom.
const InventoryTimeout = 60 * time.Second

// maxUsersPerSweep caps one pass. A host with more accounts than this
// simply takes several passes to come round — far better than one sweep
// that runs for hours holding a DB cursor's worth of rows in memory.
const maxUsersPerSweep = 5000

// UserStore is the slice of the user repository this sweeper needs.
//
// Declared here rather than added to repository.UserRepository on purpose:
// that interface is implemented by a hand-written fake in nine test
// packages, so widening it would break them all and turn a self-contained
// feature into a repo-wide diff. The concrete *userRepo satisfies this
// automatically; serve.go type-asserts.
type UserStore interface {
	List(ctx context.Context, opts repository.ListOptions) ([]models.User, int64, error)
	UpdateDiskUsage(ctx context.Context, id string, usedKB, limitKB uint64, checkedAt time.Time) error
	// BatchUpdateDiskUsage persists a whole sweep in bounded chunks instead of
	// one statement per account (JAB-376). It must NOT touch users.updated_at.
	BatchUpdateDiskUsage(ctx context.Context, rows []repository.DiskUsageRow, checkedAt time.Time) error
}

// Deps is the dependency bundle. Users + Agent are required; a nil Agent
// means there is nothing to measure with, and New returns nil so the
// caller can skip starting the loop.
type Deps struct {
	Users      UserStore
	Agent      agent.AgentInterface
	QuotaMount string
	Log        *slog.Logger
}

// Sweeper is the goroutine wrapper. Construct with New, run with Start.
type Sweeper struct{ deps Deps }

// New returns a configured sweeper, or nil when a required dependency is
// missing. Callers log and skip — an incomplete deployment must not crash
// the panel on boot over a cosmetic column.
func New(deps Deps) *Sweeper {
	if deps.Users == nil || deps.Agent == nil || deps.Log == nil {
		return nil
	}
	return &Sweeper{deps: deps}
}

// Start runs sweeps until ctx is done. The first sweep runs immediately so
// a fresh install populates the column without a 15-minute blank.
func (s *Sweeper) Start(ctx context.Context) {
	t := time.NewTicker(Interval)
	defer t.Stop()
	s.deps.Log.Info("disk usage sweeper started", "interval", Interval)
	s.SweepOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			s.deps.Log.Info("disk usage sweeper stopped")
			return
		case <-t.C:
			s.SweepOnce(ctx)
		}
	}
}

// SweepOnce measures every account with a linux user and persists the
// result. Exported so a CLI can force a refresh without waiting for the
// tick.
//
// Per-user failures are logged and skipped, never fatal: one account whose
// home is being rsynced (or whose OS user was removed out of band) must not
// cost the other 79 their refresh. A skipped user keeps its previous
// snapshot and its older disk_checked_at, which is what makes staleness
// visible rather than silent.
func (s *Sweeper) SweepOnce(ctx context.Context) {
	users, _, err := s.deps.Users.List(ctx, repository.ListOptions{Limit: maxUsersPerSweep})
	if err != nil {
		s.deps.Log.Error("disk usage sweep: list users failed", "err", err)
		return
	}
	// One privileged host quota inventory read for the whole mount (JAB-376),
	// replacing the per-user quota fan-out. On failure (quota disabled, agent
	// down) inv is empty and every account falls to the per-user du path below —
	// the pre-JAB-376 behaviour: slower, but correct.
	inv, invPartial := s.observeQuota(ctx)

	batch := make([]repository.DiskUsageRow, 0, len(users))
	var fromInventory, fromDu, skipped int
	for i := range users {
		if ctx.Err() != nil {
			return
		}
		u := &users[i]
		// No linux account means no home and no quota entry — admins and
		// not-yet-provisioned rows land here.
		if u.Username == nil || *u.Username == "" {
			continue
		}
		if e, ok := inv[*u.Username]; ok && (e.UsedKB > 0 || e.LimitKB > 0) {
			// Quota gave an authoritative answer: either real usage, or a real
			// hard limit with 0 used (a packaged-but-empty account — quota
			// accounting is on for them, so 0 is true, not "unmeasured"). The
			// fast path for the vast majority; no per-user agent call.
			batch = append(batch, repository.DiskUsageRow{UserID: u.ID, UsedKB: e.UsedKB, LimitKB: e.LimitKB})
			fromInventory++
			continue
		}
		// Truly quota-less (no usage AND no limit) or absent from the inventory:
		// fall back to the per-user du walk (GH #1242 — a quota-less home can
		// still hold files). Bounded to that minority and paced so a box full of
		// empty homes can't du-storm.
		used, limit, ok := s.reportOne(ctx, *u.Username)
		if !ok {
			skipped++
			continue // keep last-good
		}
		batch = append(batch, repository.DiskUsageRow{UserID: u.ID, UsedKB: used, LimitKB: limit})
		fromDu++
		select {
		case <-ctx.Done():
			return
		case <-time.After(InterUserDelay):
		}
	}

	// One observation time, captured AFTER measurement, so disk_checked_at
	// reflects when the snapshot was actually taken (the du-fallback loop can
	// take minutes on a box of empty homes).
	checkedAt := time.Now().UTC()
	if len(batch) > 0 {
		if err := s.deps.Users.BatchUpdateDiskUsage(ctx, batch, checkedAt); err != nil {
			s.deps.Log.Error("disk usage sweep: batch persist failed", "rows", len(batch), "err", err)
			return
		}
	}
	s.deps.Log.Info("disk usage sweep done",
		"from_inventory", fromInventory, "from_du", fromDu, "skipped", skipped,
		"inventory_partial", invPartial)
}

// quotaEntry mirrors the agent's system.quota_inventory entry.
type quotaEntry struct {
	Username string `json:"username"`
	UsedKB   uint64 `json:"used_kb"`
	LimitKB  uint64 `json:"limit_kb"`
}

// observeQuota calls the agent's one-shot host quota inventory and returns a
// username→entry map + whether the inventory was partial. A failure (quota
// disabled on the mount, empty mount, agent down, or a parse error) returns an
// empty map so every account falls back to the per-user du path — correctness
// over speed during an outage, never a fleet-wide "0 used" that clears real
// quota alerts.
func (s *Sweeper) observeQuota(ctx context.Context) (map[string]quotaEntry, bool) {
	if s.deps.QuotaMount == "" {
		return map[string]quotaEntry{}, false // nothing to inventory
	}
	callCtx, cancel := context.WithTimeout(ctx, InventoryTimeout)
	defer cancel()
	raw, err := s.deps.Agent.Call(callCtx, "system.quota_inventory", map[string]any{
		"mount": s.deps.QuotaMount,
	})
	if err != nil {
		s.deps.Log.Warn("disk usage sweep: quota inventory failed, falling back to per-user du",
			"mount", s.deps.QuotaMount, "err", err)
		return map[string]quotaEntry{}, false
	}
	var resp struct {
		Entries []quotaEntry `json:"entries"`
		Partial bool         `json:"partial"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		s.deps.Log.Warn("disk usage sweep: quota inventory parse failed", "err", err)
		return map[string]quotaEntry{}, false
	}
	m := make(map[string]quotaEntry, len(resp.Entries))
	for _, e := range resp.Entries {
		m[e.Username] = e // repquota emits one row per user; last wins defensively
	}
	return m, resp.Partial
}

// reportOne asks the agent for one account's disk figures. Returns ok=false
// on any failure so the caller leaves the previous snapshot in place —
// writing a zero would show every account as empty the moment the agent
// hiccups, which is worse than a stale number.
func (s *Sweeper) reportOne(ctx context.Context, username string) (usedKB, limitKB uint64, ok bool) {
	callCtx, cancel := context.WithTimeout(ctx, PerUserTimeout)
	defer cancel()
	raw, err := s.deps.Agent.Call(callCtx, "user.limits.report", map[string]any{
		"username":    username,
		"quota_mount": s.deps.QuotaMount,
		// GH #1242: du-fallback for users whose quota has no answer — a user
		// with no hosting package has no quota, so quota reports nothing and the
		// list showed a blank cell. The agent only walks du when quota is absent
		// or 0, so packaged users with real usage still take the fast quota path;
		// the extra du lands on quota-less / empty homes only, and this sweep is
		// serialized + paced (InterUserDelay) off the request path, so it doesn't
		// reintroduce the read-path du storm that made this on-demand-only.
		"measure_disk": true,
	})
	if err != nil {
		s.deps.Log.Debug("disk usage sweep: agent report failed",
			"username", username, "err", err)
		return 0, 0, false
	}
	var rep struct {
		Disk *struct {
			UsedKB  uint64 `json:"used_kb"`
			LimitKB uint64 `json:"limit_kb"`
		} `json:"disk"`
	}
	if err := json.Unmarshal(raw, &rep); err != nil || rep.Disk == nil {
		s.deps.Log.Debug("disk usage sweep: report had no disk section", "username", username)
		return 0, 0, false
	}
	return rep.Disk.UsedKB, rep.Disk.LimitKB, true
}
