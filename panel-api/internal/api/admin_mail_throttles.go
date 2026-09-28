// Package api — admin Mail outbound-throttle CRUD (M47 Wave 3).
//
// Admin-only. Writes to mail_outbound_policy; the reconciler converges
// each row into Stalwart's MtaOutboundThrottle objects on the next tick,
// through the agent (the panel cannot reach Stalwart's admin API).
//
// Endpoints:
//
//	GET    /admin/mail/throttles            — list all rows
//	POST   /admin/mail/throttles            — create new row
//	PUT    /admin/mail/throttles/:id        — update existing
//	DELETE /admin/mail/throttles/:id        — remove the row and its
//	                                          Stalwart throttles (inline:
//	                                          once the row is gone the
//	                                          reconciler cannot find them)
package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/mailthrottle"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// validateScopeRef pre-empts Stalwart Expression injection. The agent
// builds expressions like `sender_domain == '<scope_ref>'` with scope_ref
// embedded verbatim; a single quote would turn the throttle's match into
// always-fire (or always-skip), silently breaking the cap. The agent runs
// the same check, from the same package, before it builds anything.
func validateScopeRef(scope, ref string) bool {
	return mailthrottle.ValidScopeRef(scope, ref)
}

// AdminMailThrottlesHandlerConfig — single dep.
type AdminMailThrottlesHandlerConfig struct {
	Policies repository.MailOutboundPolicyRepository
	// ThrottleClient removes a deleted row's Stalwart throttles
	// (agent.MailThrottles). When nil, DELETE only removes the DB row.
	ThrottleClient ThrottleDispatcher
}

// ThrottleDispatcher is the delete half of reconciler.ThrottleApplier.
// Deleting an id Stalwart no longer has succeeds.
type ThrottleDispatcher interface {
	Delete(ctx context.Context, stalwartID string) error
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
	// A row owns up to two Stalwart throttles (hourly StalwartID, daily
	// StalwartIDDaily). Once the row is gone nothing points at them, so they
	// go first. If one cannot be removed, the row stays, disabled: the
	// reconciler keeps retrying the removal, and the admin can see it and
	// delete again. Deleting the row anyway would leave a cap in Stalwart
	// that the panel no longer shows.
	if h.cfg.ThrottleClient != nil && (row.StalwartID != "" || row.StalwartIDDaily != "") {
		if row.Enabled {
			row.Enabled = false
			if err := h.cfg.Policies.Update(c.Request.Context(), row); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "delete_failed", "details": err.Error()})
				return
			}
		}
		cctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
		defer cancel()
		var errs []error
		for _, sid := range []string{row.StalwartID, row.StalwartIDDaily} {
			if sid != "" {
				if err := h.cfg.ThrottleClient.Delete(cctx, sid); err != nil {
					errs = append(errs, err)
				}
			}
		}
		if err := errors.Join(errs...); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{
				"error":   "stalwart_delete_failed",
				"details": "the throttle is disabled and kept until Stalwart removes it: " + err.Error(),
			})
			return
		}
	}
	if err := h.cfg.Policies.Delete(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete_failed", "details": err.Error()})
		return
	}
	c.JSON(http.StatusNoContent, nil)
}
