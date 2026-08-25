package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/limits"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// usageReportCacheTTL bounds how stale a cached user.limits.report
// payload can be before we re-call the agent. 60s is short enough that
// an operator who just changed a quota sees fresh numbers within a
// minute, long enough that paging back-and-forth through a 20-row
// users list doesn't trigger 20 agent round-trips per page reload.
const usageReportCacheTTL = 60 * time.Second

type cachedUsageReport struct {
	raw     json.RawMessage
	expires time.Time
}

var (
	usageReportCacheMu sync.Mutex
	usageReportCache   = map[string]cachedUsageReport{}
)

func usageReportCacheGet(key string) (json.RawMessage, bool) {
	usageReportCacheMu.Lock()
	defer usageReportCacheMu.Unlock()
	c, ok := usageReportCache[key]
	if !ok || time.Now().After(c.expires) {
		return nil, false
	}
	return c.raw, true
}

func usageReportCachePut(key string, raw json.RawMessage) {
	usageReportCacheMu.Lock()
	defer usageReportCacheMu.Unlock()
	usageReportCache[key] = cachedUsageReport{
		raw:     raw,
		expires: time.Now().Add(usageReportCacheTTL),
	}
}

// UserLimitsHandlerConfig wires the M18 per-user limit endpoints. Both
// repos are required; Agent + QuotaMount are required for the usage
// endpoint (which calls user.limits.report on the agent) but endpoints
// degrade gracefully if the agent isn't reachable — returns the
// effective-limits section only, with `current` set to null.
type UserLimitsHandlerConfig struct {
	Users          repository.UserRepository
	Packages       repository.PackageRepository
	LimitOverrides repository.UserLimitOverrideRepository
	Agent          agent.AgentInterface
	// QuotaMount is the filesystem mount path /home lives on — resolved
	// at serve startup via limits.QuotaMountFor("/home") and passed
	// through to the agent on every user.limits.{apply,clear,report}
	// call. Empty string disables the disk half of the report (CI,
	// dev box without quota).
	QuotaMount string
}

// RegisterUserLimitsRoutes mounts /users/:id/usage and /limit-overrides
// under g. Must be called AFTER RegisterUserRoutes so the owner-check
// middleware is already set up consistently.
func RegisterUserLimitsRoutes(g *gin.RouterGroup, cfg UserLimitsHandlerConfig) {
	h := &userLimitsHandler{cfg: cfg}
	// Usage is a read-only view so owner or admin is fine.
	g.GET("/users/:id/usage", middleware.RequireOwner("id"), h.usage)
	// Measure runs an on-demand `du` walk on the agent (ionice'd, cached
	// 60s agent-side) for hosts without filesystem quotas. Owner-or-admin
	// like the read: it only recomputes the caller's own number. This
	// replaced the automatic du fallback that IO-starved a box at peak.
	g.POST("/users/:id/usage/measure-disk", middleware.RequireOwner("id"), h.measureDisk)
	// Override writes are admin-only — changing a user's resource limits
	// outside their package is operator territory, not self-service.
	g.PUT("/users/:id/limit-overrides", middleware.RequireAdmin(), h.upsertOverride)
	g.DELETE("/users/:id/limit-overrides", middleware.RequireAdmin(), h.clearOverride)
}

type userLimitsHandler struct{ cfg UserLimitsHandlerConfig }

// usageResponse is the shape returned by GET /users/:id/usage. `effective`
// is the resolved limits (never null). `current` may be nil if the agent
// is unavailable or the slice has no live processes yet.
// limitFieldsView is the flat six-field limit shape in snake_case, used for
// the resolved package limits so the admin Limits card can show "inherited
// from package" values distinctly from the per-user override and the final
// effective bundle. Zero means unlimited (same convention as EffectiveLimits).
type limitFieldsView struct {
	DiskQuotaMB     uint32 `json:"disk_quota_mb"`
	CPUQuotaPercent uint32 `json:"cpu_quota_percent"`
	MemoryLimitMB   uint32 `json:"memory_limit_mb"`
	IOReadMbps      uint32 `json:"io_read_mbps"`
	IOWriteMbps     uint32 `json:"io_write_mbps"`
	MaxTasks        uint32 `json:"max_tasks"`
}

