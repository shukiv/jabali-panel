package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"path"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// applications_cache_doctor.go — JAB-11 fleet half. A cache-doctor / repair /
// refresh sweep touches every cache-enabled WordPress install with one agent
// call each, so a synchronous HTTP sweep would exceed the proxy timeout. The
// POST creates a run row and kicks off a background goroutine; the admin GUI
// polls the run (and lists history). Mirrors the JAB-95 warmup async-run shape.

// cacheDoctorInstallCallTimeout bounds each per-install agent call so one hung
// install can't stall the whole sweep.
const cacheDoctorInstallCallTimeout = 90 * time.Second

// cacheDoctorSweepTimeout bounds the whole background sweep.
const cacheDoctorSweepTimeout = 30 * time.Minute

// startCacheDoctorRun (POST /admin/cache/doctor-runs) starts a fleet sweep of
// the given kind (doctor|repair|refresh) and returns the run id immediately.
func (h *wordPressHandler) startCacheDoctorRun(c *gin.Context) {
	if h.cfg.CacheDoctorRuns == nil || h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
		return
	}
	var body struct {
		Kind string `json:"kind"`
	}
	_ = c.ShouldBindJSON(&body)
	kind := body.Kind
	switch kind {
	case models.CacheDoctorKindDoctor, models.CacheDoctorKindRepair, models.CacheDoctorKindRefresh:
	case "":
		kind = models.CacheDoctorKindDoctor
	default:
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "bad_kind", "detail": "kind must be doctor, repair, or refresh"})
		return
	}
	// Repair re-provisions via SetApplicationCache, which needs the full cache
	// wiring (Redis + token secret). Refuse rather than silently no-op.
	if kind == models.CacheDoctorKindRepair && h.cfg.Redis == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "repair_unavailable", "detail": "cache repair needs Redis wiring"})
		return
	}

	// One sweep at a time — a second concurrent fleet pass just doubles agent
	// load and races on the same installs.
	if active, err := h.cfg.CacheDoctorRuns.CountActive(c.Request.Context()); err == nil && active > 0 {
		c.JSON(http.StatusConflict, gin.H{"error": "run_in_progress"})
		return
	}

	triggeredBy := ""
	if claims := ginctx.Claims(c); claims != nil {
		triggeredBy = claims.UserID
	}
	run := &models.CacheDoctorRun{
		ID:          ids.NewULID(),
		Kind:        kind,
		State:       models.CacheDoctorStateQueued,
		TriggeredBy: triggeredBy,
	}
	if err := h.cfg.CacheDoctorRuns.Create(c.Request.Context(), run); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create_run"})
		return
	}

	go h.runCacheDoctorSweep(run.ID, kind, triggeredBy)

	c.JSON(http.StatusAccepted, gin.H{"id": run.ID, "kind": kind, "state": run.State})
}

// listCacheDoctorRuns (GET /admin/cache/doctor-runs) returns recent runs (history).
func (h *wordPressHandler) listCacheDoctorRuns(c *gin.Context) {
	if h.cfg.CacheDoctorRuns == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
		return
	}
	runs, err := h.cfg.CacheDoctorRuns.ListRecent(c.Request.Context(), 50)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list_runs"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"runs": runs})
}

// getCacheDoctorRun (GET /admin/cache/doctor-runs/:id) returns one run for polling.
func (h *wordPressHandler) getCacheDoctorRun(c *gin.Context) {
	if h.cfg.CacheDoctorRuns == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
		return
	}
	run, err := h.cfg.CacheDoctorRuns.Get(c.Request.Context(), c.Param("id"))
	if err != nil {
		if err == repository.ErrNotFound {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "get_run"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"run": run})
}

// runCacheDoctorSweep is the background worker for the HTTP trigger. It owns the
// deadline and delegates to SweepCacheDoctor.
func (h *wordPressHandler) runCacheDoctorSweep(runID, kind, actorUserID string) {
	ctx, cancel := context.WithTimeout(context.Background(), cacheDoctorSweepTimeout)
	defer cancel()

	run, err := h.cfg.CacheDoctorRuns.Get(ctx, runID)
	if err != nil {
		slog.ErrorContext(ctx, "cache-doctor: run vanished before sweep", "run", runID, "err", err)
		return
	}
	_ = SweepCacheDoctor(ctx, h.cfg, run, actorUserID)
}

