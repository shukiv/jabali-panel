package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dnscompile"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// DNSScheduler schedules domain reconciliation.
type DNSScheduler interface {
	Schedule(domainID string)
}

type DNSHandlerConfig struct {
	Domains        repository.DomainRepository
	Zones          repository.DNSZoneRepository
	Records        repository.DNSRecordRepository
	ServerSettings repository.ServerSettingsRepository
	Reconciler     DNSScheduler
}

func RegisterDNSRoutes(g *gin.RouterGroup, cfg DNSHandlerConfig) {
	h := &dnsHandler{cfg: cfg}

	// Zone + records scoped under a domain
	d := g.Group("/domains/:id/dns")
	d.GET("/zone", h.getZone)
	d.PATCH("/zone", h.updateZone)
	d.GET("/records", h.listRecords)
	d.POST("/records", h.createRecord)
	// SOA + NS are auto-generated at compile time (never stored in
	// dns_records). Expose them read-only so operators can verify the
	// zone-level bits without reaching for `dig`.
	d.GET("/system-records", h.listSystemRecords)

	// Record-level operations don't need the domain id in the path
	rec := g.Group("/dns/records")
	rec.PATCH("/:recordId", h.updateRecord)
	rec.DELETE("/:recordId", h.deleteRecord)

	// Effective per-type permission matrix for the calling user (GH #466),
	// so the tenant DNS UI can reflect what it may create/edit/delete.
	g.GET("/dns/policy", h.getPolicy)
}

type dnsHandler struct {
	cfg DNSHandlerConfig
}

type updateZoneRequest struct {
	RefreshSeconds *int `json:"refresh_seconds,omitempty"`
	RetrySeconds   *int `json:"retry_seconds,omitempty"`
	ExpireSeconds  *int `json:"expire_seconds,omitempty"`
	MinimumTTL     *int `json:"minimum_ttl,omitempty"`
	IsEnabled      *bool `json:"is_enabled,omitempty"`
}

type createRecordRequest struct {
	Name      string `json:"name" binding:"required"`
	Type      string `json:"type" binding:"required"`
	Content   string `json:"content" binding:"required"`
	TTL       *int   `json:"ttl,omitempty"`
	Priority  *int   `json:"priority,omitempty"`
	IsEnabled *bool  `json:"is_enabled,omitempty"`
}

type updateRecordRequest struct {
	Name      *string `json:"name,omitempty"`
	Type      *string `json:"type,omitempty"`
	Content   *string `json:"content,omitempty"`
	TTL       *int    `json:"ttl,omitempty"`
	Priority  *int    `json:"priority,omitempty"`
	IsEnabled *bool   `json:"is_enabled,omitempty"`
}

// loadDomainOwned fetches the domain by ID and enforces that the
// caller is either the owner or an admin. Returns nil if successful,
// otherwise responds to c and returns nil.
func (h *dnsHandler) loadDomainOwned(c *gin.Context, domainID string) *models.Domain {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return nil
	}

	domain, err := h.cfg.Domains.FindByID(c.Request.Context(), domainID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return nil
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil
	}

	if !claims.IsAdmin && domain.UserID != claims.UserID {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return nil
	}

	return domain
}

