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
)

const (
	TickInterval   = 30 * time.Second
	StallTimeout   = 4 * time.Hour
	MaxJobsPerTick = 25
)

type Deps struct {
	Jobs         repository.BackupJobRepository
	Schedules    repository.BackupScheduleRepository
	Destinations repository.BackupDestinationRepository
	Agent        agent.AgentInterface
	Log          *slog.Logger
}

type Finalizer struct{ deps Deps }

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
	rows, _, err := f.deps.Jobs.ListAll(ctx, MaxJobsPerTick, 0)
	if err != nil {
		f.deps.Log.Error("finalizer list-running failed", "err", err)
		return
	}
	now := time.Now().UTC()
	for _, j := range rows {
		if j.Status != models.BackupJobStatusRunning {
			continue
		}
		// Restore jobs are sealed synchronously by the API restore
		// handler when Agent.Call returns. The finalizer only tracks
		// fan-out backup jobs that publish a manifest snapshot.
		if j.Kind == models.BackupJobKindAccountRestore ||
			j.Kind == models.BackupJobKindSystemRestore {
			continue
		}
		if j.StartedAt != nil && now.Sub(*j.StartedAt) > StallTimeout {
			f.deps.Log.Warn("backup stalled past timeout; marking failed",
				"job_id", j.ID, "started_at", j.StartedAt)
			_ = f.deps.Jobs.MarkFinished(ctx, j.ID, models.BackupJobStatusFailed,
				"", "", 0, 0, nil, nil, "stalled: no manifest snapshot after 4h")
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
	if j.DestinationID != nil && *j.DestinationID != "" {
		dest, err := f.deps.Destinations.Get(ctx, *j.DestinationID)
		if err == nil && dest != nil {
			statusParams["repo_url"] = dest.URL
			statusParams["sftp"] = backupwrapperhelpers.SFTPWireParams(dest)
			if dest.CredentialsRef != nil {
				statusParams["credentials_ref"] = *dest.CredentialsRef
			}
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := f.deps.Agent.Call(callCtx, "backup.status", statusParams)
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
}
