package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// DomainDNSSECHandlerConfig holds dependencies for DNSSEC endpoints.
type DomainDNSSECHandlerConfig struct {
	Agent   agent.AgentInterface
	Domains repository.DomainRepository
	Keys    repository.DNSSECKeyRepository
}

// RegisterDomainDNSSECRoutes mounts the three DNSSEC endpoints (ADR-0076).
func RegisterDomainDNSSECRoutes(g *gin.RouterGroup, cfg DomainDNSSECHandlerConfig) {
	if cfg.Domains == nil {
		return
	}
	h := &domainDNSSECHandler{cfg: cfg}
	g.GET("/domains/:id/dnssec", h.get)
	g.PUT("/domains/:id/dnssec", h.update)
	g.GET("/domains/:id/dnssec/ds", h.dsExport)
}

type domainDNSSECHandler struct{ cfg DomainDNSSECHandlerConfig }

// errMalformedDNSSECReply is returned (as a 502) when dns.dnssec_enable comes
// back malformed or ok=false — JAB-322 fail-closed: the DB flag stays as-is.
var errMalformedDNSSECReply = errors.New("dns.dnssec_enable returned a malformed or unsuccessful reply")

type dnssecKey struct {
	KeyTag    int    `json:"key_tag"`
	KeyType   string `json:"key_type"`
	Algorithm uint8  `json:"algorithm"`
	PublicKey string `json:"public_key"`
	Active    bool   `json:"active"`
}

type dnssecResponse struct {
	DomainID   string      `json:"domain_id"`
	DomainName string      `json:"domain_name"`
	Enabled    bool        `json:"enabled"`
	EnabledAt  *time.Time  `json:"enabled_at,omitempty"`
	Keys       []dnssecKey `json:"keys"`
}

type dnssecUpdateRequest struct {
	Enabled bool `json:"enabled"`
}

type dnssecDSResponse struct {
	DomainID   string           `json:"domain_id"`
	DomainName string           `json:"domain_name"`
	DSRecords  []dnssecDSRecord `json:"ds_records"`
}

type dnssecDSRecord struct {
	KeyTag     int    `json:"key_tag"`
	Algorithm  uint8  `json:"algorithm"`
	DigestType uint8  `json:"digest_type"`
	Digest     string `json:"digest"`
}

// loadAndAuth returns the domain after verifying ownership; writes the HTTP
// response on failure.
func (h *domainDNSSECHandler) loadAndAuth(c *gin.Context) *models.Domain {
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

// get returns the current DNSSEC state + cached keys.
// Refreshes the cache from the agent on each call when enabled — cheap
// (single pdnsutil shell-out) and keeps the UI live without an operator
// waiting for the next reconcile tick.
func (h *domainDNSSECHandler) get(c *gin.Context) {
	ctx := c.Request.Context()
	dom := h.loadAndAuth(c)
	if dom == nil {
		return
	}
	var keys []models.DomainDNSSECKey
	if dom.DNSSECEnabled && h.cfg.Agent != nil && h.cfg.Keys != nil {
		if liveKeys, ok := h.refreshKeys(ctx, dom); ok {
			keys = liveKeys
		} else if cached, err := h.cfg.Keys.ListByDomainID(ctx, dom.ID); err == nil {
			keys = cached
		}
	} else if h.cfg.Keys != nil {
		cached, err := h.cfg.Keys.ListByDomainID(ctx, dom.ID)
		if err == nil {
			keys = cached
		}
	}
	c.JSON(http.StatusOK, buildDNSSECResponse(dom, keys))
}

// update flips DNSSEC on/off. Calls the agent synchronously so the response
// reflects the new state; if the agent fails, the DB row is left unchanged
// and the caller sees 502.
func (h *domainDNSSECHandler) update(c *gin.Context) {
	ctx := c.Request.Context()
	dom := h.loadAndAuth(c)
	if dom == nil {
		return
	}
	var req dnssecUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "details": err.Error()})
		return
	}

	var liveKeys []models.DomainDNSSECKey
	if h.cfg.Agent != nil {
		agentCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		cmd := "dns.dnssec_disable"
		if req.Enabled {
			cmd = "dns.dnssec_enable"
		}
		raw, err := h.cfg.Agent.Call(agentCtx, cmd, map[string]any{"domain_name": dom.Name})
		if err != nil {
			respondAgentErr(c, "agent_error", err)
			return
		}
		if req.Enabled {
			// JAB-322 fail closed: a malformed or ok=false reply must NOT flip
			// dnssec_enabled — return 502 with the DB row untouched (the agent
			// enable did not verifiably succeed). A transport failure is already
			// an error handled above; this guards the success-with-garbage-body
			// case the old `_ = json.Unmarshal` silently accepted.
			reply, ok := models.ParseDNSSECEnableReply(raw)
			if !ok {
				respondAgentErr(c, "agent_error", errMalformedDNSSECReply)
				return
			}
			now := time.Now().UTC()
			for _, k := range reply.Keys {
				liveKeys = append(liveKeys, models.DomainDNSSECKey{
					DomainID:   dom.ID,
					KeyTag:     k.KeyTag,
					KeyType:    k.KeyType,
					Algorithm:  k.Algorithm,
					PublicKey:  k.PublicKey,
					Active:     k.Active,
					ObservedAt: now,
				})
			}
		}
	}

	if err := h.cfg.Domains.UpdateDNSSECEnabled(ctx, dom.ID, req.Enabled); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update_failed", "details": err.Error()})
		return
	}
	if h.cfg.Keys != nil {
		if req.Enabled {
			_ = h.cfg.Keys.ReplaceAll(ctx, dom.ID, liveKeys)
		} else {
			_ = h.cfg.Keys.DeleteAllForDomain(ctx, dom.ID)
		}
	}

	// Re-read the domain to pick up the new timestamp.
	dom, err := h.cfg.Domains.FindByID(ctx, dom.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.JSON(http.StatusOK, buildDNSSECResponse(dom, liveKeys))
}

