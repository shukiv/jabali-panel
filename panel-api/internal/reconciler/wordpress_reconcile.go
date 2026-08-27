package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// reconcileWordPressInstalls sweeps stuck WordPress installs and probes drift in ready installs.
//
// For rows exceeding their state's timeout (installing, cloning, deleting):
// - Mark as failed with an appropriate error message.
//
// For ready rows:
// - Probe the docroot for wp-includes/version.php existence.
// - If missing, mark as failed.
// - If present and version column is NULL/empty, attempt to parse and store the version.
func (r *Reconciler) reconcileWordPressInstalls(ctx context.Context) {
	if r.wordPressInstalls == nil {
		return
	}

	r.log.Debug("reconcile: starting WordPress installs pass")

	// Fetch all WordPress installs for sweeping stuck rows.
	installs, _, err := r.wordPressInstalls.List(ctx, repository.ListOptions{Limit: 10000})
	if err != nil {
		r.log.Error("reconcile: failed to list WordPress installs", "err", err)
		return
	}

	now := time.Now()
	installTimeout := r.cfg.WordPress.InstallTimeout
	cloneTimeout := r.cfg.WordPress.CloneTimeout
	deleteTimeout := r.cfg.WordPress.DeleteTimeout

	// Sweep stuck rows and mark them as failed.
	for _, install := range installs {
		age := now.Sub(install.UpdatedAt)

		switch install.Status {
		case "installing":
			if age > installTimeout {
				errMsg := fmt.Sprintf("operation exceeded %v timeout", installTimeout)
				r.log.Warn("reconcile: marking stuck install as failed", "id", install.ID, "age", age, "timeout", installTimeout)
				if err := r.wordPressInstalls.UpdateStatus(ctx, install.ID, "failed", &errMsg, nil); err != nil {
					r.log.Error("reconcile: failed to update install status", "id", install.ID, "err", err)
				}
			}

		case "cloning":
			if age > cloneTimeout {
				errMsg := fmt.Sprintf("operation exceeded %v timeout", cloneTimeout)
				r.log.Warn("reconcile: marking stuck clone as failed", "id", install.ID, "age", age, "timeout", cloneTimeout)
				if err := r.wordPressInstalls.UpdateStatus(ctx, install.ID, "failed", &errMsg, nil); err != nil {
					r.log.Error("reconcile: failed to update clone status", "id", install.ID, "err", err)
				}
			}

		case "deleting":
			if age > deleteTimeout {
				errMsg := fmt.Sprintf("operation exceeded %v timeout", deleteTimeout)
				r.log.Warn("reconcile: marking stuck delete as failed", "id", install.ID, "age", age, "timeout", deleteTimeout)
				if err := r.wordPressInstalls.UpdateStatus(ctx, install.ID, "failed", &errMsg, nil); err != nil {
					r.log.Error("reconcile: failed to update delete status", "id", install.ID, "err", err)
				}
			}
		}
	}

	// Probe ready installs for drift (version.php existence and content).
	r.probeReadyWordPressInstalls(ctx, installs)
}

// probeReadyWordPressInstalls checks ready WordPress installs for drift.
// It limits probes to ProbeBatch per tick to avoid reconciler dominance.
// Probes are round-robin by updated_at (oldest first) for fair revisit timing.
//
// `installs` is unused — we re-fetch a status='ready' / ORDER BY
// updated_at ASC / LIMIT ProbeBatch slice straight from the repo so
// the sort + filter happen in SQL. Keeping the param signature
// stable for the caller.
func (r *Reconciler) probeReadyWordPressInstalls(ctx context.Context, _ []models.WordPressInstall) {
	if r.cfg.WordPress.ProbeBatch <= 0 {
		return
	}

	readyInstalls, err := r.wordPressInstalls.ListReadyByUpdatedAtAsc(ctx, r.cfg.WordPress.ProbeBatch)
	if err != nil {
		r.log.Warn("reconcile: list ready installs failed", "err", err)
		return
	}
	if len(readyInstalls) == 0 {
		return
	}

	r.log.Debug("reconcile: probing WordPress installs", "count", len(readyInstalls))

	for _, install := range readyInstalls {
		r.probeOneWordPressInstall(ctx, install)
	}
}

// probeOneWordPressInstall stats <docroot>/<subdir>/wp-includes/version.php
// via the agent. If the file is gone, the install drifted (manual
// deletion, failed restore, etc.) — flip status to 'failed' so the
// operator UI surfaces it instead of silently showing 'ready'.
//
// Stat failure that ISN'T file-not-found (permission denied, agent
// unreachable) logs at warn but does not flip status — reconciler
// retries next tick.
func (r *Reconciler) probeOneWordPressInstall(ctx context.Context, install models.ApplicationInstall) {
	if r.agent == nil || r.domains == nil {
		return
	}
	// GH #378: the version.php probe is WordPress-specific. Never run it against
	// another app type — belt-and-braces behind the app_type-scoped query.
	if !strings.EqualFold(install.AppType, "wordpress") {
		return
	}
	dom, err := r.domains.FindByID(ctx, install.DomainID)
	if err != nil || dom == nil || dom.DocRoot == "" {
		return
	}
	subdir := install.Subdirectory
	if subdir != "" && subdir[0] != '/' {
		subdir = "/" + subdir
	}
	wpRoot := dom.DocRoot + subdir

	callCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := r.agent.Call(callCtx, "wordpress.version_probe", map[string]any{"dir": wpRoot})
	if err != nil {
		r.log.Warn("reconcile: WordPress version probe failed",
			"id", install.ID, "dir", wpRoot, "err", err)
		return
	}
	var probe struct {
		Exists  bool   `json:"exists"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		r.log.Warn("reconcile: WordPress probe parse failed", "id", install.ID, "err", err)
		return
	}
	if !probe.Exists {
		errMsg := "wp-includes/version.php missing — install drifted"
		r.log.Warn("reconcile: WordPress install drift detected; marking failed",
			"id", install.ID, "dir", wpRoot)
		if err := r.wordPressInstalls.UpdateStatus(ctx, install.ID, "failed", &errMsg, nil); err != nil {
			r.log.Error("reconcile: failed to flip drifted install to failed",
				"id", install.ID, "err", err)
		}
		return
	}
	// GH #1237: refresh the stored version when the site was updated (WP core
	// auto-update or an in-app update). The panel captured the version once at
	// install and never re-read it, so the Applications UI kept showing a stale
	// number. Write only on drift to avoid needless updated_at churn.
	if probe.Version != "" && (install.Version == nil || *install.Version != probe.Version) {
		newVer := probe.Version
		if err := r.wordPressInstalls.UpdateStatus(ctx, install.ID, "ready", nil, &newVer); err != nil {
			r.log.Warn("reconcile: failed to refresh WordPress version",
				"id", install.ID, "version", newVer, "err", err)
		} else {
			old := ""
			if install.Version != nil {
				old = *install.Version
			}
			r.log.Info("reconcile: refreshed WordPress version",
				"id", install.ID, "from", old, "to", newVer)
		}
	}
}
