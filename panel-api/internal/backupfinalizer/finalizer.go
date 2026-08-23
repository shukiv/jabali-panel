// Package backupfinalizer — M30.2 in-process finalizer (ADR-0080).
// Bridges the gap between "agent finished writing the manifest
// snapshot" and "panel-api marks the job succeeded".
//
// Per-destination model — copy fan-out is GONE. Each backup_jobs row
// already targets one destination; once the manifest snapshot lands
// on that destination's repo, the job is succeeded full-stop.
//
// Finalizer responsibilities:
//  1. List backup_jobs.status='running'.
//  2. For each, ask the agent if the manifest snapshot exists on
//     the destination's repo.
//  3. If yes -> mark succeeded.
//  4. If running >4h -> mark failed (safety timeout).
package backupfinalizer

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/backupwrapperhelpers"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

const (
	TickInterval = 30 * time.Second
	StallTimeout = 4 * time.Hour
	// RestoreStallTimeout seals `running` RESTORE rows whose background
	// goroutine died without MarkFinished (GH #1044 — panel restart
	// mid-restore). The restore goroutine bounds itself at 60 minutes,
	// so anything past 90 is provably orphaned, not slow.
	RestoreStallTimeout = 90 * time.Minute
	MaxJobsPerTick      = 25
	// statusCallTimeout must exceed a cold remote-repo probe: backup.status
	// runs `restic snapshots` against the destination, which on SFTP/S3
	// opens a fresh session and lists the index. At the old 10s the probe
	// timed out and retried on the next tick, so a remote-destination job
	// could never be sealed while doubling the repo churn.
	statusCallTimeout = 30 * time.Second
)

type Deps struct {
	Jobs         repository.BackupJobRepository
	Schedules    repository.BackupScheduleRepository
	Destinations repository.BackupDestinationRepository
	// Settings gates the loop on a DR standby (GH #331): the replicated
	// primary DB carries the primary's running/queued job rows, and an
	// ungated finalizer churned the standby (and the shared DR repo) probing
	// jobs that belong to another box. Optional; nil = never gated.
	Settings repository.ServerSettingsRepository
	Agent    agent.AgentInterface
	// SSOKey unseals a destination's per-row restic password (M30.2.x).
	// Without it backup.status cannot open a rotated destination's repo,
	// so a finished backup is never sealed — it sits "running" until the
	// stall timeout marks it failed.
	SSOKey *ssokey.Key
	Log    *slog.Logger
}

type Finalizer struct{ deps Deps }

// withDestPassword bridges a destination's per-row restic password to the
// agent for the duration of fn. Legacy rows (no destination, or no sealed
// password) invoke fn("") and the agent falls back to the shared file.
// Synchronous: backup.status returns before fn does, so the helper's
// deferred tempfile cleanup is correct here.
func (f *Finalizer) withDestPassword(
	ctx context.Context,
	dest *models.BackupDestination,
	fn func(passwordFile string) error,
) error {
	return backupwrapperhelpers.WithOptionalDestPassword(ctx, dest, f.deps.Agent, f.deps.SSOKey, fn)
}

func New(deps Deps) *Finalizer {
	if deps.Jobs == nil || deps.Destinations == nil ||
		deps.Agent == nil || deps.Log == nil {
		return nil
	}
	return &Finalizer{deps: deps}
}

func (f *Finalizer) Start(ctx context.Context) {
	t := time.NewTicker(TickInterval)
	defer t.Stop()
	f.deps.Log.Info("backup finalizer started", "tick_interval", TickInterval)
	f.tickOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			f.deps.Log.Info("backup finalizer stopped")
			return
		case <-t.C:
			f.tickOnce(ctx)
		}
	}
}

// agentStatus mirrors backupStatusHandler's reply shape (panel-agent
// commands/backup_create.go). Kept here as a private struct because
// the agent package owns the canonical Go types and importing them
// from panel-agent would cross the boundary the wire is supposed to
// hide.
type agentStatus struct {
	JobID         string   `json:"job_id"`
	Stages        []string `json:"stages"`
	ManifestFound bool     `json:"manifest_found"`
	Snapshots     []struct {
		ID   string   `json:"id"`
		Tags []string `json:"tags"`
	} `json:"snapshots"`
	BytesAdded uint64 `json:"bytes_added"`
	BytesTotal uint64 `json:"bytes_total"`
	// FailedStages names manifest stages whose status=failed. A backup
	// whose manifest snapshot exists but whose home/db/mail stage failed
	// is incomplete and must NOT report a plain "succeeded" (GH #454).
	FailedStages []string `json:"failed_stages"`
	Warnings     []string `json:"warnings"`
}