// dsExport fetches the DS record set via agent. Returns 409 if DNSSEC is
// not enabled for this domain.
func (h *domainDNSSECHandler) dsExport(c *gin.Context) {
	ctx := c.Request.Context()
	dom := h.loadAndAuth(c)
	if dom == nil {
		return
	}
	if !dom.DNSSECEnabled {
		c.JSON(http.StatusConflict, gin.H{"error": "not_enabled"})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	agentCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(agentCtx, "dns.dnssec_ds_export", map[string]any{"domain_name": dom.Name})
	if err != nil {
		respondAgentErr(c, "agent_error", err)
		return
	}
	var agentResp struct {
		DSRecords []dnssecDSRecord `json:"ds_records"`
	}
	if err := json.Unmarshal(raw, &agentResp); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "agent_parse"})
		return
	}
	c.JSON(http.StatusOK, dnssecDSResponse{
		DomainID:   dom.ID,
		DomainName: dom.Name,
		DSRecords:  agentResp.DSRecords,
	})
}

// refreshKeys queries the agent for the current key set + writes the cache.
// Returns the fresh set and true on success; on any failure returns nil,false.
func (h *domainDNSSECHandler) refreshKeys(ctx context.Context, dom *models.Domain) ([]models.DomainDNSSECKey, bool) {
	agentCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(agentCtx, "dns.dnssec_keys_list", map[string]any{"domain_name": dom.Name})
	if err != nil {
		return nil, false
	}
	var agentResp struct {
		Keys []struct {
			KeyTag    int    `json:"key_tag"`
			KeyType   string `json:"key_type"`
			Algorithm uint8  `json:"algorithm"`
			PublicKey string `json:"public_key"`
			Active    bool   `json:"active"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(raw, &agentResp); err != nil {
		return nil, false
	}
	now := time.Now().UTC()
	out := make([]models.DomainDNSSECKey, 0, len(agentResp.Keys))
	for _, k := range agentResp.Keys {
		out = append(out, models.DomainDNSSECKey{
			DomainID:   dom.ID,
			KeyTag:     k.KeyTag,
			KeyType:    k.KeyType,
			Algorithm:  k.Algorithm,
			PublicKey:  k.PublicKey,
			Active:     k.Active,
			ObservedAt: now,
		})
	}
	_ = h.cfg.Keys.ReplaceAll(ctx, dom.ID, out)
	return out, true
}

func buildDNSSECResponse(dom *models.Domain, keys []models.DomainDNSSECKey) dnssecResponse {
	out := dnssecResponse{
		DomainID:   dom.ID,
		DomainName: dom.Name,
		Enabled:    dom.DNSSECEnabled,
		EnabledAt:  dom.DNSSECEnabledAt,
		Keys:       make([]dnssecKey, 0, len(keys)),
	}
	for _, k := range keys {
		out.Keys = append(out.Keys, dnssecKey{
			KeyTag:    k.KeyTag,
			KeyType:   k.KeyType,
			Algorithm: k.Algorithm,
			PublicKey: k.PublicKey,
			Active:    k.Active,
		})
	}
	return out
}