type usageResponse struct {
	UserID    string                 `json:"user_id"`
	Effective limits.EffectiveLimits `json:"effective"`
	// Package is the resolved package-level limits (nil when the user has no
	// package). Override is the raw per-user override row (nil when none) —
	// its per-field omitempty distinguishes NULL=inherit from &0=explicit
	// unlimited, which the GUI needs to label each field's source.
	Package  *limitFieldsView          `json:"package,omitempty"`
	Override *models.UserLimitOverride `json:"override,omitempty"`
	Current  json.RawMessage           `json:"current,omitempty"`
}

func (h *userLimitsHandler) usage(c *gin.Context) {
	userID := c.Param("id")
	user, err := h.cfg.Users.FindByID(c.Request.Context(), userID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if user.Username == nil || *user.Username == "" {
		c.JSON(http.StatusConflict, gin.H{"error": "no_linux_account", "detail": "user has no linux account"})
		return
	}

	effective, pkgView, override := h.resolveEffective(c.Request.Context(), user)

	resp := usageResponse{
		UserID:    userID,
		Effective: effective,
		Package:   pkgView,
		Override:  override,
	}

	// Call the agent for live usage. Failure is non-fatal — we want
	// the admin UI to still render the effective-limits section even
	// if the agent socket is temporarily down. The current section
	// is just omitted.
	//
	// 60s in-memory cache (keyed by username + quota mount) keeps the
	// admin Users page fast: a 20-row table reload only fans out for
	// rows whose cache has expired. quotacheck is minutes-stale at
	// the kernel level anyway, so 60s is well inside the noise floor.
	if h.cfg.Agent != nil {
		cacheKey := *user.Username + "|" + h.cfg.QuotaMount
		if cached, ok := usageReportCacheGet(cacheKey); ok {
			resp.Current = cached
		} else {
			ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
			defer cancel()
			raw, err := h.cfg.Agent.Call(ctx, "user.limits.report", map[string]any{
				"username":    *user.Username,
				"quota_mount": h.cfg.QuotaMount,
			})
			if err == nil {
				resp.Current = raw
				usageReportCachePut(cacheKey, raw)
			}
		}
	}
	c.JSON(http.StatusOK, resp)
}

// measureDisk asks the agent for a usage report WITH the on-demand disk
// walk (measure_disk) and refreshes the cache with the result, so the next
// GET /usage renders the measured number immediately. Longer timeout than
// the read path: an ionice'd walk of a large home takes real seconds.
func (h *userLimitsHandler) measureDisk(c *gin.Context) {
	userID := c.Param("id")
	user, err := h.cfg.Users.FindByID(c.Request.Context(), userID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if user.Username == nil || *user.Username == "" {
		c.JSON(http.StatusConflict, gin.H{"error": "no_linux_account", "detail": "user has no linux account"})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 120*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(ctx, "user.limits.report", map[string]any{
		"username":     *user.Username,
		"quota_mount":  h.cfg.QuotaMount,
		"measure_disk": true,
	})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "agent_failed"})
		return
	}
	usageReportCachePut(*user.Username+"|"+h.cfg.QuotaMount, raw)
	c.JSON(http.StatusOK, gin.H{"current": json.RawMessage(raw)})
}

