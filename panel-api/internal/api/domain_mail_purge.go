// Mail-only domain delete (GH #1387, johnnyq 2026-09-01).
//
// POST /domains/:id/email/purge tears down the MAIL SERVICE for a domain while
// keeping the web domain, its DNS zone, its website cert, and its docroot:
//
//   - destroys every Stalwart account under the domain (its mailboxes)
//   - de-registers the domain in Stalwart + clears email_enabled
//   - removes the managed mail DNS (M6 autoconfig set + the mail-specific
//     bootstrap rows: mail A/AAAA, apex MX→mail, SPF, DMARC) — apex A/AAAA,
//     www, NS, and any user-edited row survive
//   - removes the per-domain mail vhost (mail./autoconfig./autodiscover.)
//   - deletes the mail TLS lineage (mail.<domain>) + its mail_certificates row
//
// This is deliberately a DISTINCT route from DELETE /domains/:id/email (soft
// disable): the distance between "turn mail off, keep everything" and "delete
// every mailbox" must never be a single flipped flag. The caller must echo the
// domain name in confirm_domain (mirrors the UI's type-to-confirm).
//
// Ordering is agent-authoritative first, DB after, and every step idempotent so
// a retry converges on a half-torn-down domain. purge_accounts is the hard gate
// (it must run while the domain is still registered in Stalwart); once it
// succeeds the teardown is committed and the rest is best-effort with warnings —
// the mail-cert reconciler and the DNS/vhost reconcilers re-converge on residue.
package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dnscompile"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type DomainMailPurgeHandlerConfig struct {
	Domains        repository.DomainRepository
	Mailboxes      repository.MailboxRepository
	MailCerts      repository.MailCertificateRepository
	Agent          agent.AgentInterface
	DNSZones       repository.DNSZoneRepository
	DNSRecords     repository.DNSRecordRepository
	ServerSettings repository.ServerSettingsRepository
}

// RegisterDomainMailPurgeRoutes mounts POST /domains/:id/email/purge. Sibling of
// the enable/disable routes; owner-or-admin, like them.
func RegisterDomainMailPurgeRoutes(g *gin.RouterGroup, cfg DomainMailPurgeHandlerConfig) {
	if cfg.Domains == nil {
		return
	}
	h := &domainMailPurgeHandler{cfg: cfg}
	g.POST("/domains/:id/email/purge", h.purge)
}

type domainMailPurgeHandler struct{ cfg DomainMailPurgeHandlerConfig }

type domainMailPurgeRequest struct {
	// ConfirmDomain must equal the domain name (case-insensitive) — a
	// server-side echo of the UI's type-to-confirm so a stray POST can't wipe
	// a domain's mail.
	ConfirmDomain string `json:"confirm_domain"`
}

type domainMailPurgeResponse struct {
	DomainID         string   `json:"domain_id"`
	DomainName       string   `json:"domain_name"`
	MailboxesDeleted int      `json:"mailboxes_deleted"`
	Warnings         []string `json:"warnings,omitempty"`
}

const domainMailPurgeAgentTimeout = 60 * time.Second

