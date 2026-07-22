package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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

// enableDomainEmailDeps is the set of collaborators the shared enable
// helper needs. Passed as a struct so both the explicit /domains/:id/email
// handler (via DomainEmailHandlerConfig) and the auto-enable path in
// domain.create (via DomainHandlerConfig) can funnel into one codepath.
//
// SSLCerts and SSLReconciler are optional — when both are set, enabling
// email on a domain flips any existing issued/self-signed cert row to
// status="renewing" so the next reconciler tick re-issues the cert with
// mail.<domain> + autoconfig.<domain> added to its SANs (M6.1). When
// they're nil, the helper skips the flip (call sites that don't wire
// SSL, like some unit tests, still work).
type enableDomainEmailDeps struct {
	Agent          agent.AgentInterface
	Domains        repository.DomainRepository
	DNSZones       repository.DNSZoneRepository
	DNSRecords     repository.DNSRecordRepository
	ServerSettings repository.ServerSettingsRepository
	SSLCerts       repository.SSLCertificateRepository
	SSLReconciler  SSLScheduler
}

// Sentinel errors so callers can distinguish HTTP status classes when
// translating the helper's failure back to a response. The helper wraps
// these with %w so callers use errors.Is.
var (
	errAgentUnconfigured = errors.New("agent unconfigured")
	errAgentFailed       = errors.New("agent call failed")
	errAgentBadResponse  = errors.New("agent returned bad response")
)

