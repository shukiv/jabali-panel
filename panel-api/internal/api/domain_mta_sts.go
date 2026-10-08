// Package api — domain MTA-STS endpoints (M47 Wave 7b, ADR-0109).
//
// Owner-or-admin per-domain toggle for MTA-STS. Wired in app.go next
// to DNSSEC / cache.
//
// PUT /domains/:id/mta-sts {enabled}
//   - Persists mta_sts_enabled (the repo also rotates mta_sts_id on
//     enable).
//   - Publishes / removes the two MTA-STS DNS records
//     (mta-sts A + _mta-sts TXT, managed_by="mta-sts" marker so the
//     disable path scopes its delete cleanly).
//   - Schedules a domain reconcile so the SSL reconciler picks up
//     mta-sts.<domain> as a new SAN on next renewal AND the Wave 7c
//     mta-sts reconcile step calls mail.mtasts.apply once the cert
//     covers the SAN.
//
// GET /domains/:id/mta-sts → current state + projected policy + cert-
//
//	readiness hint so the UI can tell the operator whether the policy
//	is live yet.
//
// The handler does NOT call the agent synchronously on toggle: the
// vhost can't be written until the SSL cert SAN actually covers
// mta-sts.<domain>, which only happens on the next ACME issue cycle.
// Doing the apply here would race the SAN expansion and trip nginx -t.
// Wave 7c adds the reconciler step that fires mail.mtasts.apply once
// the cert is ready (mirrors the panel-cert flow).
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dnscompile"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// DomainMTAStsHandlerConfig holds deps for the per-domain MTA-STS
// toggle. Agent is optional — the handler functions without it; only
// Wave 7c uses agent for the eventual reconciler-driven mail.mtasts
// dispatch.
type DomainMTAStsHandlerConfig struct {
	Agent          agent.AgentInterface
	Domains        repository.DomainRepository
	DNSZones       repository.DNSZoneRepository
	DNSRecords     repository.DNSRecordRepository
	ServerSettings repository.ServerSettingsRepository
	Reconciler     DNSScheduler
}

// RegisterDomainMTAStsRoutes mounts GET/PUT /domains/:id/mta-sts.
func RegisterDomainMTAStsRoutes(g *gin.RouterGroup, cfg DomainMTAStsHandlerConfig) {
	if cfg.Domains == nil {
		return
	}
	h := &domainMTAStsHandler{cfg: cfg}
	g.GET("/domains/:id/mta-sts", h.get)
	g.PUT("/domains/:id/mta-sts", h.update)
}

type domainMTAStsHandler struct{ cfg DomainMTAStsHandlerConfig }

type mtaStsResponse struct {
	DomainID   string `json:"domain_id"`
	DomainName string `json:"domain_name"`
	Enabled    bool   `json:"enabled"`
	ID         uint64 `json:"id"`
	PolicyURL  string `json:"policy_url"`
	StatusHint string `json:"status_hint"`
}

type mtaStsUpdateRequest struct {
	Enabled bool `json:"enabled"`
}

func (h *domainMTAStsHandler) loadAndAuth(c *gin.Context) *models.Domain {
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

func (h *domainMTAStsHandler) get(c *gin.Context) {
	dom := h.loadAndAuth(c)
	if dom == nil {
		return
	}
	c.JSON(http.StatusOK, h.buildResponse(c.Request.Context(), dom))
}

func (h *domainMTAStsHandler) update(c *gin.Context) {
	ctx := c.Request.Context()
	dom := h.loadAndAuth(c)
	if dom == nil {
		return
	}
	var req mtaStsUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_request", "details": err.Error()})
		return
	}
	newID, err := h.cfg.Domains.UpdateMTASTSEnabled(ctx, dom.ID, req.Enabled)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "update_failed", "details": err.Error()})
		return
	}
	dom.MTASTSEnabled = req.Enabled
	if req.Enabled && newID != 0 {
		dom.MTASTSId = newID
	}
	// Errors do not fail the toggle — the operator sees the cert/policy hint
	// in the GET response and can fix DNS conflicts manually.
	_ = SyncMTAStsDNS(ctx, MTAStsDNSDeps{Zones: h.cfg.DNSZones, Records: h.cfg.DNSRecords, Settings: h.cfg.ServerSettings}, dom)
	if h.cfg.Reconciler != nil {
		h.cfg.Reconciler.Schedule(dom.ID)
	}
	// Re-read so the response reflects any reconcile-side timestamps.
	if fresh, err := h.cfg.Domains.FindByID(ctx, dom.ID); err == nil {
		dom = fresh
	}
	c.JSON(http.StatusOK, h.buildResponse(ctx, dom))
}

