package api

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// domainCatchallResponse is the response envelope for catch-all operations.
type domainCatchallResponse struct {
	DomainID   string    `json:"domain_id"`
	DomainName string    `json:"domain_name"`
	Target     *string   `json:"target"` // null if no catch-all set
	UpdatedAt  time.Time `json:"updated_at"`
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

	// Call agent to update catch-all on Stalwart
	if h.cfg.Agent != nil {
		agentCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		_, err := h.cfg.Agent.Call(agentCtx, "domain.catchall_set", map[string]any{
			"domain_id":   domID,
			"domain_name": dom.Name,
			"target":      req.Target,
		})
		if err != nil {
			respondAgentErr(c, "agent_error", err)
			return
		}
	}

	// Update database
	now := time.Now().UTC()
	var target *string
	if req.Target != "" {
		target = &req.Target
	}
	if err := h.cfg.Domains.UpdateCatchallTarget(ctx, domID, target); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update_failed", "details": err.Error()})
		return
	}

	c.JSON(http.StatusOK, domainCatchallResponse{
		DomainID:   domID,
		DomainName: dom.Name,
		Target:     target,
		UpdatedAt:  now,
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

	// Call agent to clear catch-all on Stalwart
	if h.cfg.Agent != nil {
		agentCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		_, err := h.cfg.Agent.Call(agentCtx, "domain.catchall_clear", map[string]any{
			"domain_id":   domID,
			"domain_name": dom.Name,
		})
		if err != nil {
			respondAgentErr(c, "agent_error", err)
			return
		}
	}

	// Clear database
	if err := h.cfg.Domains.UpdateCatchallTarget(ctx, domID, nil); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "delete_failed", "details": err.Error()})
		return
	}

	c.JSON(http.StatusNoContent, nil)
}