// EnableDomainEmailInline runs the shared "flip email on for this domain"
// flow: invokes domain.email_enable on the agent (which generates the
// Ed25519 DKIM keypair and registers the domain in Stalwart), persists
// the new state via UpdateEmailState, and best-effort-syncs the M6
// DNS records. On nil err, the passed dom struct is mutated to reflect
// the new state so the caller can echo it back in its response without
// re-fetching the row.
//
// Returns selector, public key, accumulated DNS warnings, and error.
// On non-nil err, nothing has been written to the DB. Wrapped errors
// come from errAgent{Unconfigured,Failed,BadResponse} so HTTP callers
// can map them to 5xx vs 502 via errors.Is.
func EnableDomainEmailInline(
	ctx context.Context,
	deps enableDomainEmailDeps,
	dom *models.Domain,
) (selector, pubKey string, warnings []string, err error) {
	if deps.Agent == nil {
		return "", "", nil, errAgentUnconfigured
	}
	agentCtx, cancel := context.WithTimeout(ctx, domainEmailAgentTimeout)
	defer cancel()
	raw, err := deps.Agent.Call(agentCtx, "domain.email_enable", map[string]any{
		"domain_id":   dom.ID,
		"domain_name": dom.Name,
	})
	if err != nil {
		return "", "", nil, fmt.Errorf("%w: %v", errAgentFailed, err)
	}
	var resp struct {
		Ok            bool   `json:"ok"`
		DKIMSelector  string `json:"dkim_selector"`
		DKIMPublicKey string `json:"dkim_public_key"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", "", nil, fmt.Errorf("%w: unmarshal: %v", errAgentBadResponse, err)
	}
	if !resp.Ok || resp.DKIMSelector == "" || resp.DKIMPublicKey == "" {
		return "", "", nil, fmt.Errorf("%w: ok=%v selector=%q pubkey-len=%d",
			errAgentBadResponse, resp.Ok, resp.DKIMSelector, len(resp.DKIMPublicKey))
	}

	selector, pubKey = resp.DKIMSelector, resp.DKIMPublicKey
	now := time.Now().UTC()
	if err := deps.Domains.UpdateEmailState(ctx, dom.ID, repository.DomainEmailState{
		Enabled:        true,
		DkimSelector:   &selector,
		DkimPublicKey:  &pubKey,
		EmailEnabledAt: &now,
	}); err != nil {
		return "", "", nil, fmt.Errorf("update email_enabled row: %w", err)
	}

	// DNS sync is best-effort. Warnings flow back to caller; a DNS-side
	// failure does NOT roll back the email_enabled flip (the mailbox
	// system still works without the convenience records).
	warnings = syncEmailDNSOnEnableInline(ctx, deps.DNSZones, deps.DNSRecords, deps.ServerSettings, dom.ID, selector, pubKey)

	// Mutate caller's Domain struct so the response reflects new state.
	dom.EmailEnabled = true
	dom.DkimSelector = &selector
	dom.DkimPublicKey = &pubKey
	dom.EmailEnabledAt = &now

	// M6.1: trigger SSL re-issuance so mail.<domain> + autoconfig.<domain>
	// land on the cert. Best-effort — any failure here is logged and
	// added to warnings, never blocks the email_enabled flip.
	if msg := triggerSSLSANExpansion(ctx, deps, dom); msg != "" {
		warnings = append(warnings, msg)
	}

	return selector, pubKey, warnings, nil
}

// triggerSSLSANExpansion flips the existing SSL cert row for this domain
// to status="renewing" so the reconciler re-issues with the new SANs on
// its next tick. Returns a human-readable warning string on any
// non-fatal failure (repo missing, cert not found, update failed); empty
// string on success or when nothing to do.
//
// Only `issued` and `self_signed` certs are flipped. Other statuses
// (pending, renewing, pending_acme_retry, failed, revoked) are left
// alone — they're either already converging or in a state the operator
// must resolve manually.
func triggerSSLSANExpansion(ctx context.Context, deps enableDomainEmailDeps, dom *models.Domain) string {
	if deps.SSLCerts == nil {
		// SSL not wired (e.g. unit-test path). Log but don't warn the
		// operator — reconciler is disabled in this path by design.
		slog.Info("email_enable: SSL reconciliation skipped (SSLCerts not wired)", "domain", dom.Name)
		return ""
	}
	cert, err := deps.SSLCerts.FindByDomainID(ctx, dom.ID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			// No cert yet — the normal ssl_enabled=true flow will
			// issue one; it'll pick up sanHostnamesForDomain on first
			// issuance.
			return ""
		}
		slog.Warn("email_enable: lookup SSL cert failed", "domain", dom.Name, "err", err)
		return "SSL cert lookup failed; retry ssl reconcile manually"
	}
	if cert.Status != models.SSLStatusIssued && cert.Status != models.SSLStatusSelfSigned {
		// Already in a transitional state — reconciler will handle it.
		return ""
	}
	if err := deps.SSLCerts.UpdateStatus(ctx, cert.ID, models.SSLStatusRenewing, nil); err != nil {
		slog.Warn("email_enable: flip cert to renewing failed", "domain", dom.Name, "err", err)
		return "SSL cert flip-to-renewing failed; mail.<domain> may be missing from cert until manual renewal"
	}
	if deps.SSLReconciler != nil {
		deps.SSLReconciler.Schedule(dom.ID)
	}
	slog.Info("email_enable: SSL cert flipped to renewing for SAN expansion", "domain", dom.Name)
	return ""
}

// DomainEmailHandlerConfig wires the email-on-domain endpoints.
//
// SSLCerts + SSLReconciler are optional — when both are wired, enabling
// email on a domain triggers SSL SAN expansion via the reconciler.
type DomainEmailHandlerConfig struct {
	Domains        repository.DomainRepository
	Agent          agent.AgentInterface
	DNSZones       repository.DNSZoneRepository
	DNSRecords     repository.DNSRecordRepository
	ServerSettings repository.ServerSettingsRepository
	SSLCerts       repository.SSLCertificateRepository
	SSLReconciler  SSLScheduler
}

const (
	// domainEmailAgentTimeout bounds the agent call budget for
	// email_enable/disable. These talk to Stalwart over the panel's
	// admin loopback and generate an Ed25519 DKIM keypair on enable —
	// both fast. 30s is generous.
	domainEmailAgentTimeout = 30 * time.Second
)

// RegisterDomainEmailRoutes mounts:
//
//   - GET    /domains/:id/email      current state (enabled flag, DKIM, recommended DNS)
//   - POST   /domains/:id/email      enable (idempotent — re-enables are ok)
//   - DELETE /domains/:id/email      disable (keeps DKIM key material per ADR-0043)
//
// Live DNS-record presence status (the blueprint's /email/dns-status)
// depends on M6 Step 5 (DNS autoconfig) which hasn't landed yet. Until
// then, GET returns a static list of the records the operator should
// publish (hint-only). The UI renders that as static instructions; once
// Step 5 ships, the "status" subfield can be populated without breaking
// the response shape.
func RegisterDomainEmailRoutes(g *gin.RouterGroup, cfg DomainEmailHandlerConfig) {
	h := &domainEmailHandler{cfg: cfg}
	g.GET("/domains/:id/email", h.get)
	g.POST("/domains/:id/email", h.enable)
	g.DELETE("/domains/:id/email", h.disable)
	g.POST("/domains/:id/email/dkim-rotate", h.rotateDKIM)
}

type domainEmailHandler struct{ cfg DomainEmailHandlerConfig }

// rotateDKIM rotates the domain's DKIM keypair (Gitea #542): the agent
// generates a fresh ed25519 key (snapshotting the old), we persist the new
// public key, then wipe + republish the M6 email DNS so the new DKIM TXT lands
// at jabali._domainkey.<domain>. The reconciler also re-converges, so this is
// belt-and-suspenders for an immediate UI refresh. Admin or the domain owner.
func (h *domainEmailHandler) rotateDKIM(c *gin.Context) {
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
	if !dom.EmailEnabled {
		c.JSON(http.StatusConflict, gin.H{"error": "email_not_enabled", "detail": "enable email on this domain before rotating DKIM"})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unconfigured"})
		return
	}
	raw, err := h.cfg.Agent.Call(ctx, "domain.email_dkim_rotate", map[string]any{"domain_name": dom.Name})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "agent_rotate_failed", "detail": firstLineString(err.Error())})
		return
	}
	var resp struct {
		OldDKIMPublicKey string `json:"old_dkim_public_key"`
		NewDKIMPublicKey string `json:"new_dkim_public_key"`
		OldKeyBackupPath string `json:"old_key_backup_path"`
	}
	if jerr := json.Unmarshal(raw, &resp); jerr != nil || resp.NewDKIMPublicKey == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "agent_bad_response", "detail": "agent returned no new DKIM key"})
		return
	}
	selector := "jabali" // EmailRecordsSelector — stable across rotations
	if uerr := h.cfg.Domains.UpdateEmailState(ctx, dom.ID, repository.DomainEmailState{
		Enabled:       true,
		DkimSelector:  &selector,
		DkimPublicKey: &resp.NewDKIMPublicKey,
	}); uerr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "persist_failed", "detail": firstLineString(uerr.Error())})
		return
	}
	// Wipe the old M6-managed records + republish so the new DKIM TXT replaces
	// the old one immediately (reconciler re-converges on its next tick too).
	h.deleteEmailDNSOnDisable(ctx, dom.ID)
	warnings := h.syncEmailDNSOnEnable(ctx, dom.ID, selector, resp.NewDKIMPublicKey)
	c.JSON(http.StatusOK, gin.H{
		"domain_id":           dom.ID,
		"domain_name":         dom.Name,
		"dkim_selector":       selector,
		"old_dkim_public_key": resp.OldDKIMPublicKey,
		"new_dkim_public_key": resp.NewDKIMPublicKey,
		"old_key_backup_path": resp.OldKeyBackupPath,
		"warnings":            warnings,
	})
}

// domainEmailResponse is what the UI reads on every poll. `warnings`
// surface operator-actionable messages — typically a conflict with a
// user-edited DNS record that M6 refused to overwrite.
type domainEmailResponse struct {
	DomainID       string               `json:"domain_id"`
	DomainName     string               `json:"domain_name"`
	EmailEnabled   bool                 `json:"email_enabled"`
	DkimSelector   string               `json:"dkim_selector,omitempty"`
	DkimPublicKey  string               `json:"dkim_public_key,omitempty"`
	EmailEnabledAt *time.Time           `json:"email_enabled_at,omitempty"`
	Records        []domainEmailDNSHint `json:"records"`
	Warnings       []string             `json:"warnings,omitempty"`
}

// domainEmailDNSHint is one recommended DNS record. `Status` is one of:
//
//	"ok"       — present in dns_records with matching content
//	"missing"  — expected but no row at (name, type)
//	"conflict" — a user-edited (ManagedBy=NULL, Managed=false) row is
//	             there with different content; M6 won't overwrite it
//	""         — zone missing (domain has no DNS zone on the panel);
//	             the UI renders this as "no live data" rather than an
//	             error so non-PowerDNS setups don't look broken
//
// Purpose is a human label for the UI table.
type domainEmailDNSHint struct {
	Purpose string `json:"purpose"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Value   string `json:"value"`
	Status  string `json:"status,omitempty"`
}

func (h *domainEmailHandler) get(c *gin.Context) {
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

	selector, pubKey := "", ""
	if dom.DkimSelector != nil {
		selector = *dom.DkimSelector
	}
	if dom.DkimPublicKey != nil {
		pubKey = *dom.DkimPublicKey
	}
	hints, warnings := h.buildHintsWithStatus(ctx, dom.ID, dom.Name, selector, pubKey)
	c.JSON(http.StatusOK, domainEmailResponse{
		DomainID:       dom.ID,
		DomainName:     dom.Name,
		EmailEnabled:   dom.EmailEnabled,
		DkimSelector:   selector,
		DkimPublicKey:  pubKey,
		EmailEnabledAt: dom.EmailEnabledAt,
		Records:        hints,
		Warnings:       warnings,
	})
}

func (h *domainEmailHandler) enable(c *gin.Context) {
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

	selector, pubKey, warnings, err := EnableDomainEmailInline(ctx, enableDomainEmailDeps{
		Agent:          h.cfg.Agent,
		Domains:        h.cfg.Domains,
		DNSZones:       h.cfg.DNSZones,
		DNSRecords:     h.cfg.DNSRecords,
		ServerSettings: h.cfg.ServerSettings,
		SSLCerts:       h.cfg.SSLCerts,
		SSLReconciler:  h.cfg.SSLReconciler,
	}, dom)
	if err != nil {
		// Translate helper error categories back to HTTP responses.
		switch {
		case errors.Is(err, errAgentUnconfigured):
			c.JSON(http.StatusInternalServerError, gin.H{"error": "agent_unconfigured"})
		case errors.Is(err, errAgentBadResponse):
			respondAgentErr(c, "agent_bad_response", err)
		case errors.Is(err, errAgentFailed):
			respondAgentErr(c, "agent_failed", err)
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		}
		return
	}

	hints, statusWarnings := h.buildHintsWithStatus(ctx, dom.ID, dom.Name, selector, pubKey)
	c.JSON(http.StatusOK, domainEmailResponse{
		DomainID:       dom.ID,
		DomainName:     dom.Name,
		EmailEnabled:   true,
		DkimSelector:   selector,
		DkimPublicKey:  pubKey,
		EmailEnabledAt: dom.EmailEnabledAt,
		Records:        hints,
		Warnings:       append(warnings, statusWarnings...),
	})
}

func (h *domainEmailHandler) disable(c *gin.Context) {
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

	// Agent is authoritative for the Stalwart-side teardown; if it
	// fails we leave the DB row alone (email_enabled=1 still) so the
	// operator can retry. This matches the delete-ordering pattern in
	// mailbox.delete.
	if h.cfg.Agent == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "agent_unconfigured"})
		return
	}
	agentCtx, cancel := context.WithTimeout(ctx, domainEmailAgentTimeout)
	defer cancel()
	if _, err := h.cfg.Agent.Call(agentCtx, "domain.email_disable", map[string]any{
		"domain_id":   dom.ID,
		"domain_name": dom.Name,
	}); err != nil {
		respondAgentErr(c, "agent_failed", err)
		return
	}

	if err := h.cfg.Domains.UpdateEmailState(ctx, dom.ID, repository.DomainEmailState{
		Enabled:        false,
		EmailEnabledAt: nil,
		// Deliberately pass nil selector/pubkey — disable keeps the
		// DKIM material so re-enable doesn't re-roll the key.
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Clean up M6-managed DNS records. M4 bootstrap records (A/MX/SPF/
	// DMARC with ManagedBy=NULL) and any user-edited rows survive.
	// Best-effort — if the DB delete fails we log and move on (the
	// email_enabled flip itself already succeeded, which is the thing
	// the operator asked for). ManagedBy-scoped WHERE clause can never
	// hit the wrong rows even if this were retried.
	h.deleteEmailDNSOnDisable(ctx, dom.ID)
	c.Status(http.StatusNoContent)
}

// syncEmailDNSOnEnableInline is the free-function form of the M6 DNS
// sync. Shared by the email-enable HTTP handler and the auto-enable
// path in domain.create. Best-effort: returns a slice of human-
// readable warning messages (conflicts, hard errors) for the UI to
// surface. Never returns an error — the email_enable flip has already
// succeeded by the time we get here, and the mailbox system stays
// usable without the convenience records.
func syncEmailDNSOnEnableInline(
	ctx context.Context,
	dnsZones repository.DNSZoneRepository,
	dnsRecords repository.DNSRecordRepository,
	serverSettings repository.ServerSettingsRepository,
	domainID, selector, dkimPub string,
) []string {
	if dnsZones == nil || dnsRecords == nil {
		// DNS repos not wired — panel running in a config without
		// PowerDNS integration. Caller (the UI) will show the hint
		// list with empty status; no warning, no error.
		return nil
	}
	zone, err := dnsZones.FindByDomainID(ctx, domainID)
	if err != nil {
		if isNotFound(err) {
			return []string{"DNS autoconfig skipped: no zone on file for this domain."}
		}
		slog.Error("m6 dns: load zone", "domain_id", domainID, "err", err)
		return []string{"DNS autoconfig failed to read the domain's zone."}
	}

	existing, err := dnsRecords.ListByZoneID(ctx, zone.ID)
	if err != nil {
		slog.Error("m6 dns: list records", "zone_id", zone.ID, "err", err)
		return []string{"DNS autoconfig couldn't read existing records."}
	}
	var srv *models.ServerSettings
	if serverSettings != nil {
		srv, _ = serverSettings.Get(ctx)
	}
	intended := dnscompile.BuildEmailRecords(zone.ID, zone.Name, selector, dkimPub, srv, ids.NewULID, time.Now().UTC())

	var warnings []string
	for _, rec := range intended {
		// Skip if we've already placed this exact M6 row on a prior
		// enable (idempotent). Match by (name, type, managed_by).
		if hasExistingM6Record(existing, rec.Name, rec.Type) {
			continue
		}
		if conflict := findConflict(existing, rec.Name, rec.Type); conflict != nil {
			warnings = append(warnings,
				"A user-edited "+rec.Type+" record at "+rec.Name+" is blocking the "+
					"autoconfig entry. Remove it in the DNS editor or accept M6 may overwrite.")
			continue
		}
		r := rec
		if err := dnsRecords.Create(ctx, &r); err != nil {
			slog.Error("m6 dns: create record", "zone_id", zone.ID, "name", rec.Name, "type", rec.Type, "err", err)
			warnings = append(warnings, "Failed to publish "+rec.Type+" record at "+rec.Name+".")
		}
	}
	return warnings
}

// syncEmailDNSOnEnable is the method-form thin wrapper kept for callers
// still using the handler receiver (disable path's sibling + tests).
func (h *domainEmailHandler) syncEmailDNSOnEnable(ctx context.Context, domainID, selector, dkimPub string) []string {
	return syncEmailDNSOnEnableInline(ctx, h.cfg.DNSZones, h.cfg.DNSRecords, h.cfg.ServerSettings, domainID, selector, dkimPub)
}

// deleteEmailDNSOnDisable removes M6-managed records (by managed_by
// marker). Silent no-op when DNS repos aren't wired.
func (h *domainEmailHandler) deleteEmailDNSOnDisable(ctx context.Context, domainID string) {
	if h.cfg.DNSZones == nil || h.cfg.DNSRecords == nil {
		return
	}
	zone, err := h.cfg.DNSZones.FindByDomainID(ctx, domainID)
	if err != nil {
		if !isNotFound(err) {
			slog.Error("m6 dns: load zone on disable", "domain_id", domainID, "err", err)
		}
		return
	}
	if err := h.cfg.DNSRecords.DeleteByZoneIDAndManagedBy(ctx, zone.ID, dnscompile.EmailRecordsManagedBy); err != nil {
		slog.Error("m6 dns: delete managed records", "zone_id", zone.ID, "err", err)
	}
}

// buildHintsWithStatus projects the authoritative M6 record set onto
// the UI's list-of-hints shape, marking each entry with its live
// status from dns_records. Records the blueprint lists (M4 + M6)
// appear here; status reflects what's actually stored in PowerDNS via
// the panel's dns_records mirror.
//
// When DNS repos aren't wired or the domain has no zone, returns the
// bare hint list with empty `Status` — the UI falls back to showing
// them as static instructions.
func (h *domainEmailHandler) buildHintsWithStatus(ctx context.Context, domainID, domainName, selector, pubKey string) ([]domainEmailDNSHint, []string) {
	hints := staticEmailHints(domainName, selector, pubKey)

	if h.cfg.DNSZones == nil || h.cfg.DNSRecords == nil {
		return hints, nil
	}
	zone, err := h.cfg.DNSZones.FindByDomainID(ctx, domainID)
	if err != nil || zone == nil {
		return hints, nil
	}
	existing, err := h.cfg.DNSRecords.ListByZoneID(ctx, zone.ID)
	if err != nil {
		return hints, nil
	}

	var warnings []string
	for i := range hints {
		// Hints use FQDN form (name + trailing dot); dns_records stores
		// short labels relative to the zone (and "@" for the apex).
		// Normalise to the short form before lookup.
		shortName := shortLabelForHint(hints[i].Name, domainName)
		rec := findRecord(existing, shortName, hints[i].Type)
		switch {
		case rec == nil:
			hints[i].Status = "missing"
		case rec.Managed:
			// Managed=true means M4 bootstrap or M6 — content belongs
			// to the panel's own render pipeline (compile.go expands
			// short labels to FQDNs at wire time). Trust it without
			// textual comparison: the hint's FQDN/priority-inlined
			// format would never match the stored short label anyway
			// ("mail" vs "10 mail.example.com."). Drift detection is
			// the reconciler's job, not this live-check.
			hints[i].Status = "ok"
		default:
			// Managed=false → user-edited. Surface as conflict so the
			// operator sees the override is blocking M6.
			hints[i].Status = "conflict"
			warnings = append(warnings,
				"User-edited "+hints[i].Type+" record at "+hints[i].Name+
					" overrides the email autoconfig; remove it to let M6 manage this slot.")
		}
	}
	return hints, warnings
}

// staticEmailHints is the pure-function part: returns the list of
// records the operator should see regardless of whether live state
// can be read. When DKIM isn't set yet (pre-enable) the DKIM entry
// still appears with an empty Value so the UI shows it as "pending".
func staticEmailHints(domainName, selector, pubKey string) []domainEmailDNSHint {
	hints := []domainEmailDNSHint{
		{Purpose: "MX — delivers incoming mail to this host", Name: domainName + ".", Type: "MX", Value: "10 mail." + domainName + "."},
		{Purpose: "SPF — authorises this host to send mail for the domain", Name: domainName + ".", Type: "TXT", Value: `v=spf1 mx ~all`},
		{Purpose: "DMARC — tells receivers to reject unauthenticated mail", Name: "_dmarc." + domainName + ".", Type: "TXT", Value: "v=DMARC1; p=quarantine; sp=quarantine; adkim=r; aspf=r"},
		{Purpose: "autoconfig — Thunderbird / mobile client auto-discovery", Name: "autoconfig." + domainName + ".", Type: "CNAME", Value: "mail." + domainName + "."},
		{Purpose: "autodiscover — Outlook auto-discovery (CNAME flavour)", Name: "autodiscover." + domainName + ".", Type: "CNAME", Value: "mail." + domainName + "."},
		{Purpose: "_autodiscover._tcp — alternative auto-discovery flavour (Outlook)", Name: "_autodiscover._tcp." + domainName + ".", Type: "SRV", Value: "0 0 443 mail." + domainName + "."},
		{Purpose: "_imap._tcp — IMAP client auto-config (RFC 6186)", Name: "_imap._tcp." + domainName + ".", Type: "SRV", Value: "0 1 143 mail." + domainName + "."},
		{Purpose: "_imaps._tcp — IMAPS client auto-config", Name: "_imaps._tcp." + domainName + ".", Type: "SRV", Value: "0 1 993 mail." + domainName + "."},
		{Purpose: "_submission._tcp — SMTP submission (STARTTLS) auto-config", Name: "_submission._tcp." + domainName + ".", Type: "SRV", Value: "0 1 587 mail." + domainName + "."},
		{Purpose: "_submissions._tcp — SMTP submission (implicit TLS) auto-config", Name: "_submissions._tcp." + domainName + ".", Type: "SRV", Value: "0 1 465 mail." + domainName + "."},
		{Purpose: "TLS-RPT — receives aggregate TLS-failure reports (RFC 8460)", Name: "_smtp._tls." + domainName + ".", Type: "TXT", Value: "v=TLSRPTv1; rua=mailto:postmaster@" + domainName},
		{Purpose: "CAA — restricts cert issuance to Let's Encrypt", Name: domainName + ".", Type: "CAA", Value: `0 issue "letsencrypt.org"`},
		{Purpose: "CAA — incident-reporting address for cert issues", Name: domainName + ".", Type: "CAA", Value: `0 iodef "mailto:postmaster@` + domainName + `"`},
	}
	if selector != "" && pubKey != "" {
		hints = append(hints, domainEmailDNSHint{
			Purpose: "DKIM — signs outbound mail so receivers can verify it",
			Name:    selector + "._domainkey." + domainName + ".",
			Type:    "TXT",
			Value:   pubKey,
		})
	} else {
		hints = append(hints, domainEmailDNSHint{
			Purpose: "DKIM — generated automatically when email is enabled",
			Name:    "<selector>._domainkey." + domainName + ".",
			Type:    "TXT",
			Value:   "",
		})
	}
	return hints
}

// shortLabelForHint maps a hint's FQDN back to the short label stored
// in dns_records. "@" is used for the apex (matches BootstrapRecords).
func shortLabelForHint(hintName, domain string) string {
	// Strip the single trailing dot.
	n := hintName
	if len(n) > 0 && n[len(n)-1] == '.' {
		n = n[:len(n)-1]
	}
	if n == domain {
		return "@"
	}
	// Strip ".<domain>" suffix to get the relative label.
	suffix := "." + domain
	if len(n) > len(suffix) && n[len(n)-len(suffix):] == suffix {
		return n[:len(n)-len(suffix)]
	}
	return n
}

func findRecord(records []models.DNSRecord, name, typ string) *models.DNSRecord {
	for i := range records {
		if records[i].Name == name && records[i].Type == typ {
			return &records[i]
		}
	}
	return nil
}

func hasExistingM6Record(records []models.DNSRecord, name, typ string) bool {
	for i := range records {
		r := &records[i]
		if r.Name == name && r.Type == typ && r.ManagedBy != nil && *r.ManagedBy == dnscompile.EmailRecordsManagedBy {
			return true
		}
	}
	return false
}

// findConflict returns an existing row at (name, type) that M6 must
// NOT overwrite — i.e. a user-edited row (Managed=false) OR a
// differently-managed panel record (Managed=true but ManagedBy != m6,
// e.g. M4 bootstrap). Returns nil when the slot is empty or already
// owned by m6 (caller should use hasExistingM6Record for that case).
func findConflict(records []models.DNSRecord, name, typ string) *models.DNSRecord {
	for i := range records {
		r := &records[i]
		if r.Name != name || r.Type != typ {
			continue
		}
		if r.ManagedBy != nil && *r.ManagedBy == dnscompile.EmailRecordsManagedBy {
			continue
		}
		return r
	}
	return nil
}

// hintMatches is a tolerant comparison for TXT-style contents where
// BootstrapRecords stores `"v=spf1..."` (quoted) and PowerDNS also
// accepts unquoted. Strips a surrounding pair of double quotes on both
// sides before comparing so we don't falsely flag a match as conflict.
func hintMatches(stored, expected string) bool {
	trim := func(s string) string {
		if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
			return s[1 : len(s)-1]
		}
		return s
	}
	return trim(stored) == trim(expected)
}
