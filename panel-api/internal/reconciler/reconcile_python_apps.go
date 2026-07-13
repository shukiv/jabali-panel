package reconciler

import (
	"context"
	"encoding/json"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// WithPythonApps wires the python-app repository into the reconciler (ADR-0131).
func (r *Reconciler) WithPythonApps(repo repository.PythonAppRepository) *Reconciler {
	r.pythonApps = repo
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