// SweepCacheDoctor runs a fleet cache-maintenance sweep synchronously against
// cfg, updating the (already-Created) run row as it progresses. Exported so the
// nightly CLI timer (`jabali app cache-doctor-run`) runs the exact same executor
// as the HTTP trigger — no second sweep implementation to drift. The caller owns
// the context deadline.
func SweepCacheDoctor(ctx context.Context, cfg ApplicationHandlerConfig, run *models.CacheDoctorRun, actorUserID string) error {
	h := &wordPressHandler{cfg: cfg}
	kind := run.Kind
	now := time.Now().UTC()
	run.State = models.CacheDoctorStateRunning
	run.StartedAt = &now
	_ = cfg.CacheDoctorRuns.Update(ctx, run)

	installs, _, lErr := cfg.ApplicationInstalls.List(ctx, repository.ListOptions{Limit: 100000})
	if lErr != nil {
		h.failCacheDoctorRun(ctx, run, "list installs: "+lErr.Error())
		return lErr
	}

	items := make([]models.CacheDoctorItem, 0)
	for i := range installs {
		in := installs[i]
		if in.AppType != "wordpress" || !in.CacheEnabled || in.Status != "ready" {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		run.Total++
		item := h.doctorOneInstall(ctx, &installs[i], kind, actorUserID)
		items = append(items, item)

		run.Checked++
		if item.Drifted {
			run.Drifted++
		}
		if item.Repaired {
			run.Repaired++
		}
		if item.Refreshed {
			run.Refreshed++
		}
		if item.Error != "" {
			run.Failed++
			if run.FirstError == "" {
				run.FirstError = truncateDoctorErr(item.InstallID + ": " + item.Error)
			}
		}
		if raw, mErr := json.Marshal(items); mErr == nil {
			run.Results = raw
		}
		_ = cfg.CacheDoctorRuns.Update(ctx, run)
	}

	fin := time.Now().UTC()
	run.FinishedAt = &fin
	run.State = models.CacheDoctorStateDone
	_ = cfg.CacheDoctorRuns.Update(ctx, run)
	return nil
}

// doctorOneInstall health-checks one install and, per kind, repairs drift or
// refreshes the plugin. Never returns an error — every outcome is recorded on
// the item so one bad install doesn't abort the sweep.
func (h *wordPressHandler) doctorOneInstall(ctx context.Context, in *models.ApplicationInstall, kind, actorUserID string) models.CacheDoctorItem {
	item := models.CacheDoctorItem{InstallID: in.ID}

	dom, dErr := h.cfg.Domains.FindByID(ctx, in.DomainID)
	if dErr != nil || dom == nil || dom.DocRoot == "" {
		item.Error = "domain/docroot unresolved"
		return item
	}
	item.Host = dom.Name
	osUser := ""
	if u, uErr := h.cfg.Users.FindByID(ctx, in.UserID); uErr == nil && u != nil && u.Username != nil {
		osUser = *u.Username
	}
	if osUser == "" {
		item.Error = "no linux account for owner"
		return item
	}
	installPath := dom.DocRoot
	if in.Subdirectory != "" {
		installPath = path.Join(dom.DocRoot, in.Subdirectory)
	}
	item.Path = installPath

	// Refresh is independent of health — it updates the plugin regardless.
	if kind == models.CacheDoctorKindRefresh {
		callCtx, cancel := context.WithTimeout(ctx, cacheDoctorInstallCallTimeout)
		_, rErr := h.cfg.Agent.Call(callCtx, "wordpress.cache_plugin_refresh", map[string]any{
			"os_user": osUser, "install_path": installPath,
		})
		cancel()
		if rErr != nil {
			item.Error = rErr.Error()
			return item
		}
		item.Refreshed = true
		return item
	}

	// doctor + repair both start with a health check.
	callCtx, cancel := context.WithTimeout(ctx, cacheDoctorInstallCallTimeout)
	raw, hErr := h.cfg.Agent.Call(callCtx, "wordpress.cache_health", map[string]any{
		"os_user": osUser, "install_path": installPath, "host": dom.Name,
	})
	cancel()
	if hErr != nil {
		item.Error = hErr.Error()
		return item
	}
	var health struct {
		Healthy bool   `json:"healthy"`
		Detail  string `json:"detail"`
	}
	_ = json.Unmarshal(raw, &health)
	item.Healthy = health.Healthy
	item.Drifted = in.CacheEnabled && !health.Healthy
	if !item.Drifted {
		return item
	}
	if health.Detail != "" {
		item.Error = health.Detail
	}

	if kind == models.CacheDoctorKindRepair {
		// Re-run the idempotent enable path — re-stamps constants, re-provisions
		// the ACL, re-verifies. Same call the CLI cache-doctor --repair makes.
		if rErr := SetApplicationCache(ctx, h.cfg, in.ID, true, true, actorUserID); rErr != nil {
			item.Error = "repair failed: " + rErr.Error()
			return item
		}
		item.Repaired = true
		item.Error = "" // repaired — clear the drift detail
	}
	return item
}

func (h *wordPressHandler) failCacheDoctorRun(ctx context.Context, run *models.CacheDoctorRun, msg string) {
	fin := time.Now().UTC()
	run.FinishedAt = &fin
	run.State = models.CacheDoctorStateFailed
	run.FirstError = truncateDoctorErr(msg)
	_ = h.cfg.CacheDoctorRuns.Update(ctx, run)
}

func truncateDoctorErr(s string) string {
	const max = 1024
	if len(s) > max {
		return s[:max]
	}
	return s
}