func (h *dnsHandler) getZone(c *gin.Context) {
	domainID := c.Param("id")

	// Load and authorize domain
	if h.loadDomainOwned(c, domainID) == nil {
		return
	}

	zone, err := h.cfg.Zones.FindByDomainID(c.Request.Context(), domainID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "zone_not_provisioned",
				"detail": "DNS zone not yet provisioned. The reconciler will create it on next sync.",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// GH #259: include the user-record count (dns_records; SOA/NS are
	// auto-generated and not stored) so the DNS Zones list can show it
	// without a second per-domain fetch.
	recordCount := 0
	if h.cfg.Records != nil {
		if recs, rerr := h.cfg.Records.ListByZoneID(c.Request.Context(), zone.ID); rerr == nil {
			recordCount = len(recs)
		}
	}
	// GH #527: the effective default record TTL (server_settings.default_dns_ttl)
	// — what auto-records + new records use. Surfaced so the DNS Zones overview
	// shows the configured default (e.g. 300) rather than the SOA minimum_ttl
	// (a negative-cache timer that stays fixed at 3600 by design).
	c.JSON(http.StatusOK, gin.H{
		"zone":          zone,
		"record_count":  recordCount,
		"effective_ttl": h.defaultRecordTTL(c.Request.Context()),
	})
}

func (h *dnsHandler) updateZone(c *gin.Context) {
	domainID := c.Param("id")

	// Load and authorize domain
	if h.loadDomainOwned(c, domainID) == nil {
		return
	}

	var req updateZoneRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error":  "validation_failed",
			"detail": err.Error(),
		})
		return
	}

	zone, err := h.cfg.Zones.FindByDomainID(c.Request.Context(), domainID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "zone_not_provisioned",
				"detail": "DNS zone not yet provisioned. The reconciler will create it on next sync.",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Apply updates with validation
	if req.RefreshSeconds != nil {
		if *req.RefreshSeconds < 60 || *req.RefreshSeconds > 86400 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":  "validation_failed",
				"detail": "refresh_seconds must be between 60 and 86400",
			})
			return
		}
		zone.RefreshSeconds = *req.RefreshSeconds
	}

	if req.RetrySeconds != nil {
		if *req.RetrySeconds < 60 || *req.RetrySeconds > 86400 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":  "validation_failed",
				"detail": "retry_seconds must be between 60 and 86400",
			})
			return
		}
		zone.RetrySeconds = *req.RetrySeconds
	}

	if req.ExpireSeconds != nil {
		if *req.ExpireSeconds < 3600 || *req.ExpireSeconds > 2419200 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":  "validation_failed",
				"detail": "expire_seconds must be between 3600 and 2419200",
			})
			return
		}
		zone.ExpireSeconds = *req.ExpireSeconds
	}

	if req.MinimumTTL != nil {
		if *req.MinimumTTL < 60 || *req.MinimumTTL > 86400 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error":  "validation_failed",
				"detail": "minimum_ttl must be between 60 and 86400",
			})
			return
		}
		zone.MinimumTTL = *req.MinimumTTL
	}

	if req.IsEnabled != nil {
		zone.IsEnabled = *req.IsEnabled
	}

	// Persist update
	if err := h.cfg.Zones.Update(c.Request.Context(), zone); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Schedule reconciliation to push fresh SOA
	h.cfg.Reconciler.Schedule(domainID)

	c.JSON(http.StatusOK, gin.H{"zone": zone})
}

