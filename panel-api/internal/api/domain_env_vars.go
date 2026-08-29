package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/phpenv"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// domain_env_vars.go — GH #1332 item 14. Per-domain environment variables,
// delivered to PHP as nginx fastcgi_param (reach getenv() + $_SERVER). Key
// validation is a security boundary (phpenv package): a fastcgi_param named
// PHP_ADMIN_VALUE overrides the FPM sandbox.

// DomainEnvVarsHandlerConfig wires the per-domain env-var routes.
type DomainEnvVarsHandlerConfig struct {
	Domains repository.DomainRepository
}

// RegisterDomainEnvVarsRoutes adds:
//   - GET /domains/:id/env-vars
//   - PUT /domains/:id/env-vars   (full replace)
func RegisterDomainEnvVarsRoutes(g *gin.RouterGroup, cfg DomainEnvVarsHandlerConfig) {
	h := &domainEnvVarsHandler{cfg: cfg}
	g.GET("/domains/:id/env-vars", h.get)
	g.PUT("/domains/:id/env-vars", h.put)
}

type domainEnvVarsHandler struct{ cfg DomainEnvVarsHandlerConfig }

type envVarDTO struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type putEnvVarsRequest struct {
	EnvVars []envVarDTO `json:"env_vars"`
}

func (h *domainEnvVarsHandler) loadOwned(c *gin.Context) (*models.Domain, bool) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return nil, false
	}
	dom, err := h.cfg.Domains.FindByID(c.Request.Context(), c.Param("id"))
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
			return nil, false
		}
		slog.ErrorContext(c.Request.Context(), "env-vars: load domain", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, false
	}
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return nil, false
	}
	return dom, true
}

func (h *domainEnvVarsHandler) get(c *gin.Context) {
	dom, ok := h.loadOwned(c)
	if !ok {
		return
	}
	out := dom.EnvVars
	if out == nil {
		out = models.DomainEnvVars{}
	}
	c.JSON(http.StatusOK, gin.H{"env_vars": out})
}

func (h *domainEnvVarsHandler) put(c *gin.Context) {
	dom, ok := h.loadOwned(c)
	if !ok {
		return
	}
	var req putEnvVarsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	if len(req.EnvVars) > phpenv.MaxVars {
		c.JSON(http.StatusBadRequest, gin.H{"error": "too_many_env_vars", "detail": fmt.Sprintf("max %d", phpenv.MaxVars)})
		return
	}
	seen := map[string]bool{}
	out := make(models.DomainEnvVars, 0, len(req.EnvVars))
	for _, kv := range req.EnvVars {
		if err := phpenv.ValidKey(kv.Key); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_env_key", "detail": err.Error()})
			return
		}
		if err := phpenv.ValidValue(kv.Value); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_env_value", "detail": err.Error()})
			return
		}
		if seen[kv.Key] {
			c.JSON(http.StatusBadRequest, gin.H{"error": "duplicate_env_key", "detail": kv.Key})
			return
		}
		seen[kv.Key] = true
		out = append(out, models.DomainEnvVar{Key: kv.Key, Value: kv.Value})
	}
	if err := h.cfg.Domains.UpdateEnvVars(c.Request.Context(), dom.ID, out); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
			return
		}
		slog.ErrorContext(c.Request.Context(), "env-vars: update", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.Set("audit_target", "domain_env_vars:"+dom.Name)
	c.JSON(http.StatusOK, gin.H{"env_vars": out})
}