func (h *domainMailPurgeHandler) purge(c *gin.Context) {
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

	var req domainMailPurgeRequest
	_ = c.ShouldBindJSON(&req)
	if !strings.EqualFold(strings.TrimSpace(req.ConfirmDomain), dom.Name) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":  "confirm_mismatch",
			"detail": "confirm_domain must equal the domain name",
		})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "agent_unconfigured"})
		return
	}

	// Once we start the teardown it must run to completion regardless of the
	// client hanging up: purge_accounts destroys Stalwart accounts, and a
	// cancelled ctx aborting steps 2–6 would leave email_enabled=1, half-deleted
	// rows, and an auto-renewing cert with nobody signalled to retry. Decouple
	// from request cancellation (per-step timeouts below still bound it).
	ctx = context.WithoutCancel(ctx)

	var warnings []string

	// 1. HARD GATE — destroy every Stalwart account under the domain, BY DOMAIN
	//    (catches orphaned accounts a row-driven loop would miss), while the
	//    domain is still registered in Stalwart. On failure nothing DB-side has
	//    been touched: return 502 and let the operator retry (it is idempotent).
	pctx, cancel := context.WithTimeout(ctx, domainMailPurgeAgentTimeout)
	_, perr := h.cfg.Agent.Call(pctx, "mail.domain.purge_accounts", map[string]any{"domain": dom.Name})
	cancel()
	if perr != nil {
		respondAgentErr(c, "purge_accounts_failed", perr)
		return
	}

	// 2. Delete the USER mailbox rows. The domain row STAYS (web domain kept), so
	//    nothing FK-cascades — the rows must go explicitly (per-mailbox children
	//    like forwarders/autoresponders/shares do cascade off mailboxes). The
	//    hidden system relay (JAB-230 PHP mail() identity) is EXCLUDED: it is
	//    infra ensured per-domain regardless of email_enabled by the sendmail-
	//    cred reconciler, which re-converges its Stalwart account on its next
	//    tick — deleting the row here would only churn it back.
	deleted := 0
	if h.cfg.Mailboxes != nil {
		rows, _, lerr := h.cfg.Mailboxes.ListByDomainID(ctx, dom.ID, repository.ListOptions{ExcludeSystem: true})
		if lerr != nil {
			warnings = append(warnings, "could not enumerate mailboxes for row cleanup")
		}
		for i := range rows {
			if derr := h.cfg.Mailboxes.Delete(ctx, rows[i].ID); derr != nil {
				warnings = append(warnings, "mailbox row "+rows[i].LocalPart+" not removed")
				continue
			}
			deleted++
		}
	}

	// 3. De-register the domain in Stalwart + clear email_enabled. The agent
	//    teardown is best-effort (the reconciler re-converges), but the flag is
	//    always cleared — the accounts are gone, so the domain no longer has mail.
	dctx, cancel2 := context.WithTimeout(ctx, domainEmailAgentTimeout)
	_, derr := h.cfg.Agent.Call(dctx, "domain.email_disable", map[string]any{
		"domain_id":   dom.ID,
		"domain_name": dom.Name,
	})
	cancel2()
	if derr != nil {
		warnings = append(warnings, "Stalwart domain de-registration reported an error; the reconciler will retry")
	}
	if uerr := h.cfg.Domains.UpdateEmailState(ctx, dom.ID, repository.DomainEmailState{
		Enabled:        false,
		EmailEnabledAt: nil,
	}); uerr != nil {
		warnings = append(warnings, "email flag not cleared in the database")
	}

	// MTA-STS is gated on its OWN flag (mta_sts_enabled), not email_enabled, so
	// clearing email alone would leave the policy + its _mta-sts TXT live for a
	// domain that no longer has mail. Clear the flag; the MTA-STS reconciler's
	// disable branch (!enabled && applied_id != 0) tears the policy + DNS down.
	if dom.MTASTSEnabled {
		if _, mErr := h.cfg.Domains.UpdateMTASTSEnabled(ctx, dom.ID, false); mErr != nil {
			warnings = append(warnings, "MTA-STS not disabled; turn it off manually if it lingers")
		}
	}

	// 4. Remove the managed mail DNS (M6 set + mail-specific bootstrap rows).
	warnings = append(warnings, purgeMailDNS(ctx, h.cfg.DNSZones, h.cfg.DNSRecords, h.cfg.ServerSettings, dom.ID)...)

	// 5. Remove the per-domain mail vhost so nginx stops referencing the mail
	//    lineage BEFORE its files are deleted (cert/vhost delete-parity, #754).
	vctx, cancel3 := context.WithTimeout(ctx, domainEmailAgentTimeout)
	_, verr := h.cfg.Agent.Call(vctx, "webmail.vhost_remove", map[string]any{"domain_name": dom.Name})
	cancel3()
	if verr != nil {
		warnings = append(warnings, "mail vhost not removed; remove it manually if it lingers")
	}

	// 6. Delete the mail TLS lineage (auto-renewing on disk) + its DB row. Skips
	//    cleanly when the domain never opted into a mail cert.
	if h.cfg.MailCerts != nil {
		row, cerr := h.cfg.MailCerts.GetByDomain(ctx, dom.ID)
		if cerr == nil && row != nil {
			sctx, cancel4 := context.WithTimeout(ctx, domainEmailAgentTimeout)
			_, serr := h.cfg.Agent.Call(sctx, "ssl.mail.delete", map[string]any{
				"domain":       dom.Name,
				"lineage_path": row.LineagePath,
			})
			cancel4()
			if serr != nil {
				warnings = append(warnings, "mail certificate files not removed on disk; delete the lineage manually")
			}
			if delErr := h.cfg.MailCerts.Delete(ctx, row.ID); delErr != nil {
				warnings = append(warnings, "mail certificate record not removed")
			}
		}
	}

	c.JSON(http.StatusOK, domainMailPurgeResponse{
		DomainID:         dom.ID,
		DomainName:       dom.Name,
		MailboxesDeleted: deleted,
		Warnings:         warnings,
	})
}

