package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dockerapp"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// docker_app_env.go — view / edit / regenerate a Docker app's environment.
//
// The catalog generates per-install secrets (admin passwords, DB passwords,
// JWT/master keys) into the on-disk .env, but they were never surfaced, so
// an operator couldn't find an app's admin credential. These admin-only
// routes read the .env back (docker_app.read_env), let the operator edit a
// value, and regenerate a generated secret — each re-renders the compose
// and recreates the container via docker_app.update.
//
//	GET  /admin/docker-apps/:id/env             — list env (secrets revealed)
//	PUT  /admin/docker-apps/:id/env             — edit values, re-render, recreate
//	POST /admin/docker-apps/:id/env/regenerate  — fresh value for a generated key

// maxEnvValueLen caps an operator-supplied env value. Generous for tokens
// and connection strings; small enough to reject accidental file pastes.
const maxEnvValueLen = 4096

type envVarView struct {
	Name      string `json:"name"`
	Value     string `json:"value"`
	Secret    bool   `json:"secret"`    // render masked in the UI
	Generated bool   `json:"generated"` // has a regenerate action
}

// loadAppForEnv resolves the row + catalog entry, writing the HTTP error
// itself and returning ok=false when anything is missing.
func (h *dockerAppHandler) loadAppForEnv(c *gin.Context) (*models.DockerApp, dockerapp.Entry, bool) {
	ctx := c.Request.Context()
	app, err := h.cfg.Repo.FindByID(ctx, c.Param("id"))
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal_error"})
		}
		return nil, dockerapp.Entry{}, false
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return nil, dockerapp.Entry{}, false
	}
	if h.cfg.Catalog == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "catalog_unavailable"})
		return nil, dockerapp.Entry{}, false
	}
	entry, ok := h.cfg.Catalog.Get(app.Slug)
	if !ok {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "catalog_entry_missing"})
		return nil, dockerapp.Entry{}, false
	}
	return app, entry, true
}

// getEnv lists the install's environment with the actual on-disk values,
// flagging which are secrets (UI masks them) and which are generated (UI
// shows a regenerate button). Catalog order first, then any extra keys.
func (h *dockerAppHandler) getEnv(c *gin.Context) {
	app, entry, ok := h.loadAppForEnv(c)
	if !ok {
		return
	}
	env, err := h.readInstallEnv(c.Request.Context(), app.EffectiveSlug())
	if err != nil {
		respondAgentErr(c, "read_env_failed", err)
		return
	}
	out := make([]envVarView, 0, len(env))
	seen := make(map[string]bool, len(entry.Env))
	for _, ev := range entry.Env {
		seen[ev.Name] = true
		out = append(out, envVarView{
			Name:      ev.Name,
			Value:     env[ev.Name],
			Secret:    ev.Secret,
			Generated: ev.Generate != "",
		})
	}
	for k, v := range env {
		if !seen[k] {
			out = append(out, envVarView{Name: k, Value: v})
		}
	}
	c.JSON(http.StatusOK, gin.H{"env": out})
}

// putEnv applies operator edits: merge over the existing .env, re-render the
// compose, and recreate the container so the new values take effect.
func (h *dockerAppHandler) putEnv(c *gin.Context) {
	app, _, ok := h.loadAppForEnv(c)
	if !ok {
		return
	}
	var body struct {
		Env map[string]string `json:"env"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || len(body.Env) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "detail": "env map required"})
		return
	}
	for k, v := range body.Env {
		if msg := validateEnvKV(k, v); msg != "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_env", "detail": msg})
			return
		}
	}

	ctx := c.Request.Context()
	existing, err := h.readInstallEnv(ctx, app.EffectiveSlug())
	if err != nil {
		respondAgentErr(c, "read_env_failed", err)
		return
	}
	merged := make(map[string]string, len(existing)+len(body.Env))
	for k, v := range existing {
		merged[k] = v
	}
	for k, v := range body.Env {
		merged[k] = v
	}

	if err := h.applyEnv(ctx, app, merged); err != nil {
		respondAgentErr(c, "apply_failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "applied"})
}

// regenerateEnv mints a fresh value for a single generated secret. Drops the
// key from the override set so MaterialiseEnv regenerates it, applies, then
// reads it back so the UI can reveal the new value once.
func (h *dockerAppHandler) regenerateEnv(c *gin.Context) {
	app, entry, ok := h.loadAppForEnv(c)
	if !ok {
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Key == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "detail": "key required"})
		return
	}
	generated := false
	for _, ev := range entry.Env {
		if ev.Name == body.Key && ev.Generate != "" {
			generated = true
			break
		}
	}
	if !generated {
		c.JSON(http.StatusBadRequest, gin.H{"error": "not_generated", "detail": "key is not a generated secret"})
		return
	}

	ctx := c.Request.Context()
	existing, err := h.readInstallEnv(ctx, app.EffectiveSlug())
	if err != nil {
		respondAgentErr(c, "read_env_failed", err)
		return
	}
	delete(existing, body.Key) // absent → MaterialiseEnv generates a fresh value

	if err := h.applyEnv(ctx, app, existing); err != nil {
		respondAgentErr(c, "apply_failed", err)
		return
	}
	// Read back so the UI can show the new secret once.
	after, err := h.readInstallEnv(ctx, app.EffectiveSlug())
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"status": "applied"}) // applied, just can't echo the value
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "applied", "key": body.Key, "value": after[body.Key]})
}

// applyEnv re-renders the compose with the given env override set and
// recreates the container via docker_app.update (which writes compose +
// .env atomically, then recreates). Restores the running status on success.
func (h *dockerAppHandler) applyEnv(ctx context.Context, app *models.DockerApp, overrideEnv map[string]string) error {
	domain := h.installDomain(ctx, app.ID)
	composeYML, envFile, err := h.renderInstallCompose(ctx, app, domain, overrideEnv)
	if err != nil {
		return err
	}
	_ = h.cfg.Repo.UpdateStatus(ctx, app.ID, models.DockerAppStatusUpdating, nil)
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	envUpdateParams := map[string]any{
		"slug":                        app.EffectiveSlug(),
		"compose_yml":                 composeYML,
		"env_file":                    envFile,
		"healthcheck_timeout_seconds": 300,
	}
	if verr := h.applyTenantValidateParams(ctx, app, envUpdateParams); verr != nil {
		return verr
	}
	_, callErr := h.cfg.Agent.Call(callCtx, "docker_app.update", envUpdateParams)
	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer persistCancel()
	if callErr != nil {
		msg := firstLineString(callErr.Error())
		_ = h.cfg.Repo.UpdateStatus(persistCtx, app.ID, models.DockerAppStatusFailed, &msg)
		return callErr
	}
	_ = h.cfg.Repo.UpdateStatus(persistCtx, app.ID, models.DockerAppStatusRunning, nil)
	return nil
}

// validateEnvKV rejects keys/values that would corrupt the .env file or the
// compose render. Keys: shell-env shape. Values: no newlines/NUL, bounded.
func validateEnvKV(k, v string) string {
	if k == "" {
		return "empty key"
	}
	for _, r := range k {
		if !(r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return "key " + k + " has an invalid character (allowed: A-Z a-z 0-9 _)"
		}
	}
	if len(v) > maxEnvValueLen {
		return "value for " + k + " is too long"
	}
	if strings.ContainsAny(v, "\n\r\x00") {
		return "value for " + k + " may not contain newlines or NUL"
	}
	return ""
}