func (h *dnsHandler) listRecords(c *gin.Context) {
	domainID := c.Param("id")

	// Load and authorize domain
	if h.loadDomainOwned(c, domainID) == nil {
		return
	}

	// Load zone to get zone ID
	zone, err := h.cfg.Zones.FindByDomainID(c.Request.Context(), domainID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "zone_not_provisioned",
				"detail": "DNS zone not yet provisioned. The reconciler will create it on next sync.",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	records, err := h.cfg.Records.ListByZoneID(c.Request.Context(), zone.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	if records == nil {
		records = []models.DNSRecord{}
	}

	c.JSON(http.StatusOK, gin.H{"records": records})
}

// listSystemRecords returns the SOA + NS rows that dnscompile.Compile
// injects automatically. They never appear in dns_records and are
// regenerated from server_settings + zone scalars on every push; the
// UI shows them read-only so operators can verify what pdns serves
// without a shell `dig`. Auth mirrors listRecords — domain-scoped,
// owner or admin only.
func (h *dnsHandler) listSystemRecords(c *gin.Context) {
	domainID := c.Param("id")

	if h.loadDomainOwned(c, domainID) == nil {
		return
	}

	zone, err := h.cfg.Zones.FindByDomainID(c.Request.Context(), domainID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{
				"error":  "zone_not_provisioned",
				"detail": "DNS zone not yet provisioned. The reconciler will create it on next sync.",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// ServerSettings is optional — fresh installs before first seed
	// return ErrNotFound. dnscompile.SystemRecords handles srv=nil by
	// falling back to "zone apex acts as its own NS"; the UI will
	// surface that accurately.
	var srv *models.ServerSettings
	if h.cfg.ServerSettings != nil {
		if s, err := h.cfg.ServerSettings.Get(c.Request.Context()); err == nil {
			srv = s
		}
	}

	system := dnscompile.SystemRecords(zone, srv)
	c.JSON(http.StatusOK, gin.H{"system_records": system})
}

func (h *dnsHandler) createRecord(c *gin.Context) {
	domainID := c.Param("id")

	// Load and authorize domain
	if h.loadDomainOwned(c, domainID) == nil {
		return
	}

	var req createRecordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error":  "validation_failed",
			"detail": err.Error(),
		})
		return
	}

	// Per-type permission gate for non-admin tenants (GH #466). Admins bypass.
	if claims := ginctx.Claims(c); claims != nil && !claims.IsAdmin {
		if !h.userRecordPolicy(c.Request.Context()).Allows(req.Type, "create") {
			c.JSON(http.StatusForbidden, gin.H{
				"error":  "record_type_forbidden",
				"detail": "Your administrator does not allow creating " + strings.ToUpper(req.Type) + " records.",
			})
			return
		}
	}

	// Load zone to get zone ID
	zone, err := h.cfg.Zones.FindByDomainID(c.Request.Context(), domainID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "zone_not_provisioned",
				"detail": "DNS zone not yet provisioned. The reconciler will create it on next sync.",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Build record from request
	record := &models.DNSRecord{
		ID:     ids.NewULID(),
		ZoneID: zone.ID,
		Name:   req.Name,
		Type:   req.Type,
		Content: req.Content,
		TTL:    h.defaultRecordTTL(c.Request.Context()),
		Priority: 0,
		Managed: false,
		IsEnabled: true,
	}

	// Override defaults if provided
	if req.TTL != nil {
		record.TTL = *req.TTL
	}
	if req.Priority != nil {
		record.Priority = *req.Priority
	}
	if req.IsEnabled != nil {
		record.IsEnabled = *req.IsEnabled
	}

	// Validate record
	if err := ValidateDNSRecord(record); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "invalid_record",
			"detail": err.Error(),
		})
		return
	}

	// Conflict check: CNAME exclusivity + exact-duplicate rejection.
	if err := CheckDNSRecordConflict(c.Request.Context(), h.cfg.Records, zone.ID, record, ""); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "record_conflict", "detail": err.Error()})
		return
	}

	// Persist record
	if err := h.cfg.Records.Create(c.Request.Context(), record); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Schedule reconciliation
	h.cfg.Reconciler.Schedule(domainID)

	c.JSON(http.StatusCreated, gin.H{"record": record})
}

