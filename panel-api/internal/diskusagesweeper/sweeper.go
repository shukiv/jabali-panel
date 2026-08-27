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
// It deliberately reuses the same `user.limits.report` agent call that the
// per-user endpoint makes, rather than measuring independently, so the
// sorted column and the detail view can never disagree about a user.
//
// Cadence is minutes, not seconds: the figure comes from `quota` (or a
// `du -sk` fallback), which is already minutes-stale at the kernel level.
// The sweep is spread one user at a time — a burst of concurrent `du` over
// every account on the box is a good way to make a busy disk worse.
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

// InterUserDelay paces the sweep. Measuring every account back-to-back can
// pin a disk that tenants are actively serving from; a short gap turns the
// sweep into background noise.
const InterUserDelay = 250 * time.Millisecond

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
	var swept, skipped int
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
		used, limit, ok := s.reportOne(ctx, *u.Username)
		if !ok {
			skipped++
			continue
		}
		if err := s.deps.Users.UpdateDiskUsage(ctx, u.ID, used, limit, time.Now().UTC()); err != nil {
			s.deps.Log.Warn("disk usage sweep: persist failed",
				"user_id", u.ID, "username", *u.Username, "err", err)
			skipped++
			continue
		}
		swept++
		select {
		case <-ctx.Done():
			return
		case <-time.After(InterUserDelay):
		}
	}
	s.deps.Log.Info("disk usage sweep done", "swept", swept, "skipped", skipped)
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