func (f *Finalizer) tickOnce(ctx context.Context) {
	// A DR standby's job rows are the PRIMARY's replica — probing or
	// stall-failing them here is work on another box's jobs (GH #331 two-
	// node drill). Same re-read pattern as drsync/backupscheduler.
	if f.deps.Settings != nil {
		if set, err := f.deps.Settings.Get(ctx); err == nil && set != nil && set.IsStandby() {
			return
		}
	}
	// Query running jobs directly. Paging the newest MaxJobsPerTick rows of
	// ANY status and filtering here used to drop the oldest-created running
	// jobs of a large fan-out — the dispatcher admits work oldest-first, so
	// those are exactly the running ones — and a dropped job could then
	// neither be finalized nor stall-timed-out. It stayed `running` forever,
	// holding a dispatcher slot, until the whole backup queue deadlocked.
	rows, err := f.deps.Jobs.ListRunning(ctx, MaxJobsPerTick)
	if err != nil {
		f.deps.Log.Error("finalizer list-running failed", "err", err)
		return
	}
	if len(rows) == MaxJobsPerTick {
		// Never silently cap coverage: if there are more running jobs than
		// one tick handles, say so — the rest are picked up next tick
		// (oldest-started first, so nothing starves).
		f.deps.Log.Info("finalizer tick full; remaining running jobs deferred to next tick",
			"limit", MaxJobsPerTick)
	}
	now := time.Now().UTC()
	for _, j := range rows {
		if j.Status != models.BackupJobStatusRunning {
			continue
		}
		// Restore jobs (GH #1044) run as background goroutines sealed by
		// their own MarkFinished — the finalizer's manifest tracking
		// (checkOne) does not apply to them, because a restore publishes
		// no manifest snapshot. But "sealed by the goroutine" fails in
		// exactly one way: a panel restart kills the goroutine mid-restore
		// and the row stays `running` forever with nothing left to seal
		// it. Sweep those. The bound is the restore goroutine's own
		// timeout plus slack: any restore row still running past it is
		// provably orphaned, not slow.
		if j.Kind == models.BackupJobKindAccountRestore ||
			j.Kind == models.BackupJobKindSystemRestore {
			if j.StartedAt != nil && now.Sub(*j.StartedAt) > RestoreStallTimeout {
				f.deps.Log.Warn("restore job orphaned past its bound; sealing failed",
					"job_id", j.ID, "kind", j.Kind, "started_at", j.StartedAt)
				_ = f.deps.Jobs.MarkFinished(ctx, j.ID, models.BackupJobStatusFailed,
					"", "", 0, 0, nil, nil,
					"restore interrupted — no completion recorded (panel restarted mid-restore?)")
			}
			continue
		}
		if j.StartedAt != nil && now.Sub(*j.StartedAt) > StallTimeout {
			f.deps.Log.Warn("backup stalled past timeout; marking failed",
				"job_id", j.ID, "started_at", j.StartedAt)
			_ = f.deps.Jobs.MarkFinished(ctx, j.ID, models.BackupJobStatusFailed,
				"", "", 0, 0, nil, nil, "stalled: no manifest snapshot after 4h")
			// JAB-362: a 4h stall is the signal that this destination is dead or
			// unreachable. Trip its breaker so the dispatcher backs it off
			// instead of burning a shared slot on it every tick. Only the stall
			// path counts — dispatch-time failures (user/dest lookup) are not
			// destination health.
			if j.DestinationID != nil && *j.DestinationID != "" {
				f.tripDestinationBreaker(ctx, *j.DestinationID, now)
			}
			continue
		}
		f.checkOne(ctx, j)
	}
}