func (h *dnsHandler) updateRecord(c *gin.Context) {
	recordID := c.Param("recordId")

	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	var req updateRecordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"error":  "validation_failed",
			"detail": err.Error(),
		})
		return
	}

	// Load record
	record, err := h.cfg.Records.FindByID(c.Request.Context(), recordID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	oldRecordType := record.Type

	// Load zone to get domain ID
	zone, err := h.cfg.Zones.FindByID(c.Request.Context(), record.ZoneID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Load domain to authorize
	domain, err := h.cfg.Domains.FindByID(c.Request.Context(), zone.DomainID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Check authorization
	if !claims.IsAdmin && domain.UserID != claims.UserID {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	// SOA / NS are zone infrastructure, NOT content records: PowerDNS
	// regenerates the SOA serial on every change (a hand-edit is a
	// no-op it overwrites), and the apex NS set must point at jabali's
	// authoritative nameservers or the entire zone stops resolving.
	// Those two stay panel-owned. EVERYTHING else (MX/SRV/A/AAAA/TXT/
	// CNAME) is freely editable: an operator/admin edit is
	// authoritative — see the demote-to-operator-owned step after the
	// updates are applied, which makes the reconciler hand off so the
	// edit persists and converges into PowerDNS (ADR-0107).
	if record.Managed && (record.Type == "SOA" || record.Type == "NS") {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "record_managed",
			"detail": "This " + record.Type + " record is zone" +
				" infrastructure managed by jabali's nameserver" +
				" (the SOA serial is auto-generated; the apex NS set" +
				" must point at the jabali nameservers). It is not" +
				" directly editable.",
		})
		return
	}

	// Apply updates
	if req.Name != nil {
		record.Name = *req.Name
	}
	if req.Type != nil {
		record.Type = *req.Type
	}
	if req.Content != nil {
		record.Content = *req.Content
	}
	if req.TTL != nil {
		record.TTL = *req.TTL
	}
	if req.Priority != nil {
		record.Priority = *req.Priority
	}
	if req.IsEnabled != nil {
		record.IsEnabled = *req.IsEnabled
	}

	// Per-type permission gate for non-admin tenants (GH #466): editing the
	// record needs the edit right on its current type, and changing its type
	// additionally needs the create right on the new type. Admins bypass.
	if !claims.IsAdmin {
		pol := h.userRecordPolicy(c.Request.Context())
		if !pol.Allows(oldRecordType, "edit") {
			c.JSON(http.StatusForbidden, gin.H{
				"error":  "record_type_forbidden",
				"detail": "Your administrator does not allow editing " + strings.ToUpper(oldRecordType) + " records.",
			})
			return
		}
		if !strings.EqualFold(record.Type, oldRecordType) && !pol.Allows(record.Type, "create") {
			c.JSON(http.StatusForbidden, gin.H{
				"error":  "record_type_forbidden",
				"detail": "Your administrator does not allow creating " + strings.ToUpper(record.Type) + " records.",
			})
			return
		}
	}

	// Validate updated record
	if err := ValidateDNSRecord(record); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "invalid_record",
			"detail": err.Error(),
		})
		return
	}

	// Conflict check (skip self via excludeID).
	if err := CheckDNSRecordConflict(c.Request.Context(), h.cfg.Records, record.ZoneID, record, record.ID); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": "record_conflict", "detail": err.Error()})
		return
	}

	// An operator/admin edit is authoritative (ADR-0107). Demote the
	// row to operator-owned (Managed=false, ManagedBy=NULL) so every
	// reconciler path — bootstrap apex converge, migrateBootstrapShape,
	// M6 email ensure — hands off (they all gate on Managed=true and a
	// matching ManagedBy). The edited value becomes the desired state
	// the DNS reconciler pushes into PowerDNS, and nothing reverts it.
	// The model already anticipates exactly this ("…by flipping
	// Managed to false from the API", models/dns.go).
	record.Managed = false
	record.ManagedBy = nil

	// Persist update
	if err := h.cfg.Records.Update(c.Request.Context(), record); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Schedule reconciliation
	h.cfg.Reconciler.Schedule(zone.DomainID)

	c.JSON(http.StatusOK, gin.H{"record": record})
}

