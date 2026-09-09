// DNS-zone delete — drop the DNS facet of a domain while keeping web + mail
// (GH #1611, johnnyq). "Host DNS elsewhere": the panel stops hosting the zone,
// but the Web Domain and Mail Domain keep working.
//
// A jabali "domain" is one row carrying web + mail + DNS facets. This is the
// mirror image of the GH #1603 facet-preserving WEB delete: there the web facet
// goes and mail/DNS may stay; here the DNS facet goes and web/mail stay. Both
// share tearDownDNSFacet so the two paths can never drift on how a zone is torn
// down.
package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// tearDownDNSFacet drops the DNS facet of dom: flip dns_disabled=true, delete the
// PowerDNS zone on the box, and remove the panel's dns_zones + dns_records rows so
// the end-state is indistinguishable from a domain created with ManageDNS=false
// (dns_disabled=1, no zone row — the reconciler never auto-creates one while
// disabled). Shared by the GH #1603 web-facet delete and the GH #1611 DNS-zone
// delete.
//
// Ordering is fail-closed: the flip runs FIRST and, if it fails, the function
// returns immediately WITHOUT deleting anything on the box — dns_disabled is
// still 0, so the reconciler still owns the zone and would resurrect whatever we
// removed. flipErr is that (hard) failure; a zone-delete or row-cleanup problem
// is non-fatal residue returned in warnings (the flag is already set, so a retry
// of the same delete converges).
func tearDownDNSFacet(
	ctx context.Context,
	domains repository.DomainRepository,
	zones repository.DNSZoneRepository,
	records repository.DNSRecordRepository,
	ag agent.AgentInterface,
	dom *models.Domain,
) (warnings []string, flipErr error) {
	// 1. dns_disabled=true BEFORE any teardown so the reconciler stops pushing
	//    the zone. On failure the reconciler still owns it → do NOT delete it.
	if err := domains.UpdateDNSDisabled(ctx, dom.ID, true); err != nil {
		return nil, err
	}

	// 2. Delete the PowerDNS zone on the box. A box without the DNS module has no
	//    PowerDNS backend; that is permanent, not a failure.
	zctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	_, zerr := ag.Call(zctx, "dns.zone.delete", map[string]string{"zone": dom.Name})
	cancel()
	if zerr != nil && !strings.Contains(zerr.Error(), "powerdns backend not available") {
		warnings = append(warnings, "DNS zone not removed on the server; remove it manually if it lingers")
	}

	// 3. Remove the panel's zone + records rows so the domain matches the
	//    ManageDNS=false shape (no zone row). FindByDomainID → ErrNotFound simply
	//    means there was never a row (created disabled) — nothing to clean. The
	//    repos are optional (nil in a dev binary without DNS wired); a missing one
	//    just leaves the rows in place, non-fatally.
	if zones == nil || records == nil {
		return warnings, nil
	}
	if z, err := zones.FindByDomainID(ctx, dom.ID); err == nil && z != nil {
		if err := records.DeleteByZoneID(ctx, z.ID); err != nil {
			warnings = append(warnings, "DNS records not cleared from the panel database")
		}
		if err := zones.Delete(ctx, z.ID); err != nil {
			warnings = append(warnings, "DNS zone row not cleared from the panel database")
		}
	}
	return warnings, nil
}

// tenantMayDeleteZone reports whether a non-admin tenant may delete an entire
// zone under the server's per-type DNS record policy (GH #466). A zone delete
// removes EVERY record, so the conservative rule is: permit it only if the
// policy allows `delete` on every user-manageable record type. If an admin has
// locked delete on even one type (say MX), a tenant cannot drop the whole zone
// out from under that restriction — they delete records within their allowance
// instead. Mirrors the per-record delete gate in deleteRecord so the zone path
// can't be used to bypass it wholesale.
func tenantMayDeleteZone(pol models.DNSUserRecordPolicy) bool {
	for _, t := range models.UserManageableDNSTypes {
		if !pol.Allows(t, "delete") {
			return false
		}
	}
	return true
}

// deleteZone serves DELETE /domains/:id/dns/zone — drop the DNS facet, keep web
// + mail. Admin or the domain's owner. Refuses (v1) rather than surprise:
//   - the panel's own primary domain (its DNS underpins the panel);
//   - a DNSSEC-signed zone (deleting it makes the domain bogus for validating
//     resolvers while the DS still sits at the registrar);
//   - the domain's LAST facet (web off + mail off): deleting DNS would leave an
//     empty row — the caller should delete the whole domain instead.
func (h *dnsHandler) deleteZone(c *gin.Context) {
	ctx := c.Request.Context()

	if h.cfg.Agent == nil {
		// Dev binary without an agent wired: the teardown can't run.
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "dns_teardown_unavailable"})
		return
	}

	// FindByID + owner-or-admin authorization (401/403/404/500 handled inside).
	dom := h.loadDomainOwned(c, c.Param("id"))
	if dom == nil {
		return
	}
	claims := ginctx.Claims(c) // non-nil: loadDomainOwned already gated it.

	if dom.IsPanelPrimary {
		c.JSON(http.StatusForbidden, gin.H{"error": "panel_primary_protected"})
		return
	}

	// Per-type DNS policy gate for non-admin tenants (GH #466). A zone delete is a
	// delete of every record, so a tenant may drop the zone only if the policy
	// permits delete on every user-manageable type; admins bypass. Same 403 shape
	// as the per-record delete gate so a restricted tenant can't bypass it here.
	if !claims.IsAdmin && !tenantMayDeleteZone(h.userRecordPolicy(ctx)) {
		c.JSON(http.StatusForbidden, gin.H{
			"error":  "record_type_forbidden",
			"detail": "Your administrator does not allow deleting one or more DNS record types, so you cannot delete the whole zone.",
		})
		return
	}

	// Transition guards only fire when DNS is currently managed. If it's already
	// disabled the call is an idempotent cleanup (a retry after a partial run),
	// so let tearDownDNSFacet mop up any lingering zone/rows and return 200.
	if !dom.DNSDisabled {
		if dom.DNSSECEnabled {
			c.JSON(http.StatusConflict, gin.H{
				"error":  "dnssec_enabled",
				"detail": "Disable DNSSEC and remove the DS record at your registrar before deleting the zone.",
			})
			return
		}
		if dom.WebDisabled && !dom.EmailEnabled {
			c.JSON(http.StatusConflict, gin.H{
				"error":  "last_facet",
				"detail": "This domain has only DNS (no web, no mail). Delete the whole domain instead.",
			})
			return
		}
	}

	// The teardown must complete even if the client hangs up.
	ctx = context.WithoutCancel(ctx)
	warnings, flipErr := tearDownDNSFacet(ctx, h.cfg.Domains, h.cfg.Zones, h.cfg.Records, h.cfg.Agent, dom)
	if flipErr != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if h.cfg.Reconciler != nil {
		h.cfg.Reconciler.Schedule(dom.ID)
	}
	c.JSON(http.StatusOK, gin.H{
		"kept":     gin.H{"web": !dom.WebDisabled, "mail": dom.EmailEnabled},
		"warnings": warnings,
	})
}
