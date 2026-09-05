package reconciler

import (
	"context"
	"encoding/json"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/notifications"
)

// M53 Updates Center — run reconciler (ADR-0118).
//
// `jabali update` / apt upgrades run as `systemd-run --no-block` transient
// units, so panel-api never synchronously sees their terminal state. The run
// handler logs an update_history row as `running` at dispatch; this tick flips
// it to success/failed by polling the unit — independent of whether any UI is
// polling, so a row never gets stuck `running` just because the page closed.
//
// Terminal mapping leans on the run handlers dropping `--collect`: a *failed*
// oneshot lingers in systemd "failed" state (readable), while a *successful*
// one is garbage-collected to inactive/unknown. So:
//   active | activating | reloading -> still running (skip)
//   failed (or non-zero ExecMainStatus) -> failed
//   anything else (inactive/unknown/gone) -> success

// staleOrphanRunTimeout bounds how long a running row with no pollable unit may
// linger before the reconciler reaps it as failed. No update runs this long; a
// shorter window risks false-failing a legitimately slow run.
const staleOrphanRunTimeout = 30 * time.Minute

// maxRunningRunAge is a HARD backstop (GH #1486): any running row older than this
// is reaped as failed regardless of unit or poll outcome. Without it a run could
// spin "running" in the Tasks indicator forever — the per-unit poll path had no
// timeout, so a pollable unit whose agent status call errors (unreachable/old
// agent) or whose transient unit hangs "activating" never sealed, and the CLI
// self-update path stamps a unit the poller doesn't recognise (jabali-autoupdate)
// whose finish() closure is skipped if the update restarts the process. No real
// update runs anywhere near this long.
const maxRunningRunAge = 2 * time.Hour

// statusCmdForUnit maps a run unit to its agent status command. Only the two
// known transient units are pollable; anything else is left alone.
func statusCmdForUnit(unit string) (string, bool) {
	switch unit {
	case "jabali-update-oneshot.service":
		return "system.update_status", true
	case "jabali-apt-oneshot.service":
		return "system.apt_status", true
	}
	return "", false
}

// unitStatusView is the subset of the agent's unit-status reply we need.
type unitStatusView struct {
	Status   string `json:"status"`
	ExitCode *int   `json:"exit_code"`
	LogTail  string `json:"log_tail"`
}

func (r *Reconciler) reconcileUpdateRuns(ctx context.Context) {
	if r.updateRunHistory == nil || r.agent == nil {
		return
	}
	rows, err := r.updateRunHistory.ListRunning(ctx)
	if err != nil {
		r.log.Debug("update-run reconcile: list running failed", "error", err)
		return
	}
	for i := range rows {
		row := rows[i]
		// GH #1486 hard backstop: reap any run that has spun far past the longest
		// plausible update, no matter its unit or why the normal path didn't seal
		// it (agent status call erroring, unit hung "activating", or an
		// unrecognised/legacy unit). The fast paths below still seal a healthy run
		// in one tick; this only catches the ones that would otherwise never clear.
		if time.Since(row.StartedAt) > maxRunningRunAge {
			if err := r.updateRunHistory.MarkFinished(ctx, row.ID, models.UpdateStatusFailed, "timed out — no completion signal within 2h", ""); err != nil {
				r.log.Warn("update-run reconcile: reap stale run failed", "id", row.ID, "error", err)
			}
			continue
		}
		cmd, ok := statusCmdForUnit(row.Unit)
		if !ok {
			// No pollable unit (empty/unknown) — can't reconcile via systemd.
			// A panel restart orphans such a row forever and the tasks
			// indicator shows a phantom "running" task. Reap it as failed once
			// it is clearly stale (legacy rows; current handlers always set a
			// unit). Keeps recent unit-less rows, if any, untouched.
			if time.Since(row.StartedAt) > staleOrphanRunTimeout {
				if err := r.updateRunHistory.MarkFinished(ctx, row.ID, models.UpdateStatusFailed, "orphaned — no unit to reconcile", ""); err != nil {
					r.log.Warn("update-run reconcile: reap orphan failed", "id", row.ID, "error", err)
				}
			}
			continue
		}
		raw, cerr := r.agent.Call(ctx, cmd, map[string]any{
			"since": row.StartedAt.UTC().Format(time.RFC3339),
		})
		if cerr != nil {
			r.log.Debug("update-run reconcile: status call failed", "unit", row.Unit, "error", cerr)
			// GH #1486: a pollable unit whose status can't be read (agent down /
			// too old to answer this verb) must not spin forever. Reap it once
			// clearly stale, like an orphan — retry next tick below that window.
			if time.Since(row.StartedAt) > staleOrphanRunTimeout {
				if err := r.updateRunHistory.MarkFinished(ctx, row.ID, models.UpdateStatusFailed, "no completion signal — update status unavailable from the agent", ""); err != nil {
					r.log.Warn("update-run reconcile: reap unpollable run failed", "id", row.ID, "error", err)
				}
			}
			continue
		}
		var v unitStatusView
		if json.Unmarshal(raw, &v) != nil {
			continue
		}
		switch v.Status {
		case "active", "activating", "reloading":
			continue // still running
		}
		status := models.UpdateStatusSuccess
		summary := "completed"
		if v.Status == "failed" || (v.ExitCode != nil && *v.ExitCode != 0) {
			status = models.UpdateStatusFailed
			summary = "failed"
		}
		excerpt := v.LogTail
		if len(excerpt) > 4000 {
			excerpt = excerpt[len(excerpt)-4000:]
		}
		if err := r.updateRunHistory.MarkFinished(ctx, row.ID, status, summary, excerpt); err != nil {
			r.log.Warn("update-run reconcile: mark finished failed", "id", row.ID, "error", err)
			continue
		}
		r.publishUpdateCompleted(ctx, row.Kind, status)
	}
}

// publishUpdateCompleted broadcasts an M14 notification when an update run
// finishes (panel `jabali` or `apt` system packages). Best-effort; a nil
// queue (notifications not wired) or publish error is logged, never fatal.
func (r *Reconciler) publishUpdateCompleted(ctx context.Context, kind, status string) {
	if r.notificationQueue == nil {
		return
	}
	what := "Panel"
	if kind == models.UpdateKindApt {
		what = "System packages"
	}
	sev, verb := "info", "updated"
	if status == models.UpdateStatusFailed {
		sev, verb = "warning", "update failed"
	}
	if _, err := r.notificationQueue.Publish(ctx, notifications.Envelope{
		EventKind: "update.completed",
		Severity:  sev,
		Title:     what + " " + verb,
		Body:      what + " " + verb + " on this server.",
		Deeplink:  "/jabali-admin/updates",
	}); err != nil {
		r.log.Warn("update-run reconcile: publish update.completed failed", "kind", kind, "error", err)
	}
}
