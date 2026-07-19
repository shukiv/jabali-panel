package reconciler

import (
	"context"
	"encoding/json"

	"github.com/oklog/ulid/v2"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/pyframeworks"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// WithPythonApps wires the python-app repository into the reconciler (ADR-0131).
func (r *Reconciler) WithPythonApps(repo repository.PythonAppRepository) *Reconciler {
	r.pythonApps = repo
	return r
}

// WithPyFrameworks wires the JAB-164 framework marketplace catalog so the
// reconciler can run one-shot framework scaffolds. Optional.
func (r *Reconciler) WithPyFrameworks(cat *pyframeworks.Catalog) *Reconciler {
	r.pyFrameworks = cat
	return r
}

// reconcilePythonApps converges every Python app's runtime: it dispatches
// app.python.apply (venv + systemd unit) to the agent and writes back the
// resulting status. nginx is handled separately by the domain reconciler via
// the proxy_pass nginx rule the API attached to the domain. No-op without the
// repo/agent, or when the feature flag is off.
func (r *Reconciler) reconcilePythonApps(ctx context.Context) {
	if r.pythonApps == nil || r.agent == nil || r.users == nil {
		return
	}
	if r.serverSettings != nil {
		if s, err := r.serverSettings.Get(ctx); err == nil && s != nil && !s.PythonAppsEnabled {
			return // feature opt-out: leave apps in their last state
		}
	}
	apps, err := r.pythonApps.ListAll(ctx)
	if err != nil {
		r.log.Warn("pyapp: listAll failed", "err", err)
		return
	}
	for _, app := range apps {
		r.reconcileOnePythonApp(ctx, app)
	}
}

func (r *Reconciler) reconcileOnePythonApp(ctx context.Context, app *models.PythonApp) {
	if app.LoopbackPort == nil {
		return // API allocates the port on create; nothing to converge yet
	}
	user, err := r.users.FindByID(ctx, app.UserID)
	if err != nil || user == nil || user.Username == nil || *user.Username == "" || user.IsAdmin {
		r.failPythonApp(ctx, app.ID, "owner has no linux username")
		return
	}

	// JAB-164: one-shot framework scaffold (starter project + pinned deps +
	// generated secrets) before the first apply. Gated on scaffolded_at so it
	// never re-runs over the tenant's code.
	if app.Framework != "" && app.ScaffoldedAt == nil {
		if !r.scaffoldPythonApp(ctx, app, *user.Username) {
			return // scaffold failed; status already set to failed
		}
	}

	envRows, _ := r.pythonApps.ListEnv(ctx, app.ID)
	env := make(map[string]string, len(envRows))
	for _, e := range envRows {
		env[e.Key] = e.Value
	}

	params := map[string]any{
		"app_id":         app.ID,
		"username":       *user.Username,
		"user_id":        app.UserID,
		"app_root":       app.AppRoot,
		"python_version": app.PythonVersion,
		"app_type":       app.AppType,
		"entrypoint":     app.Entrypoint,
		"base_uri":       app.BaseURI,
		"port":           *app.LoopbackPort,
		"env":            env,
	}
	if app.StartCommand != nil {
		params["start_command"] = *app.StartCommand
	}
	if app.MemoryLimit != nil {
		params["memory_limit"] = *app.MemoryLimit
	}
	if app.CPULimit != nil {
		params["cpu_limit"] = *app.CPULimit
	}
	if app.PIDsLimit != nil {
		params["pids_limit"] = *app.PIDsLimit
	}

	raw, err := r.agent.Call(ctx, "app.python.apply", params)
	if err != nil {
		r.failPythonApp(ctx, app.ID, err.Error())
		return
	}
	var res struct {
		Active bool   `json:"active"`
		Unit   string `json:"unit"`
	}
	_ = json.Unmarshal(raw, &res)
	status := models.PythonAppStatusFailed
	var lastErr *string
	if res.Active {
		status = models.PythonAppStatusRunning
	} else {
		m := "app started but is not active — check the app logs (journalctl -u " + res.Unit + ")"
		lastErr = &m
		// GH #357: surface the failure server-side, not just in the DB row —
		// otherwise an app that goes pending→failed leaves "nothing in logs".
		r.log.Warn("pyapp: app applied but not active", "id", app.ID, "unit", res.Unit)
	}
	if app.Status != status {
		if err := r.pythonApps.UpdateStatus(ctx, app.ID, status, lastErr); err != nil {
			r.log.Warn("pyapp: status update failed", "id", app.ID, "err", err)
		}
	}
}

func (r *Reconciler) failPythonApp(ctx context.Context, id, msg string) {
	// GH #357: log the reason so a failed apply is diagnosable from the server
	// log, not only the app's last_error column. The agent returns detailed
	// errors (pip install / systemd restart output) that were previously
	// swallowed into the DB row alone.
	r.log.Warn("pyapp: apply failed", "id", id, "reason", msg)
	if err := r.pythonApps.UpdateStatus(ctx, id, models.PythonAppStatusFailed, &msg); err != nil {
		r.log.Warn("pyapp: fail status update failed", "id", id, "err", err)
	}
}

// scaffoldPythonApp lays down the framework starter (venv + pinned deps +
// starter project + optional Django hardening) via app.python.scaffold, persists
// generated secrets + framework default env, and stamps scaffolded_at. Returns
// false and marks the app failed on any error. Runs once per app.
func (r *Reconciler) scaffoldPythonApp(ctx context.Context, app *models.PythonApp, username string) bool {
	if r.pyFrameworks == nil {
		r.failPythonApp(ctx, app.ID, "framework catalog unavailable")
		return false
	}
	entry, ok := r.pyFrameworks.Get(app.Framework)
	if !ok {
		r.failPythonApp(ctx, app.ID, "unknown framework "+app.Framework)
		return false
	}
	spec, err := entry.ResolveCreate()
	if err != nil {
		r.failPythonApp(ctx, app.ID, "resolve framework: "+err.Error())
		return false
	}

	var domainName string
	if r.domains != nil {
		if d, derr := r.domains.FindByID(ctx, app.DomainID); derr == nil && d != nil {
			domainName = d.Name
		}
	}

	params := map[string]any{
		"app_id":         app.ID,
		"username":       username,
		"app_root":       app.AppRoot,
		"python_version": app.PythonVersion,
		"server":         spec.Server,
		"requirements":   spec.Requirements,
		"scaffold_kind":  spec.ScaffoldKind,
	}
	if spec.ScaffoldKind == "cmd" {
		params["scaffold_argv"] = spec.ScaffoldCommand
		params["project"] = spec.Project
	} else {
		params["template_files"] = spec.TemplateFiles
	}
	if spec.HardenSettings {
		params["harden_django"] = true
		params["static_root"] = spec.StaticRoot
		params["allowed_hosts"] = domainName
	}
	if len(spec.PostInstall) > 0 {
		params["post_install"] = spec.PostInstall
	}

	raw, err := r.agent.Call(ctx, "app.python.scaffold", params)
	if err != nil {
		r.failPythonApp(ctx, app.ID, "scaffold: "+err.Error())
		return false
	}
	var res struct {
		GeneratedEnv map[string]string `json:"generated_env"`
		Skipped      bool              `json:"skipped"`
	}
	_ = json.Unmarshal(raw, &res)

	r.persistFrameworkEnv(ctx, app, spec, res.GeneratedEnv, domainName)
	if err := r.pythonApps.MarkScaffolded(ctx, app.ID); err != nil {
		r.log.Warn("pyapp: mark scaffolded failed", "id", app.ID, "err", err)
	}
	return true
}

// persistFrameworkEnv merges the framework's default + generated env into the
// app's env set WITHOUT clobbering values the operator already set. Generated
// secrets (e.g. Django SECRET_KEY) come from the agent's scaffold result;
// DJANGO_ALLOWED_HOSTS defaults to the mounted domain when the catalog leaves it
// blank. Secrets live only in the app env (never in source), per ADR-0131.
func (r *Reconciler) persistFrameworkEnv(ctx context.Context, app *models.PythonApp, spec pyframeworks.CreateSpec, generated map[string]string, domainName string) {
	existing, _ := r.pythonApps.ListEnv(ctx, app.ID)
	have := make(map[string]bool, len(existing))
	merged := make([]models.PythonAppEnv, 0, len(existing)+len(spec.Env))
	for _, e := range existing {
		have[e.Key] = true
		merged = append(merged, models.PythonAppEnv{ID: e.ID, AppID: app.ID, Key: e.Key, Value: e.Value})
	}
	for _, v := range spec.Env {
		if v.Key == "" || have[v.Key] {
			continue // never overwrite an operator-set value
		}
		val := v.Default
		if v.Generate != "" {
			val = generated[v.Key]
		}
		if v.Key == "DJANGO_ALLOWED_HOSTS" && val == "" {
			val = domainName
		}
		have[v.Key] = true
		merged = append(merged, models.PythonAppEnv{ID: ulid.Make().String(), AppID: app.ID, Key: v.Key, Value: val})
	}
	if err := r.pythonApps.ReplaceEnv(ctx, app.ID, merged); err != nil {
		r.log.Warn("pyapp: persist framework env failed", "id", app.ID, "err", err)
	}
}