func (f *Finalizer) checkOne(ctx context.Context, j models.BackupJob) {
	logger := f.deps.Log.With("job_id", j.ID)

	// backup.status now needs the destination repo URL/creds to query
	// against. Legacy rows (NULL destination_id) fall back to the
	// agent's local default.
	statusParams := map[string]any{"job_id": j.ID}
	var dest *models.BackupDestination
	if j.DestinationID != nil && *j.DestinationID != "" {
		d, err := f.deps.Destinations.Get(ctx, *j.DestinationID)
		if err == nil && d != nil {
			dest = d
			statusParams["repo_url"] = dest.URL
			statusParams["sftp"] = backupwrapperhelpers.SFTPWireParams(dest)
			if dest.CredentialsRef != nil {
				statusParams["credentials_ref"] = *dest.CredentialsRef
			}
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, statusCallTimeout)
	defer cancel()
	// A destination with its own sealed password (M30.2.x) can only be
	// probed with THAT password. Without this bridge the poll fails
	// forever against a rotated destination, so a backup that actually
	// succeeded is never sealed and eventually stall-fails.
	var raw json.RawMessage
	err := f.withDestPassword(callCtx, dest, func(passwordFile string) error {
		if passwordFile != "" {
			statusParams["password_file"] = passwordFile
		}
		out, callErr := f.deps.Agent.Call(callCtx, "backup.status", statusParams)
		raw = out
		return callErr
	})
	if err != nil {
		logger.Debug("backup.status query failed; will retry", "err", err)
		return
	}
	var st agentStatus
	if err := json.Unmarshal(raw, &st); err != nil {
		logger.Warn("malformed backup.status reply; skipping", "err", err)
		return
	}
	if !st.ManifestFound {
		return // still running
	}

	manifestSnapID := ""
	for _, s := range st.Snapshots {
		for _, tag := range s.Tags {
			if tag == "stage=manifest" {
				manifestSnapID = s.ID
				break
			}
		}
		if manifestSnapID != "" {
			break
		}
	}
	// A failed stage (e.g. the home dir under a repo lock) leaves the
	// manifest snapshot present but the backup incomplete. Downgrade to
	// "partial" and record why, so a tenant never trusts + restores a
	// full backup that is silently missing its files (GH #454).
	status := models.BackupJobStatusSucceeded
	var warningsJSON json.RawMessage
	errText := ""
	if len(st.FailedStages) > 0 {
		status = models.BackupJobStatusPartial
		errText = "incomplete backup — stage(s) failed: " + strings.Join(st.FailedStages, ", ")
		if b, mErr := json.Marshal(st.Warnings); mErr == nil {
			warningsJSON = b
		}
	}
	if err := f.deps.Jobs.MarkFinished(ctx, j.ID,
		status,
		manifestSnapID, "", st.BytesAdded, st.BytesTotal, nil, warningsJSON, errText); err != nil {
		logger.Error("mark finished failed", "err", err, "status", status)
		return
	}
	logger.Info("backup finalized", "snapshot_id", manifestSnapID,
		"status", status, "failed_stages", st.FailedStages)

	// JAB-362: a landed snapshot proves the destination is reachable — clear any
	// tripped breaker so the dispatcher stops backing it off. Both Succeeded and
	// Partial reached the repo (Partial = some stage failed, not the transport).
	if dest != nil && dest.ConsecutiveFailures > 0 {
		if err := f.deps.Destinations.ResetHealth(ctx, dest.ID); err != nil {
			logger.Warn("failed to reset backup destination breaker", "dest", dest.ID, "err", err)
		} else {
			logger.Warn("backup destination breaker RESET after success", "dest", dest.ID)
		}
	}
}

// backupBreakerBaseBackoff / backupBreakerMaxBackoff bound the exponential
// dispatch backoff a repeatedly stall-failing destination earns (JAB-362).
const (
	backupBreakerBaseBackoff = time.Hour
	backupBreakerMaxBackoff  = 24 * time.Hour
)

// breakerBackoffFor returns the dispatch backoff for the n-th consecutive
// failure: base * 2^(n-1), capped. n>=1.
func breakerBackoffFor(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	d := backupBreakerBaseBackoff
	for i := 1; i < n && d < backupBreakerMaxBackoff; i++ {
		d *= 2
	}
	if d > backupBreakerMaxBackoff {
		d = backupBreakerMaxBackoff
	}
	return d
}

// tripDestinationBreaker increments a destination's consecutive-failure count
// and pushes its backoff-until out exponentially. The next dispatch tick skips
// the destination until the window expires, at which point one job is admitted
// as a probe (a success resets the breaker via checkOne).
func (f *Finalizer) tripDestinationBreaker(ctx context.Context, destID string, now time.Time) {
	d, err := f.deps.Destinations.Get(ctx, destID)
	if err != nil || d == nil {
		f.deps.Log.Warn("breaker trip: destination lookup failed", "dest", destID, "err", err)
		return
	}
	failures := d.ConsecutiveFailures + 1
	until := now.Add(breakerBackoffFor(failures))
	if err := f.deps.Destinations.RecordFailure(ctx, destID, failures, until); err != nil {
		f.deps.Log.Warn("breaker trip: record-failure failed", "dest", destID, "err", err)
		return
	}
	f.deps.Log.Warn("backup destination breaker TRIPPED",
		"dest", destID, "consecutive_failures", failures, "backoff_until", until)
}