// purgeMailDNS removes the managed mail records for a domain: the M6 set
// (autoconfig/autodiscover/SRV/TLS-RPT/DKIM/CAA, by managed_by marker) plus the
// mail-specific bootstrap rows that still carry the panel's pristine content.
// The zone itself and every non-mail or user-edited record survive. Best-effort:
// returns human-readable warnings, never an error.
func purgeMailDNS(
	ctx context.Context,
	zones repository.DNSZoneRepository,
	records repository.DNSRecordRepository,
	serverSettings repository.ServerSettingsRepository,
	domainID string,
) []string {
	if zones == nil || records == nil {
		return nil
	}
	zone, err := zones.FindByDomainID(ctx, domainID)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return []string{"mail DNS cleanup skipped: could not read the domain's zone"}
	}

	// M6-managed set — scoped by managed_by="m6", can never hit a bootstrap or
	// user row.
	_ = records.DeleteByZoneIDAndManagedBy(ctx, zone.ID, dnscompile.EmailRecordsManagedBy)

	existing, lerr := records.ListByZoneID(ctx, zone.ID)
	if lerr != nil {
		return []string{"mail DNS bootstrap cleanup skipped: could not list records"}
	}
	var srv *models.ServerSettings
	if serverSettings != nil {
		srv, _ = serverSettings.Get(ctx)
	}
	var warnings []string
	for i := range existing {
		r := &existing[i]
		if !isPristineMailBootstrapRecord(r, zone.Name, srv) {
			continue
		}
		if derr := records.Delete(ctx, r.ID); derr != nil {
			warnings = append(warnings, "mail DNS record "+r.Name+" "+r.Type+" not removed")
		}
	}
	return warnings
}

// isPristineMailBootstrapRecord reports whether r is one of the MAIL-specific
// records BootstrapRecords writes (mail A/AAAA, apex MX→mail.<zone>, apex SPF,
// _dmarc DMARC) AND still carries the panel's pristine content. It never matches:
//   - a user-edited row (Managed=false),
//   - a row owned by another pipeline (ManagedBy != nil — e.g. an m6 row, which
//     step 4 already removed by marker),
//   - the apex A/AAAA (the website IP), www, NS, or any other record.
//
// The exact-content gate means a customised SPF/DMARC, or a mail host record that
// drifted from the current server IP, is left in place rather than deleted.
func isPristineMailBootstrapRecord(r *models.DNSRecord, zoneName string, srv *models.ServerSettings) bool {
	if !r.Managed || r.ManagedBy != nil {
		return false
	}
	switch {
	case r.Name == "mail" && r.Type == "A":
		return srv != nil && srv.PublicIPv4 != "" && r.Content == srv.PublicIPv4
	case r.Name == "mail" && r.Type == "AAAA":
		return srv != nil && srv.PublicIPv6 != "" && r.Content == srv.PublicIPv6
	case r.Name == "@" && r.Type == "MX":
		return zoneName != "" && r.Priority == 10 && r.Content == "mail."+zoneName
	case r.Name == "@" && r.Type == "TXT":
		return srv != nil && hintMatches(r.Content, dnscompile.BuildSPFString(srv))
	case r.Name == "_dmarc" && r.Type == "TXT":
		return dnscompile.IsCanonicalDMARC(r.Content)
	}
	return false
}