func (h *dnsHandler) deleteRecord(c *gin.Context) {
	recordID := c.Param("recordId")

	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	// Load record
	record, err := h.cfg.Records.FindByID(c.Request.Context(), recordID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Load zone to get domain ID
	zone, err := h.cfg.Zones.FindByID(c.Request.Context(), record.ZoneID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Load domain to authorize
	domain, err := h.cfg.Domains.FindByID(c.Request.Context(), zone.DomainID)
	if err != nil {
		if isNotFound(err) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Check authorization
	if !claims.IsAdmin && domain.UserID != claims.UserID {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}

	// Only SOA/NS are undeletable (zone infrastructure — see update
	// handler / ADR-0107). MX/SRV/etc may be deleted by the operator;
	// a feature that genuinely requires a record (e.g. M6 mail
	// autoconfig SRV while email is enabled) will legitimately
	// re-create it on the next reconcile — that is correct
	// convergence, not a reverted edit.
	if record.Managed && (record.Type == "SOA" || record.Type == "NS") {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "record_managed",
			"detail": "This " + record.Type + " record is zone" +
				" infrastructure managed by jabali's nameserver and" +
				" cannot be deleted directly.",
		})
		return
	}

	// Per-type permission gate for non-admin tenants (GH #466). Admins bypass.
	if !claims.IsAdmin && !h.userRecordPolicy(c.Request.Context()).Allows(record.Type, "delete") {
		c.JSON(http.StatusForbidden, gin.H{
			"error":  "record_type_forbidden",
			"detail": "Your administrator does not allow deleting " + strings.ToUpper(record.Type) + " records.",
		})
		return
	}

	// Delete record
	if err := h.cfg.Records.Delete(c.Request.Context(), recordID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Schedule reconciliation
	h.cfg.Reconciler.Schedule(zone.DomainID)

	c.Status(http.StatusNoContent)
}

// Validation helpers

func IsValidDNSType(t string) bool {
	switch strings.ToUpper(t) {
	case "A", "AAAA", "CNAME", "MX", "TXT", "NS", "SRV", "CAA":
		return true
	}
	return false
}

func ValidateDNSRecord(r *models.DNSRecord) error {
	r.Type = strings.ToUpper(strings.TrimSpace(r.Type))
	r.Name = strings.TrimSpace(r.Name)
	r.Content = strings.TrimSpace(r.Content)

	if !IsValidDNSType(r.Type) {
		return fmt.Errorf("unsupported record type %q (allowed: A, AAAA, CNAME, MX, TXT, NS, SRV, CAA)", r.Type)
	}
	if r.Name == "" {
		return fmt.Errorf("name required (use '@' for apex)")
	}
	if strings.ContainsAny(r.Name, " \t\n\r\x00") {
		return fmt.Errorf("name has invalid whitespace / control chars")
	}
	if r.TTL == 0 {
		r.TTL = 300
	}
	if r.TTL < 60 || r.TTL > 604800 {
		return fmt.Errorf("ttl must be between 60 and 604800 seconds")
	}

	switch r.Type {
	case "A":
		ip := net.ParseIP(r.Content)
		if ip == nil || ip.To4() == nil {
			return fmt.Errorf("A content must be an IPv4 address")
		}
	case "AAAA":
		ip := net.ParseIP(r.Content)
		if ip == nil || ip.To4() != nil {
			return fmt.Errorf("AAAA content must be an IPv6 address")
		}
	case "CNAME", "NS":
		if r.Content == "" || strings.ContainsAny(r.Content, " \t\n\r\x00") {
			return fmt.Errorf("%s content must be a hostname", r.Type)
		}
	case "MX":
		if r.Content == "" {
			return fmt.Errorf("MX content must be a hostname (target mailserver)")
		}
		if r.Priority < 0 || r.Priority > 65535 {
			return fmt.Errorf("MX priority must be 0-65535")
		}
	case "TXT":
		if len(r.Content) > 4000 {
			return fmt.Errorf("TXT content exceeds 4000 chars")
		}
	case "SRV":
		// content format: "priority weight port target"
		fields := strings.Fields(r.Content)
		if len(fields) != 4 {
			return fmt.Errorf("SRV content must be \"priority weight port target\"")
		}
		port, err := strconv.Atoi(fields[2])
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("SRV port must be 1-65535")
		}
	case "CAA":
		// content format: "<flags> <tag> \"<value>\"" per RFC 8659,
		// e.g. `0 issue "letsencrypt.org"`. flags 0-255, tag one of
		// issue/issuewild/iodef (the common set jabali emits + most
		// operators use). PowerDNS stores the content verbatim.
		fields := strings.Fields(r.Content)
		if len(fields) < 3 {
			return fmt.Errorf("CAA content must be \"<flags> <tag> \\\"<value>\\\"\" e.g. 0 issue \\\"letsencrypt.org\\\"")
		}
		flags, err := strconv.Atoi(fields[0])
		if err != nil || flags < 0 || flags > 255 {
			return fmt.Errorf("CAA flags must be 0-255")
		}
		switch fields[1] {
		case "issue", "issuewild", "iodef":
		default:
			return fmt.Errorf("CAA tag must be issue, issuewild, or iodef")
		}
	}
	return nil
}


// CheckDNSRecordConflict enforces RFC 1034 §3.6.2 (CNAME exclusivity)
// and prevents exact-duplicate rows.
//
// Rules:
//   1. Exact duplicate (same zone+name+type+content) → rejected.
//   2. CNAME at a name MUST be the only record at that name.
//      - Adding CNAME when ANY other record (A/AAAA/MX/TXT/SRV/CNAME)
//        already exists at the same name → rejected.
//      - Adding A/AAAA/MX/TXT/SRV when a CNAME already exists at the
//        same name → rejected.
//   3. Multiple CNAMEs at the same name → rejected (only one allowed).
//
// excludeID is the record being updated (skip self-conflict on edit);
// pass "" on create.
func CheckDNSRecordConflict(ctx context.Context, records repository.DNSRecordRepository, zoneID string, candidate *models.DNSRecord, excludeID string) error {
	existing, err := records.ListByZoneID(ctx, zoneID)
	if err != nil {
		return fmt.Errorf("list records for conflict check: %w", err)
	}
	candName := strings.ToLower(strings.TrimSpace(candidate.Name))
	candType := strings.ToUpper(strings.TrimSpace(candidate.Type))
	candContent := strings.TrimSpace(candidate.Content)
	for _, e := range existing {
		if e.ID == excludeID {
			continue
		}
		eName := strings.ToLower(strings.TrimSpace(e.Name))
		eType := strings.ToUpper(strings.TrimSpace(e.Type))
		if eName != candName {
			continue
		}
		// Rule 1: exact duplicate.
		if eType == candType && strings.TrimSpace(e.Content) == candContent {
			return fmt.Errorf("duplicate record: %s %s %q already exists", candidate.Name, candidate.Type, candidate.Content)
		}
		// Rule 2 + 3: CNAME exclusivity.
		if candType == "CNAME" {
			return fmt.Errorf("CNAME at %q conflicts with existing %s record (CNAME must be the only record at its name, RFC 1034 §3.6.2)", candidate.Name, e.Type)
		}
		if eType == "CNAME" {
			return fmt.Errorf("cannot add %s at %q because a CNAME already exists there (CNAME must be the only record at its name, RFC 1034 §3.6.2)", candidate.Type, candidate.Name)
		}
	}
	return nil
}

// defaultRecordTTL returns the server-wide default TTL applied to new
// DNS records when the caller didn't pass one. Reads ServerSettings;
// falls back to 3600 (the pre-2026-06 hardcoded value) on any read
// error so an empty / unreachable server_settings row doesn't break
// record creation. ADR-0140 introduced this column.
func (h *dnsHandler) defaultRecordTTL(ctx context.Context) int {
	if h.cfg.ServerSettings == nil {
		return 300
	}
	s, err := h.cfg.ServerSettings.Get(ctx)
	if err != nil || s == nil || s.DefaultDNSTTL == 0 {
		return 300
	}
	return int(s.DefaultDNSTTL)
}

// userRecordPolicy returns the effective per-type permission matrix for
// non-admin tenants (GH #466). An empty/absent stored policy falls back to the
// permissive default so a misconfiguration never locks every tenant out of DNS.
func (h *dnsHandler) userRecordPolicy(ctx context.Context) models.DNSUserRecordPolicy {
	if h.cfg.ServerSettings == nil {
		return models.DefaultDNSUserRecordPolicy()
	}
	s, err := h.cfg.ServerSettings.Get(ctx)
	if err != nil || s == nil || len(s.DNSUserRecordPolicy) == 0 {
		return models.DefaultDNSUserRecordPolicy()
	}
	return s.DNSUserRecordPolicy
}

// getPolicy returns the effective DNS record-type permissions for the caller so
// the tenant UI can reflect them. Admins bypass the matrix, so they get an
// all-permitted view.
func (h *dnsHandler) getPolicy(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}
	if claims.IsAdmin {
		c.JSON(http.StatusOK, gin.H{"policy": models.DefaultDNSUserRecordPolicy(), "is_admin": true})
		return
	}
	c.JSON(http.StatusOK, gin.H{"policy": h.userRecordPolicy(c.Request.Context()), "is_admin": false})
}
