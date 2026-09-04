package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/phppoolops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// PHPPoolHandlerConfig plugs the PHP pool handlers into the router.
// Resource caps (GH #339 security): pm_max_children + idle timeout are
// tenant-settable, so they MUST be bounded — an unbounded value is a
// resource-exhaustion DoS on the shared host.
// Resource caps + the FPM tuning validators now live in internal/phppoolops so
// the HTTP handlers and the operator CLI share one authority (JAB-360). These
// package-local aliases keep the existing api call sites (packages.go,
// php_performance_modes.go, php_pool_user_tuning.go, the update handler)
// unchanged.
const (
	phpPoolUserMaxChildrenCap  = phppoolops.UserMaxChildrenCap
	phpPoolAdminMaxChildrenCap = phppoolops.AdminMaxChildrenCap
	phpPoolMaxIdleTimeoutSec   = phppoolops.MaxIdleTimeoutSec
	phpPoolMaxRequestsCap      = phppoolops.MaxRequestsCap
	phpPoolMaxTerminateSec     = phppoolops.MaxTerminateSec
)

func clampToPackageCap(cap uint32, mode string, maxChildren, start, minSpare, maxSpare, maxReq, terminate *uint32) (string, bool) {
	return phppoolops.ClampToPackageCap(cap, mode, maxChildren, start, minSpare, maxSpare, maxReq, terminate)
}

func resolvePMTuning(mode string, maxChildren uint32, start, minSpare, maxSpare, maxReq, terminate *uint32) (string, bool) {
	return phppoolops.ResolvePMTuning(mode, maxChildren, start, minSpare, maxSpare, maxReq, terminate)
}

type PHPPoolHandlerConfig struct {
	PHPPools            repository.PHPPoolRepository
	PHPPoolIniOverrides repository.PHPPoolIniOverrideRepository
	Domains             repository.DomainRepository
	Users               repository.UserRepository
	Packages            repository.PackageRepository
	Agent               agent.AgentInterface
}

const (
	defaultPHPPoolPageSize = 20
	maxPHPPoolPageSize     = 200
)

// RegisterPHPPoolRoutes mounts /php-pools* under g.
// - GET /php-pools (admin: all; user: scoped to self)
// - GET /php-pools/:id (admin: all; user: scoped to self)
// - POST /php-pools (admin: all; user: own only)
// - PUT + PATCH /php-pools/:id (admin: all; user: scoped to self)
// - DELETE /php-pools/:id (admin: all; user: scoped to self)
// - GET /php-pools/:id/ini-overrides (admin: all; user: scoped to self)
// - POST /php-pools/:id/ini-overrides (admin: all; user: scoped to self)
// - PUT /php-pools/:id/ini-overrides/:override_id (admin: all; user: scoped to self)
// - DELETE /php-pools/:id/ini-overrides/:override_id (admin: all; user: scoped to self)
func RegisterPHPPoolRoutes(g *gin.RouterGroup, cfg PHPPoolHandlerConfig) {
	h := &phpPoolHandler{cfg: cfg}

	pools := g.Group("/php-pools")
	pools.GET("", h.list)
	pools.GET("/:id", h.get)
	pools.POST("", h.create)
	pools.PUT("/:id", h.update)
	// GH #339: the SPA's generic useUpdateMutation issues PATCH; php-pools only
	// registered PUT, so editing a pool 405'd. Accept both.
	pools.PATCH("/:id", h.update)
	pools.DELETE("/:id", h.delete)

	// INI overrides are nested under pools
	pools.GET("/:id/ini-overrides", h.listIniOverrides)
	pools.POST("/:id/ini-overrides", h.createIniOverride)
	pools.PUT("/:id/ini-overrides/:override_id", h.updateIniOverride)
	pools.DELETE("/:id/ini-overrides/:override_id", h.deleteIniOverride)
}

type phpPoolHandler struct{ cfg PHPPoolHandlerConfig }

// ---- Requests/Responses ----

type createPHPPoolRequest struct {
	UserID                         string `json:"user_id"`
	PHPVersion                     string `json:"php_version"`
	PmMode                         string `json:"pm_mode"`
	PmMaxChildren                  uint32 `json:"pm_max_children"`
	ProcessIdleTimeoutSeconds      uint32 `json:"process_idle_timeout_seconds"`
	PmStartServers                 uint32 `json:"pm_start_servers"`
	PmMinSpareServers              uint32 `json:"pm_min_spare_servers"`
	PmMaxSpareServers              uint32 `json:"pm_max_spare_servers"`
	PmMaxRequests                  uint32 `json:"pm_max_requests"`
	RequestTerminateTimeoutSeconds uint32 `json:"request_terminate_timeout_seconds"`
}

