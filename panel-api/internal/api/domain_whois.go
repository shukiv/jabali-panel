package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// DomainWhoisHandlerConfig holds dependencies for the whois endpoint (GH #254).
type DomainWhoisHandlerConfig struct {
	Agent   agent.AgentInterface
	Domains repository.DomainRepository
}

// RegisterDomainWhoisRoutes mounts GET /domains/:id/whois — the data source
// for the admin Domain "Information" modal. Read-only; dispatches the
// domain.whois agent command (shells out to the whois binary).
func RegisterDomainWhoisRoutes(g *gin.RouterGroup, cfg DomainWhoisHandlerConfig) {
	if cfg.Domains == nil {
		return
	}
	h := &domainWhoisHandler{cfg: cfg}
	g.GET("/domains/:id/whois", h.get)
}

type domainWhoisHandler struct{ cfg DomainWhoisHandlerConfig }

// whoisData mirrors the agent's domainWhoisResult shape so the panel-api can
// re-marshal it into the HTTP response without coupling to the agent package.
type whoisData struct {
	Domain      string   `json:"domain"`
	Registrar   string   `json:"registrar,omitempty"`
	CreatedDate string   `json:"created_date,omitempty"`
	UpdatedDate string   `json:"updated_date,omitempty"`
	ExpiryDate  string   `json:"expiry_date,omitempty"`
	Registrant  string   `json:"registrant,omitempty"`
	NameServers []string `json:"name_servers,omitempty"`
	Statuses    []string `json:"statuses,omitempty"`
	Raw         string   `json:"raw"`
}

type whoisResponse struct {
	DomainID   string    `json:"domain_id"`
	DomainName string    `json:"domain_name"`
	Whois      whoisData `json:"whois"`
}

// loadAndAuth returns the domain after verifying ownership; writes the HTTP
// response on failure. Mirrors the DNSSEC handler.
func (h *domainWhoisHandler) loadAndAuth(c *gin.Context) *models.Domain {
	ctx := c.Request.Context()
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return nil
	}
	dom, err := h.cfg.Domains.FindByID(ctx, c.Param("id"))
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
			return nil
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil
	}
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return nil
	}
	return dom
}

// get fetches whois for the domain via the agent. whois is a network call to
// the registry (TCP/43) that can hang, so the agent runs under a 20s deadline.
func (h *domainWhoisHandler) get(c *gin.Context) {
	ctx := c.Request.Context()
	dom := h.loadAndAuth(c)
	if dom == nil {
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	agentCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(agentCtx, "domain.whois", map[string]any{"domain": dom.Name})
	if err != nil {
		respondAgentErr(c, "agent_error", err)
		return
	}
	var data whoisData
	if err := json.Unmarshal(raw, &data); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "agent_parse"})
		return
	}
	c.JSON(http.StatusOK, whoisResponse{
		DomainID:   dom.ID,
		DomainName: dom.Name,
		Whois:      data,
	})
}