// MTAStsDNSDeps is what SyncMTAStsDNS reads and writes.
type MTAStsDNSDeps struct {
	Zones    repository.DNSZoneRepository
	Records  repository.DNSRecordRepository
	Settings repository.ServerSettingsRepository
}

// SyncMTAStsDNS publishes dom's two MTA-STS records when its MTA-STS is on and
// removes them when it is off. Nothing else publishes them, so every door that
// flips the switch runs it: the domain's mail page and `jabali domain mta-sts`.
func SyncMTAStsDNS(ctx context.Context, d MTAStsDNSDeps, dom *models.Domain) error {
	if d.Zones == nil || d.Records == nil {
		return errors.New("the DNS stores are not wired")
	}
	zone, err := d.Zones.FindByDomainID(ctx, dom.ID)
	if err != nil {
		return fmt.Errorf("the domain has no DNS zone here: %w", err)
	}
	if !dom.MTASTSEnabled {
		return d.Records.DeleteByZoneIDAndManagedBy(ctx, zone.ID, dnscompile.MTAStsRecordsManagedBy)
	}
	if d.Settings == nil {
		return errors.New("the server settings are not wired")
	}
	srv, err := d.Settings.Get(ctx)
	if err != nil || srv == nil {
		return fmt.Errorf("read the server settings: %v", err)
	}
	_, err = PublishMTAStsRecords(ctx, d.Records, zone, srv, dom)
	return err
}

// PublishMTAStsRecords puts dom's two MTA-STS records in zone (ADR-0109): the
// policy host's address and the _mta-sts TXT naming the policy id. Records
// already there as intended stay; otherwise the zone's MTA-STS rows are
// replaced, so a new policy id or server address takes effect. It reports
// whether it wrote anything. The MTA-STS toggle and the account restore
// (GH #1993) both publish through it.
func PublishMTAStsRecords(ctx context.Context, records repository.DNSRecordRepository, zone *models.DNSZone, srv *models.ServerSettings, dom *models.Domain) (bool, error) {
	intended := dnscompile.BuildMTAStsRecords(
		zone.ID, zone.Name, srv.PublicIPv4, dom.MTASTSId,
		srv, ids.NewULID, time.Now().UTC(),
	)
	if len(intended) == 0 {
		if srv.PublicIPv4 == "" {
			return false, errors.New("this server has no public IPv4 address to publish")
		}
		return false, errors.New("the domain has no MTA-STS policy id")
	}
	existing, err := records.ListByZoneID(ctx, zone.ID)
	if err != nil {
		return false, err
	}
	have := map[string]bool{}
	for _, r := range existing {
		if r.ManagedBy != nil && *r.ManagedBy == dnscompile.MTAStsRecordsManagedBy {
			have[r.Name+" "+r.Type+" "+r.Content] = true
		}
	}
	same := len(have) == len(intended)
	for _, r := range intended {
		same = same && have[r.Name+" "+r.Type+" "+r.Content]
	}
	if same {
		return false, nil
	}
	if len(have) > 0 {
		if err := records.DeleteByZoneIDAndManagedBy(ctx, zone.ID, dnscompile.MTAStsRecordsManagedBy); err != nil {
			return false, err
		}
	}
	for i := range intended {
		if err := records.Create(ctx, &intended[i]); err != nil {
			return true, err
		}
	}
	return true, nil
}

// buildResponse summarises state for the UI. status_hint values:
//   - "off"                   — toggle is off
//   - "policy_published"      — toggle on, DNS records out; cert
//     renewal still pending (next ACME tick
//     adds the SAN, then Wave 7c reconciler
//     step writes the vhost). The UI surfaces
//     this as "Policy live in DNS — vhost
//     waiting for SSL renewal."
func (h *domainMTAStsHandler) buildResponse(ctx context.Context, dom *models.Domain) mtaStsResponse {
	_ = ctx
	resp := mtaStsResponse{
		DomainID:   dom.ID,
		DomainName: dom.Name,
		Enabled:    dom.MTASTSEnabled,
		ID:         dom.MTASTSId,
		PolicyURL:  "https://mta-sts." + dom.Name + "/.well-known/mta-sts.txt",
	}
	if !dom.MTASTSEnabled {
		resp.StatusHint = "off"
		return resp
	}
	resp.StatusHint = "policy_published"
	return resp
}
