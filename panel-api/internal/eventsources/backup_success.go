package eventsources

// backup_success event source (GH #979 follow-up — per-job "backup finished"
// notification the fail source never had a counterpart for).
//
// Polls backup_jobs for jobs that FINISHED cleanly within the lookback window —
// status='succeeded' (fully) or status='partial' (finished with some items
// skipped/failed) — and fires one `backup.success` envelope per freshly-finished
// job: severity "info" for a full success, "warning" for a partial. Partial is
// deliberately included: a partial backup IS finished, and firing nothing for it
// would read to a user who enabled this event as "the backup didn't run" — worse
// than a plain success being noisy. Deduped via the History layer on the job_id
// tag; the dedupe key is scoped to the event kind, so a job that failed then
// succeeded on retry is NOT suppressed by its earlier backup.fail row.
//
// Why a poller and not an emit at the finish site: backups are marked finished by
// the finalizer, the scheduler, AND the CLI backup/restore path — multiple
// writers, one poller catches them all without coupling the repository layer to
// notifications. Mirrors backup_fail.go.
//
// Off when BackupJobs repo is nil — tests + minimal panel installs (no backup
// stack wired) skip the source.

import (
	"context"
	"fmt"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
)

const (
	backupSuccessTick     = 5 * time.Minute
	backupSuccessLookback = 30 * time.Minute
	backupSuccessCoolOff  = 24 * time.Hour
	// Successful backups are the common case (unlike failures), so a busy fleet
	// box can produce a large burst of finished jobs in one window.
	// ListByStatusSince orders finished_at ASC, so an over-cap pass drains the
	// oldest first and the next tick catches the rest while they are still inside
	// the lookback — a higher cap than backup_fail's keeps that self-drain from
	// ever lagging on a big multi-tenant box.
	backupSuccessLimit = 500
)

// backupFinishedStatus pairs a terminal-OK backup status with how its
// notification should read.
type backupFinishedStatus struct {
	status   string
	severity string
	verb     string
}

var backupFinishedStatuses = []backupFinishedStatus{
	{models.BackupJobStatusSucceeded, "info", "succeeded"},
	{models.BackupJobStatusPartial, "warning", "completed with warnings"},
}

func runBackupSuccess(ctx context.Context, d Deps) {
	if d.BackupJobs == nil {
		d.Log.Debug("eventsources: backup_success disabled (no BackupJobs repo)")
		return
	}
	// Immediate pass so a panel restart doesn't sleep through a fresh success
	// that landed during the down window.
	backupSuccessPass(ctx, d)
	tick := time.NewTicker(backupSuccessTick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		backupSuccessPass(ctx, d)
	}
}

func backupSuccessPass(ctx context.Context, d Deps) {
	since := d.Now().Add(-backupSuccessLookback)
	for _, fs := range backupFinishedStatuses {
		rows, err := d.BackupJobs.ListByStatusSince(ctx, fs.status, since, backupSuccessLimit)
		if err != nil {
			d.Log.Warn("eventsources: backup_success list failed", "status", fs.status, "err", err)
			continue
		}
		for _, j := range rows {
			dedupeTag := "backup_job_id=" + j.ID
			if !shouldFire(ctx, d, "backup.success", dedupeTag, backupSuccessCoolOff) {
				continue
			}
			env := notifications.Envelope{
				EventKind: "backup.success",
				Severity:  fs.severity,
				Title:     fmt.Sprintf("Backup %s: %s", fs.verb, j.Kind),
				Body:      fmt.Sprintf("Backup job %s (%s) %s. dedupe=%s", j.ID, j.Kind, fs.verb, dedupeTag),
				Deeplink:  "/jabali-admin/backups",
				UserID:    j.UserID,
			}
			if _, perr := d.Queue.Publish(ctx, env); perr != nil {
				d.Log.Warn("eventsources: backup_success publish failed", "job_id", j.ID, "err", perr)
			}
		}
	}
}
