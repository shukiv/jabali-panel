package backupscheduler

// GH #1240 — opt-in automatic daily local backups for all users.
//
// When server_settings.default_local_backups_enabled is ON, the panel keeps a
// single MANAGED schedule (backup_schedules.is_managed_default = 1) that backs up
// every tenant to the local repo daily. It's a normal account_backup schedule
// with an EMPTY user list — which the scheduler already fans out to "all non-admin
// users" at tick time, so new tenants are covered automatically. When the flag is
// OFF the managed schedule is disabled; admin/tenant-created schedules are never
// touched. The provisioner converges this from the flag on boot and on every
// enqueue tick (idempotent + cheap), so flipping the setting takes effect within
// one tick without any extra wiring.

import (
	"context"
	"errors"
	"time"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

const (
	// defaultLocalBackupCron is the daily local-backup time (05:00), matching the
	// reporter's request in GH #1240. The admin can adjust it after creation — the
	// provisioner only creates + enables/disables, it doesn't re-clobber the time.
	defaultLocalBackupCron = "0 5 * * *"
	defaultLocalBackupKeep = 3 // 3-day versioning (GH #1240); older pruned.
	defaultLocalDestName   = "Local"
)

// ensureDefaultLocalBackup converges the managed default-local-backup schedule
// from the flag. Best-effort: logs and returns on any error (a transient DB blip
// just retries next tick).
func (s *Scheduler) ensureDefaultLocalBackup(ctx context.Context) {
	if s.deps.Settings == nil {
		return
	}
	st, err := s.deps.Settings.Get(ctx)
	if err != nil || st == nil {
		return
	}

	all, err := s.deps.Schedules.List(ctx)
	if err != nil {
		s.deps.Log.WarnContext(ctx, "default-local-backup: list schedules", "error", err)
		return
	}
	var managed *models.BackupSchedule
	for i := range all {
		if all[i].IsManagedDefault {
			managed = &all[i]
			break
		}
	}

	if !st.DefaultLocalBackupsEnabled {
		// OFF: disable the managed schedule if present + enabled. Keep the row so
		// re-enabling is a clean flip, and NEVER touch other schedules.
		if managed != nil && managed.Enabled {
			managed.Enabled = false
			if err := s.deps.Schedules.Update(ctx, managed); err != nil {
				s.deps.Log.WarnContext(ctx, "default-local-backup: disable", "error", err)
			} else {
				s.deps.Log.InfoContext(ctx, "default-local-backup: disabled (setting off)")
			}
		}
		return
	}

	// ON: ensure a local destination exists, then the managed schedule.
	destID, err := s.ensureLocalDestination(ctx)
	if err != nil {
		s.deps.Log.WarnContext(ctx, "default-local-backup: ensure local destination", "error", err)
		return
	}

	if managed == nil {
		keep := defaultLocalBackupKeep
		// A NULL next_run_at is NEVER due (ListDue filters it out), so the schedule
		// would never fire — set the first run from the cron now.
		next, nerr := internalbackup.NextFire(defaultLocalBackupCron, time.Now().UTC())
		if nerr != nil {
			s.deps.Log.WarnContext(ctx, "default-local-backup: parse cron", "cron", defaultLocalBackupCron, "error", nerr)
			return
		}
		m := &models.BackupSchedule{
			ID:               ids.NewULID(),
			Kind:             models.BackupScheduleKindAccount,
			UserID:           nil, // admin-owned; fan-out driven by the (empty) user list
			CronExpr:         defaultLocalBackupCron,
			Content:          "full",
			Cadence:          "", // legacy cron-governed (not window-guarded)
			Enabled:          true,
			KeepDaily:        &keep,
			NextRunAt:        &next,
			IsManagedDefault: true,
		}
		if err := s.deps.Schedules.Create(ctx, m); err != nil {
			// A concurrent panel instance won the race — the unique index on
			// is_managed_default rejects the second insert. Benign: the winner's
			// row is found + converged on the next tick.
			if errors.Is(err, repository.ErrConflict) {
				return
			}
			s.deps.Log.WarnContext(ctx, "default-local-backup: create schedule", "error", err)
			return
		}
		// Empty user list = all non-admin tenants at tick time (auto-covers new
		// users); link the local destination.
		if err := s.deps.Schedules.ReplaceUsers(ctx, m.ID, nil); err != nil {
			s.deps.Log.WarnContext(ctx, "default-local-backup: set users", "error", err)
		}
		if err := s.deps.Schedules.ReplaceDestinations(ctx, m.ID, []string{destID}); err != nil {
			s.deps.Log.WarnContext(ctx, "default-local-backup: set destination", "error", err)
		}
		s.deps.Log.InfoContext(ctx, "default-local-backup: created managed schedule", "cron", defaultLocalBackupCron, "keep_daily", keep)
		return
	}

	// Exists: re-enable if disabled, and heal a missing destination link. Don't
	// clobber cron/content/keep so an admin's later tweak sticks.
	if !managed.Enabled {
		managed.Enabled = true
		if err := s.deps.Schedules.Update(ctx, managed); err != nil {
			s.deps.Log.WarnContext(ctx, "default-local-backup: re-enable", "error", err)
			return
		}
		s.deps.Log.InfoContext(ctx, "default-local-backup: re-enabled (setting on)")
	}
	if dests, err := s.deps.Schedules.GetDestinations(ctx, managed.ID); err == nil && len(dests) == 0 {
		if err := s.deps.Schedules.ReplaceDestinations(ctx, managed.ID, []string{destID}); err != nil {
			s.deps.Log.WarnContext(ctx, "default-local-backup: relink destination", "error", err)
		}
	}
}

// ensureLocalDestination returns the id of a local backup destination, creating a
// shared "Local" one (empty URL → the agent's default repo, legacy shared restic
// password) if none exists.
func (s *Scheduler) ensureLocalDestination(ctx context.Context) (string, error) {
	dests, err := s.deps.Destinations.List(ctx)
	if err != nil {
		return "", err
	}
	for _, d := range dests {
		if d.Kind == models.BackupDestinationKindLocal {
			return d.ID, nil
		}
	}
	d := &models.BackupDestination{
		ID:      ids.NewULID(),
		Name:    defaultLocalDestName,
		Kind:    models.BackupDestinationKindLocal,
		URL:     "", // empty = the agent's default local repo /var/lib/jabali-backups/repo
		Enabled: true,
	}
	if err := s.deps.Destinations.Create(ctx, d); err != nil {
		return "", err
	}
	return d.ID, nil
}