// resolveEffective hydrates the user's package + any override row and
// passes both through the pure resolver. Returns the zero-value
// EffectiveLimits on lookup errors — the caller falls back to
// "unlimited everywhere" which is the safe default.
func (h *userLimitsHandler) resolveEffective(ctx context.Context, user *models.User) (limits.EffectiveLimits, *limitFieldsView, *models.UserLimitOverride) {
	var pkgL *limits.PackageLimits
	var pkgView *limitFieldsView
	if user.PackageID != nil && *user.PackageID != "" {
		pkg, err := h.cfg.Packages.FindByID(ctx, *user.PackageID)
		if err == nil {
			pkgL = &limits.PackageLimits{
				DiskQuotaMB:     pkg.DiskQuotaMB,
				CPUQuotaPercent: pkg.CPUQuotaPercent,
				MemoryLimitMB:   pkg.MemoryLimitMB,
				IOReadMbps:      pkg.IOReadMbps,
				IOWriteMbps:     pkg.IOWriteMbps,
				MaxTasks:        pkg.MaxTasks,
			}
			pkgView = &limitFieldsView{
				DiskQuotaMB:     pkg.DiskQuotaMB,
				CPUQuotaPercent: pkg.CPUQuotaPercent,
				MemoryLimitMB:   pkg.MemoryLimitMB,
				IOReadMbps:      pkg.IOReadMbps,
				IOWriteMbps:     pkg.IOWriteMbps,
				MaxTasks:        pkg.MaxTasks,
			}
		}
	}

	var ovL *limits.OverrideLimits
	var ovRow *models.UserLimitOverride
	if ov, err := h.cfg.LimitOverrides.FindByUserID(ctx, user.ID); err == nil && ov != nil {
		ovRow = ov
		ovL = &limits.OverrideLimits{
			DiskQuotaMB:     ov.DiskQuotaMB,
			CPUQuotaPercent: ov.CPUQuotaPercent,
			MemoryLimitMB:   ov.MemoryLimitMB,
			IOReadMbps:      ov.IOReadMbps,
			IOWriteMbps:     ov.IOWriteMbps,
			MaxTasks:        ov.MaxTasks,
		}
	}
	return limits.Resolve(pkgL, ovL), pkgView, ovRow
}

type overrideRequest struct {
	DiskQuotaMB     *uint32 `json:"disk_quota_mb"`
	CPUQuotaPercent *uint32 `json:"cpu_quota_percent"`
	MemoryLimitMB   *uint32 `json:"memory_limit_mb"`
	IOReadMbps      *uint32 `json:"io_read_mbps"`
	IOWriteMbps     *uint32 `json:"io_write_mbps"`
	MaxTasks        *uint32 `json:"max_tasks"`
}

func (h *userLimitsHandler) upsertOverride(c *gin.Context) {
	userID := c.Param("id")
	// Confirm the user exists — don't want orphan override rows.
	if _, err := h.cfg.Users.FindByID(c.Request.Context(), userID); err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	var req overrideRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "validation_failed", "detail": err.Error()})
		return
	}

	// Validate bounds: a non-nil override value must fit within the
	// limits pkg's bounds. Zero and nil both pass.
	if err := validateOverrideBounds(req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "validation_failed", "detail": err.Error()})
		return
	}

	o := &models.UserLimitOverride{
		UserID:          userID,
		DiskQuotaMB:     req.DiskQuotaMB,
		CPUQuotaPercent: req.CPUQuotaPercent,
		MemoryLimitMB:   req.MemoryLimitMB,
		IOReadMbps:      req.IOReadMbps,
		IOWriteMbps:     req.IOWriteMbps,
		MaxTasks:        req.MaxTasks,
		UpdatedAt:       time.Now().UTC(),
	}
	if err := h.cfg.LimitOverrides.Upsert(c.Request.Context(), o); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, o)
}

// validateOverrideBounds runs each present override value through the
// same bounds check the package fields use. Field-by-field rather than
// the bundle-style Validate() on EffectiveLimits because an override
// can have any combination of NULL values.
func validateOverrideBounds(req overrideRequest) error {
	// Shared with the operator CLI (JAB-309) so both enforce the identical
	// bounds; each present value is validated as an EffectiveLimits field-of-one.
	return limits.ValidateOverrideBounds(
		req.CPUQuotaPercent, req.MemoryLimitMB, req.IOReadMbps, req.IOWriteMbps, req.MaxTasks)
}

func (h *userLimitsHandler) clearOverride(c *gin.Context) {
	userID := c.Param("id")
	if err := h.cfg.LimitOverrides.Delete(c.Request.Context(), userID); err != nil {
		if isNotFound(err) {
			// Idempotent: no override = 204, same as successful delete.
			c.Status(http.StatusNoContent)
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.Status(http.StatusNoContent)
}
