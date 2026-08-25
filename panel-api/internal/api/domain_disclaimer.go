// domain_disclaimer.go — M6.5 Step 6 per-domain outbound disclaimer.
//
// Wire contract: GET/PUT/DELETE /domains/:id/disclaimer
// Stalwart surface: x:SieveSystemScript named jabali-disclaimer-<domain>.
// ADR-0052: HTML-body coverage deferred pending live spike A/B.

package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainmailpolicy"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type DomainDisclaimerHandlerConfig struct {
	Domains repository.DomainRepository
	Agent   agent.AgentInterface
}

type disclaimerResponse struct {
	DomainID   string `json:"domain_id"`
	DomainName string `json:"domain_name"`
	Enabled    bool   `json:"enabled"`
	Text       string `json:"text"`
	UpdatedAt  string `json:"updated_at"`
	// Warning (JAB-338) is set when the DB write succeeded but the inline agent
	// push failed; the reconciler re-asserts. Omitted on a clean push.
	Warning string `json:"warning,omitempty"`
}

// disclaimerPush is the domainmailpolicy.PushFunc backed by the handler's agent.
func (h *domainDisclaimerHandler) disclaimerPush(ctx context.Context, cmd string, params map[string]any) error {
	if h.cfg.Agent == nil {
		return nil
	}
	agentCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := h.cfg.Agent.Call(agentCtx, cmd, params)
	return err
}

type disclaimerUpdateRequest struct {
	Enabled bool   `json:"enabled"`
	Text    string `json:"text"`
}

type domainDisclaimerHandler struct {
	cfg DomainDisclaimerHandlerConfig
}

func RegisterDomainDisclaimerRoutes(g *gin.RouterGroup, cfg DomainDisclaimerHandlerConfig) {
	if cfg.Domains == nil {
		return
	}
	h := &domainDisclaimerHandler{cfg: cfg}
	g.GET("/domains/:id/disclaimer", h.get)
	g.PUT("/domains/:id/disclaimer", h.put)
	g.DELETE("/domains/:id/disclaimer", h.del)
}

func (h *domainDisclaimerHandler) loadDomain(c *gin.Context) (*models.Domain, bool) {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	dom, err := h.cfg.Domains.FindByID(ctx, c.Param("id"))
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		}
		return nil, false
	}
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return nil, false
	}
	if !dom.EmailEnabled {
		c.JSON(http.StatusForbidden, gin.H{"error": "email_not_enabled"})
		return nil, false
	}
	return dom, true
}

func (h *domainDisclaimerHandler) get(c *gin.Context) {
	ctx := c.Request.Context()
	dom, err := h.cfg.Domains.FindByID(ctx, c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	claims := ginctx.Claims(c)
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	text := ""
	if dom.DisclaimerText != nil {
		text = *dom.DisclaimerText
	}
	c.JSON(http.StatusOK, disclaimerResponse{
		DomainID:   dom.ID,
		DomainName: dom.Name,
		Enabled:    dom.DisclaimerEnabled,
		Text:       text,
		UpdatedAt:  dom.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	})
}

func (h *domainDisclaimerHandler) put(c *gin.Context) {
	ctx := c.Request.Context()
	dom, ok := h.loadDomain(c)
	if !ok {
		return
	}
	var req disclaimerUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	norm, warning, err := domainmailpolicy.SetDisclaimer(ctx,
		domainmailpolicy.Deps{Domains: h.cfg.Domains}, dom, req.Enabled, req.Text, h.disclaimerPush)
	if err != nil {
		switch {
		case errors.Is(err, domainmailpolicy.ErrDisclaimerTextRequired):
			c.JSON(http.StatusBadRequest, gin.H{"error": "text_required_when_enabled"})
		case errors.Is(err, domainmailpolicy.ErrEmailNotEnabled):
			c.JSON(http.StatusBadRequest, gin.H{"error": "email_not_enabled"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		}
		return
	}
	c.JSON(http.StatusOK, disclaimerResponse{
		DomainID:   dom.ID,
		DomainName: dom.Name,
		Enabled:    req.Enabled,
		Text:       norm,
		UpdatedAt:  time.Now().UTC().Format("2006-01-02T15:04:05Z07:00"),
		Warning:    warning,
	})
}

func (h *domainDisclaimerHandler) del(c *gin.Context) {
	ctx := c.Request.Context()
	dom, ok := h.loadDomain(c)
	if !ok {
		return
	}
	if _, err := domainmailpolicy.ClearDisclaimer(ctx,
		domainmailpolicy.Deps{Domains: h.cfg.Domains}, dom, h.disclaimerPush); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusNoContent, nil)
}
