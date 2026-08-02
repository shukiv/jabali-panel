// Package api — admin Mail outbound-throttle CRUD (M47 Wave 3).
//
// Admin-only. Writes to mail_outbound_policy; the reconciler converges
// each row into Stalwart's MtaOutboundThrottle on the next tick.
//
// Endpoints:
//
//	GET    /admin/mail/throttles            — list all rows
//	POST   /admin/mail/throttles            — create new row
//	PUT    /admin/mail/throttles/:id        — update existing
//	DELETE /admin/mail/throttles/:id        — remove (reconciler also
//	                                          unwinds the Stalwart side
//	                                          on the next tick because
//	                                          Enabled==false drives the
//	                                          delete branch — but the
//	                                          row is GONE here, so we
//	                                          dispatch the delete inline)
package api

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// scopeRefDomainRe + scopeRefEmailRe pre-empt Stalwart Expression
// injection. The reconciler builds expressions like
// `sender_domain == '<scope_ref>'` and embeds scope_ref verbatim.
// Without these guards an admin could insert a value with a single
// quote and turn the throttle's match into always-fire (or
// always-skip), silently breaking the cap.
var (
	scopeRefDomainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)
	scopeRefEmailRe  = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)
)

func validateScopeRef(scope, ref string) bool {
	switch scope {
	case models.OutboundScopeUser:
		return scopeRefEmailRe.MatchString(ref) && !strings.ContainsAny(ref, "'\\")
	case models.OutboundScopeDomain:
		return scopeRefDomainRe.MatchString(ref) && !strings.ContainsAny(ref, "'\\")
	}
	return false
}

// AdminMailThrottlesHandlerConfig — single dep.
type AdminMailThrottlesHandlerConfig struct {
	Policies repository.MailOutboundPolicyRepository
	// ThrottleClient is the inline delete dispatch path. When nil,
	// DELETE only removes the DB row; the reconciler still cleans up
	// the Stalwart side on the next tick (the row is gone so the
	// !enabled && stalwart_id!="" branch fires).
	ThrottleClient ThrottleDispatcher
}

// ThrottleDispatcher is the narrow subset of *stalwartadmin.Client the
// inline delete path uses. Mirrors reconciler.ThrottleStalwartClient
// (deliberately duplicated — the reconciler-side and handler-side
// usage shouldn't share an interface across packages, the wire is
// the same but the responsibilities differ).
type ThrottleDispatcher interface {
	Delete(ctx context.Context, typeName, id string) error
}

func RegisterAdminMailThrottlesRoutes(g *gin.RouterGroup, cfg AdminMailThrottlesHandlerConfig) {
	if cfg.Policies == nil {
		return
	}
	h := &adminMailThrottlesHandler{cfg: cfg}
	grp := g.Group("/admin/mail/throttles")
	grp.Use(middleware.RequireAdmin())
	grp.GET("", h.list)
	grp.POST("", h.create)
	grp.PUT("/:id", h.update)
	grp.DELETE("/:id", h.del)
}

type adminMailThrottlesHandler struct {
	cfg AdminMailThrottlesHandlerConfig
}

type throttleRequest struct {
	Scope      string  `json:"scope" binding:"required"` // user|domain|global
	ScopeRef   *string `json:"scope_ref"`                // nil for global
	MaxPerHour uint    `json:"max_per_hour"`
	MaxPerDay  uint    `json:"max_per_day"`
	Enabled    *bool   `json:"enabled"` // pointer so default-true on POST without one
}

func validScope(scope string) bool {
	switch scope {
	case models.OutboundScopeUser, models.OutboundScopeDomain, models.OutboundScopeGlobal:
		return true
	}
	return false
}

func (h *adminMailThrottlesHandler) list(c *gin.Context) {
	rows, err := h.cfg.Policies.List(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list_failed", "details": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": rows})
}

func (h *adminMailThrottlesHandler) create(c *gin.Context) {
	var req throttleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "details": err.Error()})
		return
	}
	if !validScope(req.Scope) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_scope"})
		return
	}
	if req.Scope == models.OutboundScopeGlobal {
		req.ScopeRef = nil
	} else {
		if req.ScopeRef == nil || *req.ScopeRef == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "scope_ref required for non-global scope"})
			return
		}
		if !validateScopeRef(req.Scope, *req.ScopeRef) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "scope_ref must be a valid " + req.Scope + " (email for user, FQDN for domain; no quotes/backslashes)"})
			return
		}
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	row := &models.MailOutboundPolicy{
		Scope:      req.Scope,
		ScopeRef:   req.ScopeRef,
		MaxPerHour: req.MaxPerHour,
		MaxPerDay:  req.MaxPerDay,
		Enabled:    enabled,
	}
	if err := h.cfg.Policies.Create(c.Request.Context(), row); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create_failed", "details": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, row)
}

func (h *adminMailThrottlesHandler) update(c *gin.Context) {
	id := c.Param("id")
	row, err := h.cfg.Policies.FindByID(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	var req throttleRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "details": err.Error()})
		return
	}
	row.MaxPerHour = req.MaxPerHour
	row.MaxPerDay = req.MaxPerDay
	if req.Enabled != nil {
		row.Enabled = *req.Enabled
	}
	if err := h.cfg.Policies.Update(c.Request.Context(), row); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update_failed", "details": err.Error()})
		return
	}
	c.JSON(http.StatusOK, row)
}

func (h *adminMailThrottlesHandler) del(c *gin.Context) {
	id := c.Param("id")
	row, err := h.cfg.Policies.FindByID(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	// Inline-delete the Stalwart-side object if we know its id.
	// Best-effort: a failure here doesn't block the DB delete, and
	// the next reconciler tick would catch a stranded Stalwart row
	// anyway IF the row still existed — but it doesn't, so we'd
	// leave a Stalwart orphan unless we try now.
	if h.cfg.ThrottleClient != nil && row.StalwartID != "" {
		cctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
		defer cancel()
		_ = h.cfg.ThrottleClient.Delete(cctx, "MtaOutboundThrottle", row.StalwartID)
	}
	if err := h.cfg.Policies.Delete(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete_failed", "details": err.Error()})
		return
	}
	c.JSON(http.StatusNoContent, nil)
}
