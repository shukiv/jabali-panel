package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// reconcileCronJobs converges cron_jobs DB rows -> systemd-user timers on disk.
//
// Per plan section 1 step 5 / section 3 invariants:
//  1. Fetch every cron_jobs row.
//  2. Group by user_id; resolve Linux username.
//  3. Compute owned docroots (Domains.ListByUserID) once per user.
//  4. Ask the agent which jabali-cron-<id>.timer files already exist on disk.
//  5. For each enabled DB row -> cron.apply (idempotent, agent skips no-op).
//  6. For each disabled DB row -> cron.remove.
//  7. For every on-disk ID that has no DB row -> cron.remove as orphan.
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
		if err != nil || u == nil || u.Username == nil || *u.Username == "" || u.IsAdmin {
			r.log.Warn("reconcile: skipping cron jobs — user missing or has no linux username",
				"user_id", userID, "err", err)
			continue
		}
		username := *u.Username

		docroots, domains, err := r.ownedTargetsFor(ctx, userID)
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
			if err := r.cronApplyOne(ctx, userID, username, job, docroots, domains); err != nil {
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
	orphans += r.sweepCronOrphansOnce(ctx, byUser)

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

// sweepCronOrphansOnce removes the cron units of accounts with no cron rows,
// which the loop above never visits. cron.remove used to look in the wrong
// directory (GH #273 moved the units, it did not follow), so a deleted job's
// timer kept running, and an account whose last job was deleted kept them
// all. The sweep runs once per panel start, which every update brings, and
// again on the next tick until a pass has no errors. A job created since this
// pass read the cron rows is left alone.
func (r *Reconciler) sweepCronOrphansOnce(ctx context.Context, withRows map[string][]*models.CronJob) int {
	if r.cronOrphanSweepDone.Load() {
		return 0
	}
	users, _, err := r.users.List(ctx, repository.ListOptions{Limit: 10000})
	if err != nil {
		r.log.Warn("reconcile: cron orphan sweep could not list users", "err", err)
		return 0
	}
	removed, failed := 0, false
	for _, u := range users {
		if _, ok := withRows[u.ID]; ok || u.IsAdmin || u.Username == nil || *u.Username == "" {
			continue
		}
		onDisk, err := r.cronListOnDisk(ctx, u.ID, *u.Username)
		if err != nil {
			r.log.Warn("reconcile: cron orphan sweep: agent cron.list failed", "user_id", u.ID, "err", err)
			failed = true
			continue
		}
		for jobID := range onDisk {
			if _, err := r.cronJobs.FindByID(ctx, jobID); err == nil {
				continue
			} else if !errors.Is(err, repository.ErrNotFound) {
				r.log.Warn("reconcile: cron orphan sweep: look up job", "job_id", jobID, "err", err)
				failed = true
				continue
			}
			if err := r.cronRemoveOne(ctx, u.ID, *u.Username, jobID); err != nil {
				r.log.Warn("reconcile: failed to clean orphan cron unit", "job_id", jobID, "err", err)
				failed = true
				continue
			}
			r.log.Info("reconcile: removed orphan cron unit", "job_id", jobID, "user_id", u.ID)
			removed++
		}
	}
	if !failed {
		r.cronOrphanSweepDone.Store(true)
	}
	return removed
}

func (r *Reconciler) ownedTargetsFor(ctx context.Context, userID string) (docroots, domainNames []string, err error) {
	if r.domains == nil {
		return nil, nil, nil
	}
	doms, _, err := r.domains.ListByUserID(ctx, userID, repository.ListOptions{Limit: 1000})
	if err != nil {
		return nil, nil, err
	}
	docroots = make([]string, 0, len(doms))
	domainNames = make([]string, 0, len(doms))
	for _, d := range doms {
		if d.DocRoot != "" {
			docroots = append(docroots, d.DocRoot)
		}
		if d.Name != "" {
			domainNames = append(domainNames, d.Name)
		}
	}
	return docroots, domainNames, nil
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

func (r *Reconciler) cronApplyOne(ctx context.Context, userID, username string, job *models.CronJob, docroots, domains []string) error {
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
		"owned_domains":  domains,
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
