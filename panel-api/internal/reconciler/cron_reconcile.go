package reconciler

import (
	"context"
	"encoding/json"
	"time"

	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/models"
	"git.linux-hosting.co.il/shukivaknin/jabali2/panel-api/internal/repository"
)

// reconcileCronJobs converges cron_jobs DB rows -> systemd-user timers on disk.
//
// Per plan section 1 step 5 / section 3 invariants:
//   1. Fetch every cron_jobs row.
//   2. Group by user_id; resolve Linux username.
//   3. Compute owned docroots (Domains.ListByUserID) once per user.
//   4. Ask the agent which jabali-cron-<id>.timer files already exist on disk.
//   5. For each enabled DB row -> cron.apply (idempotent, agent skips no-op).
//   6. For each disabled DB row -> cron.remove.
//   7. For every on-disk ID that has no DB row -> cron.remove as orphan.
//
// First agent error per user is logged and the user is skipped; the rest of the
// pass continues. Mirrors the shape of reconcileWordPressInstalls.
func (r *Reconciler) reconcileCronJobs(ctx context.Context) {
	if r.cronJobs == nil {
		return
	}
	r.log.Debug("reconcile: starting cron jobs pass")

	jobs, err := r.cronJobs.ListAll(ctx)
	if err != nil {
		r.log.Error("reconcile: failed to list cron jobs", "err", err)
		return
	}

	byUser := make(map[string][]*models.CronJob, 16)
	for _, job := range jobs {
		byUser[job.UserID] = append(byUser[job.UserID], job)
	}

	var applied, removed, orphans int

	for userID, userJobs := range byUser {
		u, err := r.users.FindByID(ctx, userID)
		if err != nil || u == nil || u.Username == nil || *u.Username == "" {
			r.log.Warn("reconcile: skipping cron jobs — user missing or has no linux username",
				"user_id", userID, "err", err)
			continue
		}
		username := *u.Username

		docroots, err := r.ownedDocrootsFor(ctx, userID)
		if err != nil {
			r.log.Warn("reconcile: failed to list owned docroots for cron", "user_id", userID, "err", err)
			continue
		}

		onDisk, err := r.cronListOnDisk(ctx, userID, username)
		if err != nil {
			r.log.Error("reconcile: agent cron.list failed", "user_id", userID, "err", err)
			continue
		}

		for _, job := range userJobs {
			if !job.Enabled {
				continue
			}
			if err := r.cronApplyOne(ctx, userID, username, job, docroots); err != nil {
				r.log.Error("reconcile: agent cron.apply failed", "job_id", job.ID, "err", err)
				continue
			}
			applied++
			delete(onDisk, job.ID)
			// Harvest the last autonomous (timer) run so the panel's
			// "Last Run" reflects scheduled fires, not just Run-now.
			r.cronHarvestLastRun(ctx, userID, username, job)
		}

		for _, job := range userJobs {
			if job.Enabled {
				continue
			}
			if err := r.cronRemoveOne(ctx, userID, username, job.ID); err != nil {
				r.log.Warn("reconcile: agent cron.remove (disabled) failed", "job_id", job.ID, "err", err)
				continue
			}
			removed++
			delete(onDisk, job.ID)
		}

		for orphanID := range onDisk {
			if err := r.cronRemoveOne(ctx, userID, username, orphanID); err != nil {
				r.log.Warn("reconcile: failed to clean orphan cron unit", "job_id", orphanID, "err", err)
				continue
			}
			r.log.Info("reconcile: removed orphan cron unit", "job_id", orphanID, "user_id", userID)
			orphans++
		}
	}

	// Demote to Debug when nothing changed — this fires every tick
	// (~60s) and was the dominant remaining log noise after the
	// per-tick-storm cleanup work. INFO when something actually
	// happened (apply/remove/orphan-removal) stays visible.
	if applied == 0 && removed == 0 && orphans == 0 {
		r.log.Debug("reconcile: cron jobs pass complete (no-op)",
			"applied", applied, "removed", removed, "orphans_removed", orphans)
	} else {
		r.log.Info("reconcile: cron jobs pass complete",
			"applied", applied, "removed", removed, "orphans_removed", orphans)
	}
}

func (r *Reconciler) ownedDocrootsFor(ctx context.Context, userID string) ([]string, error) {
	if r.domains == nil {
		return nil, nil
	}
	domains, _, err := r.domains.ListByUserID(ctx, userID, repository.ListOptions{Limit: 1000})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(domains))
	for _, d := range domains {
		if d.DocRoot != "" {
			out = append(out, d.DocRoot)
		}
	}
	return out, nil
}

func (r *Reconciler) cronListOnDisk(ctx context.Context, userID, username string) (map[string]struct{}, error) {
	agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result, err := r.agent.Call(agentCtx, "cron.list", map[string]any{
		"user_id":  userID,
		"username": username,
	})
	if err != nil {
		return nil, err
	}
	var resp struct {
		UnitFiles []string `json:"unit_files"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(resp.UnitFiles))
	for _, id := range resp.UnitFiles {
		set[id] = struct{}{}
	}
	return set, nil
}

// cronHarvestLastRun polls the user cron's systemd service for its last
// completion (cron.status) and writes last_run_at/last_exit_code back to
// the DB — but ONLY when the systemd timestamp is newer than the stored
// value, so a recent manual Run-now (#275) isn't clobbered by an older
// scheduled-fire poll (and vice-versa). Best-effort: any failure is
// logged at Debug and the pass continues (display-only).
func (r *Reconciler) cronHarvestLastRun(ctx context.Context, userID, username string, job *models.CronJob) {
	if r.cronJobs == nil {
		return
	}
	agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := r.agent.Call(agentCtx, "cron.status", map[string]any{
		"user_id":  userID,
		"username": username,
		"job_id":   job.ID,
	})
	if err != nil {
		r.log.Debug("reconcile: cron.status failed", "job_id", job.ID, "err", err)
		return
	}
	var st struct {
		HasRun    bool   `json:"has_run"`
		LastRunAt string `json:"last_run_at"`
		ExitCode  int    `json:"exit_code"`
	}
	if jErr := json.Unmarshal(raw, &st); jErr != nil || !st.HasRun {
		return
	}
	t, pErr := time.Parse(time.RFC3339, st.LastRunAt)
	if pErr != nil {
		return
	}
	// Don't clobber a newer stored value (e.g. a Run-now that just landed).
	if job.LastRunAt != nil && !t.After(*job.LastRunAt) {
		return
	}
	if uErr := r.cronJobs.UpdateStatus(ctx, job.ID, t.UTC(), st.ExitCode, ""); uErr != nil {
		r.log.Debug("reconcile: cron.status write-back failed", "job_id", job.ID, "err", uErr)
	}
}

func (r *Reconciler) cronApplyOne(ctx context.Context, userID, username string, job *models.CronJob, docroots []string) error {
	agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := r.agent.Call(agentCtx, "cron.apply", map[string]any{
		"user_id":        userID,
		"username":       username,
		"job_id":         job.ID,
		"name":           job.Name,
		"command":        job.Command,
		"schedule":       job.Schedule,
		"owned_docroots": docroots,
	})
	return err
}

func (r *Reconciler) cronRemoveOne(ctx context.Context, userID, username, jobID string) error {
	agentCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := r.agent.Call(agentCtx, "cron.remove", map[string]any{
		"user_id":  userID,
		"username": username,
		"job_id":   jobID,
	})
	return err
}
