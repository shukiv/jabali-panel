package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainmailpolicy"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// domainCatchallResponse is the response envelope for catch-all operations.
type domainCatchallResponse struct {
	DomainID   string    `json:"domain_id"`
	DomainName string    `json:"domain_name"`
	Target     *string   `json:"target"` // null if no catch-all set
	UpdatedAt  time.Time `json:"updated_at"`
	// Warning is set (JAB-338) when the DB write succeeded but the inline agent
	// push failed; the reconciler re-asserts. Omitted on a clean push.
	Warning string `json:"warning,omitempty"`
}

// catchallPush is the domainmailpolicy.PushFunc backed by the handler's agent.
func (h *domainCatchallHandler) catchallPush(ctx context.Context, cmd string, params map[string]any) error {
	if h.cfg.Agent == nil {
		return nil
	}
	agentCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := h.cfg.Agent.Call(agentCtx, cmd, params)
	return err
}

// RegisterDomainCatchallRoutes registers catch-all endpoints.
func RegisterDomainCatchallRoutes(g *gin.RouterGroup, cfg DomainCatchallHandlerConfig) {
	h := &domainCatchallHandler{cfg: cfg}
	g.GET("/domains/:id/catchall", h.get)
	g.PUT("/domains/:id/catchall", h.update)
	g.DELETE("/domains/:id/catchall", h.delete)
}

// DomainCatchallHandlerConfig holds dependencies for the catch-all handler.
type DomainCatchallHandlerConfig struct {
	Agent   agent.AgentInterface
	Domains repository.DomainRepository
}

type domainCatchallHandler struct{ cfg DomainCatchallHandlerConfig }

// get retrieves the current catch-all target for a domain.
func (h *domainCatchallHandler) get(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	dom, err := h.cfg.Domains.FindByID(ctx, c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	c.JSON(http.StatusOK, domainCatchallResponse{
		DomainID:   dom.ID,
		DomainName: dom.Name,
		Target:     dom.CatchallTarget,
		UpdatedAt:  dom.UpdatedAt,
	})
}

// updateRequest is the payload for PUT /domains/:id/catchall.
type updateCatchallRequest struct {
	Target string `json:"target"` // email address or empty to clear
}

// update sets or clears the catch-all target for a domain.
func (h *domainCatchallHandler) update(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	domID := c.Param("id")
	var req updateCatchallRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "details": err.Error()})
		return
	}

	dom, err := h.cfg.Domains.FindByID(ctx, domID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	deps := domainmailpolicy.Deps{Domains: h.cfg.Domains}
	// An empty target clears the catch-all; a non-empty one sets it (JAB-338:
	// email-enabled gate + canonical target, DB-first then best-effort push).
	if strings.TrimSpace(req.Target) == "" {
		warning, err := domainmailpolicy.ClearCatchall(ctx, deps, dom, h.catchallPush)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
		c.JSON(http.StatusOK, domainCatchallResponse{
			DomainID: domID, DomainName: dom.Name, Target: nil,
			UpdatedAt: time.Now().UTC(), Warning: warning,
		})
		return
	}
	canon, warning, err := domainmailpolicy.SetCatchall(ctx, deps, dom, req.Target, h.catchallPush)
	if err != nil {
		switch {
		case errors.Is(err, domainmailpolicy.ErrEmailNotEnabled):
			c.JSON(http.StatusBadRequest, gin.H{"error": "email_not_enabled"})
		case errors.Is(err, domainmailpolicy.ErrInvalidTarget):
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_target", "details": err.Error()})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		}
		return
	}
	ct := canon
	c.JSON(http.StatusOK, domainCatchallResponse{
		DomainID: domID, DomainName: dom.Name, Target: &ct,
		UpdatedAt: time.Now().UTC(), Warning: warning,
	})
}

// delete clears the catch-all for a domain.
func (h *domainCatchallHandler) delete(c *gin.Context) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	domID := c.Param("id")
	dom, err := h.cfg.Domains.FindByID(ctx, domID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	if _, err := domainmailpolicy.ClearCatchall(ctx,
		domainmailpolicy.Deps{Domains: h.cfg.Domains}, dom, h.catchallPush); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusNoContent, nil)
}