type updatePHPPoolRequest struct {
	PmMode                         string `json:"pm_mode"`
	PmMaxChildren                  uint32 `json:"pm_max_children"`
	ProcessIdleTimeoutSeconds      uint32 `json:"process_idle_timeout_seconds"`
	PmStartServers                 uint32 `json:"pm_start_servers"`
	PmMinSpareServers              uint32 `json:"pm_min_spare_servers"`
	PmMaxSpareServers              uint32 `json:"pm_max_spare_servers"`
	PmMaxRequests                  uint32 `json:"pm_max_requests"`
	RequestTerminateTimeoutSeconds uint32 `json:"request_terminate_timeout_seconds"`
}

type createPHPPoolIniOverrideRequest struct {
	Directive string `json:"directive"`
	Value     string `json:"value"`
	Kind      string `json:"kind"` // "value" or "flag"
}

type updatePHPPoolIniOverrideRequest struct {
	Value string `json:"value"`
}

// ---- List ----

// list returns all PHP pools for the authenticated user (or all if admin).
// Supports pagination and filtering.
func (h *phpPoolHandler) list(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	page, pageSize, opts := parseListOptions(c, defaultPHPPoolPageSize, maxPHPPoolPageSize)
	ctx := c.Request.Context()

	var pools []models.PHPPool
	var total int64
	var err error

	if claims.IsAdmin {
		// Admins see all pools
		pools, total, err = h.cfg.PHPPools.ListAll(ctx, opts)
	} else {
		// Users see only their own pools
		pool, err := h.cfg.PHPPools.FindByUserID(ctx, claims.UserID)
		if err != nil && !isNotFound(err) {
			slog.ErrorContext(ctx, "failed to find PHP pool", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		if pool != nil {
			pools = []models.PHPPool{*pool}
			total = 1
		} else {
			pools = []models.PHPPool{}
			total = 0
		}
	}

	if err != nil {
		slog.ErrorContext(ctx, "failed to list PHP pools", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data": pools,
		"meta": gin.H{
			"total":  total,
			"page":   page,
			"limit":  pageSize,
			"offset": opts.Offset,
		},
	})
}

// ---- Get ----

// get returns a single PHP pool by ID.
func (h *phpPoolHandler) get(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	poolID := c.Param("id")
	ctx := c.Request.Context()

	pool, err := h.cfg.PHPPools.FindByID(ctx, poolID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "pool_not_found"})
			return
		}
		slog.ErrorContext(ctx, "failed to find PHP pool", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Check authorization: user can only see their own pools
	if !claims.IsAdmin && pool.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	c.JSON(http.StatusOK, pool)
}

// ---- Create ----

// create creates a new PHP pool for a user.
// The pool is created with status="pending" and must be reconciled by the agent.
func (h *phpPoolHandler) create(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	var req createPHPPoolRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}

	ctx := c.Request.Context()

	// Determine target user ID
	targetUserID := req.UserID
	if !claims.IsAdmin {
		// Non-admin users can only create pools for themselves
		targetUserID = claims.UserID
	}

	// If target user is not the requestor, must be admin
	if targetUserID != claims.UserID && !claims.IsAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	// Validate target user exists
	_, err := h.cfg.Users.FindByID(ctx, targetUserID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "user_not_found"})
			return
		}
		slog.ErrorContext(ctx, "failed to find user", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Validate PHP version format: X.Y (handled by agent validation)
	if req.PHPVersion == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "invalid_php_version",
			"detail": "php_version is required",
		})
		return
	}

	// JAB-360: one shared create-side validator (mode + defaults + admin/user
	// pm_max_children cap + idle ceiling + FPM dynamic constraint) so the
	// operator CLI create can't drift from this handler. The returned field key
	// maps 1:1 onto the error codes this endpoint has always returned.
	tuning, msg, field, ok := phppoolops.ResolveCreateTuning(claims.IsAdmin, phppoolops.CreateTuning{
		PmMode:                         req.PmMode,
		PmMaxChildren:                  req.PmMaxChildren,
		ProcessIdleTimeoutSeconds:      req.ProcessIdleTimeoutSeconds,
		PmStartServers:                 req.PmStartServers,
		PmMinSpareServers:              req.PmMinSpareServers,
		PmMaxSpareServers:              req.PmMaxSpareServers,
		PmMaxRequests:                  req.PmMaxRequests,
		RequestTerminateTimeoutSeconds: req.RequestTerminateTimeoutSeconds,
	})
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": field, "detail": msg})
		return
	}
	req.PmMode = tuning.PmMode
	req.PmMaxChildren = tuning.PmMaxChildren
	req.ProcessIdleTimeoutSeconds = tuning.ProcessIdleTimeoutSeconds
	req.PmStartServers = tuning.PmStartServers
	req.PmMinSpareServers = tuning.PmMinSpareServers
	req.PmMaxSpareServers = tuning.PmMaxSpareServers
	req.PmMaxRequests = tuning.PmMaxRequests
	req.RequestTerminateTimeoutSeconds = tuning.RequestTerminateTimeoutSeconds

	// Check if user already has a pool (MVP constraint: one pool per user)
	existingPool, err := h.cfg.PHPPools.FindByUserID(ctx, targetUserID)
	if err == nil && existingPool != nil {
		c.JSON(http.StatusConflict, gin.H{
			"error":  "pool_already_exists",
			"detail": "user already has a PHP pool assigned (MVP constraint)",
		})
		return
	}
	if err != nil && !isNotFound(err) {
		slog.ErrorContext(ctx, "failed to check existing pool", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Create pool record with status="pending"
	now := time.Now().UTC()
	pool := &models.PHPPool{
		ID:                             ids.NewULID(),
		UserID:                         targetUserID,
		PHPVersion:                     req.PHPVersion,
		PmMode:                         req.PmMode,
		PmMaxChildren:                  req.PmMaxChildren,
		ProcessIdleTimeoutSeconds:      req.ProcessIdleTimeoutSeconds,
		PmStartServers:                 req.PmStartServers,
		PmMinSpareServers:              req.PmMinSpareServers,
		PmMaxSpareServers:              req.PmMaxSpareServers,
		PmMaxRequests:                  req.PmMaxRequests,
		RequestTerminateTimeoutSeconds: req.RequestTerminateTimeoutSeconds,
		Status:                         "pending",
		CreatedAt:                      now,
		UpdatedAt:                      now,
	}

	if err := h.cfg.PHPPools.Create(ctx, pool); err != nil {
		if isConflict(err) {
			c.JSON(http.StatusConflict, gin.H{"error": "pool_already_exists"})
			return
		}
		slog.ErrorContext(ctx, "failed to create PHP pool", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	slog.InfoContext(ctx, "php_pool.created", "user_id", pool.UserID, "pool_id", pool.ID, "php_version", pool.PHPVersion, "pm_mode", pool.PmMode)

	// Trigger agent to reconcile the pool asynchronously (fire-and-forget)
	go h.reconcilePoolAsync(*pool)

	c.JSON(http.StatusCreated, pool)
}

// ---- Update ----

// update updates a PHP pool's configuration.
// Changes to settings trigger a re-reconciliation of the pool.
func (h *phpPoolHandler) update(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	poolID := c.Param("id")
	ctx := c.Request.Context()

	var req updatePHPPoolRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}

	// Load existing pool
	pool, err := h.cfg.PHPPools.FindByID(ctx, poolID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "pool_not_found"})
			return
		}
		slog.ErrorContext(ctx, "failed to find PHP pool", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Check authorization: user can only update their own pools
	if !claims.IsAdmin && pool.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	// Capture old values for audit logging
	oldPmMode := pool.PmMode
	oldPmMaxChildren := pool.PmMaxChildren

	// Validate pm_mode
	if req.PmMode != "" {
		validPmModes := map[string]bool{"static": true, "ondemand": true, "dynamic": true}
		if !validPmModes[req.PmMode] {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":  "invalid_pm_mode",
				"detail": "pm_mode must be static, ondemand, or dynamic",
			})
			return
		}
		pool.PmMode = req.PmMode
	}

	// Validate pm_max_children. SECURITY: an unbounded value is a resource-
	// exhaustion vector — a tenant could set 100000 and OOM the shared host,
	// taking down every other tenant. Cap it; tenants get a low ceiling, admins
	// a higher (still bounded) one to avoid a typo bricking the box.
	if req.PmMaxChildren > 0 {
		maxChildren := uint32(phpPoolUserMaxChildrenCap)
		if claims.IsAdmin {
			maxChildren = phpPoolAdminMaxChildrenCap
		}
		if req.PmMaxChildren > maxChildren {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":  "pm_max_children_too_high",
				"detail": fmt.Sprintf("pm_max_children must be <= %d", maxChildren),
			})
			return
		}
		pool.PmMaxChildren = req.PmMaxChildren
	}

	// Validate process_idle_timeout_seconds (cap so a worker can't idle forever).
	if req.ProcessIdleTimeoutSeconds > 0 {
		if req.ProcessIdleTimeoutSeconds > phpPoolMaxIdleTimeoutSec {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":  "process_idle_timeout_too_high",
				"detail": fmt.Sprintf("process_idle_timeout_seconds must be <= %d", phpPoolMaxIdleTimeoutSec),
			})
			return
		}
		pool.ProcessIdleTimeoutSeconds = req.ProcessIdleTimeoutSeconds
	}

	// GH #339: extended pm.* tuning. 0 = "not provided" (same convention as
	// the fields above), so setting a field back to 0 isn't expressible on
	// update — mirrors pm_max_children. Apply provided values, then re-validate
	// the resulting pool (defaults + caps + FPM dynamic constraint).
	if req.PmStartServers > 0 {
		pool.PmStartServers = req.PmStartServers
	}
	if req.PmMinSpareServers > 0 {
		pool.PmMinSpareServers = req.PmMinSpareServers
	}
	if req.PmMaxSpareServers > 0 {
		pool.PmMaxSpareServers = req.PmMaxSpareServers
	}
	if req.PmMaxRequests > 0 {
		pool.PmMaxRequests = req.PmMaxRequests
	}
	if req.RequestTerminateTimeoutSeconds > 0 {
		pool.RequestTerminateTimeoutSeconds = req.RequestTerminateTimeoutSeconds
	}
	if msg, ok := resolvePMTuning(pool.PmMode, pool.PmMaxChildren,
		&pool.PmStartServers, &pool.PmMinSpareServers, &pool.PmMaxSpareServers,
		&pool.PmMaxRequests, &pool.RequestTerminateTimeoutSeconds); !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "pm_tuning_invalid", "detail": msg})
		return
	}

	pool.UpdatedAt = time.Now().UTC()
	pool.Status = "pending" // Mark for re-reconciliation

	if err := h.cfg.PHPPools.Update(ctx, pool); err != nil {
		slog.ErrorContext(ctx, "failed to update PHP pool", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	slog.InfoContext(ctx, "php_pool.updated", "user_id", pool.UserID, "pool_id", pool.ID, "old_pm_mode", oldPmMode, "new_pm_mode", pool.PmMode, "old_pm_max_children", oldPmMaxChildren, "new_pm_max_children", pool.PmMaxChildren)

	// Trigger agent to re-reconcile the pool asynchronously (fire-and-forget)
	go h.reconcilePoolAsync(*pool)

	c.JSON(http.StatusOK, pool)
}

// ---- Delete ----

// delete removes a PHP pool and all associated ini overrides.
func (h *phpPoolHandler) delete(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	poolID := c.Param("id")
	ctx := c.Request.Context()

	pool, err := h.cfg.PHPPools.FindByID(ctx, poolID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "pool_not_found"})
			return
		}
		slog.ErrorContext(ctx, "failed to find PHP pool", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Pool deletion is admin-only per ADR-0023 decision 10. Regular users
	// cannot delete their own pool — the reconciler ensures every user
	// has one. To "disable PHP" for a domain, the user unbinds the pool
	// from the domain (POST /domains/:id/php-pool DELETE), not by deleting
	// the pool itself.
	if !claims.IsAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "admin_required"})
		return
	}

	// Refuse deletion while any domain still references this pool. Belt
	// and suspenders alongside the FK's ON DELETE SET NULL — the 409
	// keeps the admin explicit about what they're about to unbind.
	if h.cfg.Domains != nil {
		bound, cerr := h.cfg.Domains.CountByPHPPoolID(ctx, poolID)
		if cerr != nil {
			slog.ErrorContext(ctx, "failed to count domains bound to pool", "error", cerr)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		if bound > 0 {
			c.JSON(http.StatusConflict, gin.H{
				"error":         "pool_has_bound_domains",
				"bound_domains": bound,
				"message":       "unbind all domains from this pool before deleting",
			})
			return
		}
	}

	// Delete all ini overrides first
	overrides, err := h.cfg.PHPPoolIniOverrides.ListByPool(ctx, poolID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list ini overrides", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	for _, override := range overrides {
		if err := h.cfg.PHPPoolIniOverrides.Delete(ctx, override.ID); err != nil {
			slog.ErrorContext(ctx, "failed to delete ini override", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
	}

	// Delete the pool record
	if err := h.cfg.PHPPools.Delete(ctx, poolID); err != nil {
		slog.ErrorContext(ctx, "failed to delete PHP pool", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	slog.InfoContext(ctx, "php_pool.deleted", "user_id", pool.UserID, "pool_id", pool.ID, "php_version", pool.PHPVersion)

	// Trigger agent to remove the pool asynchronously (fire-and-forget)
	go func() {
		agentCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		// Load user details
		user, err := h.cfg.Users.FindByID(agentCtx, pool.UserID)
		if err != nil {
			slog.ErrorContext(agentCtx, "failed to find user for removal", "error", err)
			return
		}

		// Call agent to remove the pool
		_, err = h.cfg.Agent.Call(agentCtx, "php.pool.remove", map[string]any{
			"username": user.Username,
		})
		if err != nil {
			slog.ErrorContext(agentCtx, "agent failed to remove pool", "error", err)
		}
	}()

	c.JSON(http.StatusNoContent, nil)
}

// ---- INI Overrides: List ----

// listIniOverrides returns all ini overrides for a PHP pool.
func (h *phpPoolHandler) listIniOverrides(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	poolID := c.Param("id")
	ctx := c.Request.Context()

	// Check authorization: verify pool ownership
	pool, err := h.cfg.PHPPools.FindByID(ctx, poolID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "pool_not_found"})
			return
		}
		slog.ErrorContext(ctx, "failed to find PHP pool", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	if !claims.IsAdmin && pool.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	page, pageSize, opts := parseListOptions(c, defaultPHPPoolPageSize, maxPHPPoolPageSize)
	overrides, err := h.cfg.PHPPoolIniOverrides.ListByPool(ctx, poolID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list ini overrides", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	total := int64(len(overrides))
	c.JSON(http.StatusOK, gin.H{
		"data": overrides,
		"meta": gin.H{
			"total":  total,
			"page":   page,
			"limit":  pageSize,
			"offset": opts.Offset,
		},
	})
}

// ---- INI Overrides: Create ----

// createIniOverride creates a new ini override for a PHP pool.
func (h *phpPoolHandler) createIniOverride(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	poolID := c.Param("id")
	ctx := c.Request.Context()

	var req createPHPPoolIniOverrideRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}

	// Check authorization: verify pool ownership
	pool, err := h.cfg.PHPPools.FindByID(ctx, poolID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "pool_not_found"})
			return
		}
		slog.ErrorContext(ctx, "failed to find PHP pool", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	if !claims.IsAdmin && pool.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	// Validate directive and kind
	if req.Directive == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "invalid_directive",
			"detail": "directive is required",
		})
		return
	}

	if req.Kind == "" {
		req.Kind = "value" // default
	}

	validKinds := map[string]bool{"value": true, "flag": true}
	if !validKinds[req.Kind] {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "invalid_kind",
			"detail": "kind must be 'value' or 'flag'",
		})
		return
	}

	// Create ini override record
	now := time.Now().UTC()
	override := &models.PHPPoolIniOverride{
		ID:        ids.NewULID(),
		PoolID:    poolID,
		Directive: req.Directive,
		Value:     req.Value,
		Kind:      req.Kind,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := h.cfg.PHPPoolIniOverrides.Create(ctx, override); err != nil {
		slog.ErrorContext(ctx, "failed to create ini override", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	slog.InfoContext(ctx, "php_pool_ini_override.created", "user_id", pool.UserID, "pool_id", pool.ID, "override_id", override.ID, "directive", override.Directive, "value", override.Value)

	// Mark pool for re-reconciliation
	pool.Status = "pending"
	pool.UpdatedAt = time.Now().UTC()
	_ = h.cfg.PHPPools.Update(ctx, pool)

	// Trigger agent to re-reconcile the pool
	go h.reconcilePoolAsync(*pool)

	c.JSON(http.StatusCreated, override)
}

// ---- INI Overrides: Update ----

// updateIniOverride updates an ini override value.
func (h *phpPoolHandler) updateIniOverride(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	poolID := c.Param("id")
	overrideID := c.Param("override_id")
	ctx := c.Request.Context()

	var req updatePHPPoolIniOverrideRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "detail": err.Error()})
		return
	}

	// Check authorization: verify pool ownership
	pool, err := h.cfg.PHPPools.FindByID(ctx, poolID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "pool_not_found"})
			return
		}
		slog.ErrorContext(ctx, "failed to find PHP pool", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	if !claims.IsAdmin && pool.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	// Find the override
	override, err := h.cfg.PHPPoolIniOverrides.FindByID(ctx, overrideID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "override_not_found"})
			return
		}
		slog.ErrorContext(ctx, "failed to find ini override", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Verify override belongs to this pool
	if override.PoolID != poolID {
		c.JSON(http.StatusNotFound, gin.H{"error": "override_not_found"})
		return
	}

	// Capture old value for audit logging
	oldValue := override.Value

	// Update the value
	override.Value = req.Value
	override.UpdatedAt = time.Now().UTC()

	if err := h.cfg.PHPPoolIniOverrides.Update(ctx, override); err != nil {
		slog.ErrorContext(ctx, "failed to update ini override", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	slog.InfoContext(ctx, "php_pool_ini_override.updated", "user_id", pool.UserID, "pool_id", pool.ID, "override_id", override.ID, "directive", override.Directive, "old_value", oldValue, "new_value", override.Value)

	// Mark pool for re-reconciliation
	pool.Status = "pending"
	pool.UpdatedAt = time.Now().UTC()
	_ = h.cfg.PHPPools.Update(ctx, pool)

	// Trigger agent to re-reconcile the pool
	go h.reconcilePoolAsync(*pool)

	c.JSON(http.StatusOK, override)
}

// ---- INI Overrides: Delete ----

// deleteIniOverride removes an ini override from a PHP pool.
func (h *phpPoolHandler) deleteIniOverride(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	poolID := c.Param("id")
	overrideID := c.Param("override_id")
	ctx := c.Request.Context()

	// Check authorization: verify pool ownership
	pool, err := h.cfg.PHPPools.FindByID(ctx, poolID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "pool_not_found"})
			return
		}
		slog.ErrorContext(ctx, "failed to find PHP pool", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	if !claims.IsAdmin && pool.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	// Find the override to verify pool ownership
	override, err := h.cfg.PHPPoolIniOverrides.FindByID(ctx, overrideID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "override_not_found"})
			return
		}
		slog.ErrorContext(ctx, "failed to find ini override", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	if override.PoolID != poolID {
		c.JSON(http.StatusNotFound, gin.H{"error": "override_not_found"})
		return
	}

	// Delete the override
	if err := h.cfg.PHPPoolIniOverrides.Delete(ctx, overrideID); err != nil {
		slog.ErrorContext(ctx, "failed to delete ini override", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	slog.InfoContext(ctx, "php_pool_ini_override.deleted", "user_id", pool.UserID, "pool_id", pool.ID, "override_id", override.ID, "directive", override.Directive)

	// Mark pool for re-reconciliation
	pool.Status = "pending"
	pool.UpdatedAt = time.Now().UTC()
	_ = h.cfg.PHPPools.Update(ctx, pool)

	// Trigger agent to re-reconcile the pool
	go h.reconcilePoolAsync(*pool)

	c.JSON(http.StatusNoContent, nil)
}

// ---- Helpers ----

// reconcilePoolAsync triggers a pool reconciliation in the background.
// Implementation lives in php_pool_reconcile.go so the user-driven
// /domains/:id/php-pool path can share it without depending on phpPoolHandler.
func (h *phpPoolHandler) reconcilePoolAsync(pool models.PHPPool) {
	reconcilePHPPoolViaAgent(h.cfg.Agent, h.cfg.Users, h.cfg.PHPPoolIniOverrides, h.cfg.PHPPools, pool)
}
