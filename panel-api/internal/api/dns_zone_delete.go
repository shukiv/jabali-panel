// DNS-zone delete + re-enable — drop or restore the DNS facet of a domain while
// keeping web + mail (GH #1611, johnnyq). "Host DNS elsewhere": the panel stops
// hosting the zone, but the Web Domain and Mail Domain keep working; re-enabling
// hands the zone back to the reconciler.
//
// A jabali "domain" is one row carrying web + mail + DNS facets. The delete is
// the mirror image of the GH #1603 facet-preserving WEB delete: there the web
// facet goes and mail/DNS may stay; here the DNS facet goes and web/mail stay.
// Both share dnsops.TearDownFacet so the two paths can never drift on how a
// zone is torn down, and the CLI shares dnsops.DeleteZone/EnableZone so the
// operator surface enforces exactly the same guards.
package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dnsops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// tenantMayDeleteZone reports whether a non-admin tenant may delete an entire
// zone under the server's per-type DNS record policy (GH #466). A zone delete
// removes EVERY record, so the conservative rule is: permit it only if the
// policy allows `delete` on every user-manageable record type. If an admin has
// locked delete on even one type (say MX), a tenant cannot drop the whole zone
// out from under that restriction — they delete records within their allowance
// instead. Mirrors the per-record delete gate in deleteRecord so the zone path
// can't be used to bypass it wholesale. REST-only: the CLI is admin context.
func tenantMayDeleteZone(pol models.DNSUserRecordPolicy) bool {
	for _, t := range models.UserManageableDNSTypes {
		if !pol.Allows(t, "delete") {
			return false
		}
	}
	return true
}

// dnsFacetDeps builds the dnsops.Deps for this handler's repositories. The
// dnsHandler config uses Zones/Records (the domainHandler uses DNSZones/
// DNSRecords), so each caller assembles its own Deps.
func (h *dnsHandler) dnsFacetDeps() dnsops.Deps {
	d := dnsops.Deps{Domains: h.cfg.Domains, Zones: h.cfg.Zones, Records: h.cfg.Records}
	if h.cfg.Agent != nil {
		d.Call = h.cfg.Agent.Call
	}
	return d
}

// deleteZone serves DELETE /domains/:id/dns/zone — drop the DNS facet, keep web
// + mail. Admin or the domain's owner. The transition guards (panel-primary,
// DNSSEC, last-facet, idempotent-when-already-disabled) live in dnsops.DeleteZone
// so the CLI enforces them identically; this adapter adds only the REST-specific
// authorization and the GH #466 per-type policy gate.
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

	// The teardown must complete even if the client hangs up.
	ctx = context.WithoutCancel(ctx)
	warnings, err := dnsops.DeleteZone(ctx, h.dnsFacetDeps(), dom)
	if err != nil {
		switch {
		case errors.Is(err, dnsops.ErrPanelPrimary):
			c.JSON(http.StatusForbidden, gin.H{"error": "panel_primary_protected"})
		case errors.Is(err, dnsops.ErrDNSSECEnabled):
			c.JSON(http.StatusConflict, gin.H{
				"error":  "dnssec_enabled",
				"detail": "Disable DNSSEC and remove the DS record at your registrar before deleting the zone.",
			})
		case errors.Is(err, dnsops.ErrLastFacet):
			c.JSON(http.StatusConflict, gin.H{
				"error":  "last_facet",
				"detail": "This domain has only DNS (no web, no mail). Delete the whole domain instead.",
			})
		case errors.Is(err, dnsops.ErrAgentUnavailable):
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "dns_teardown_unavailable"})
		default:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		}
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

// enableZone serves POST /domains/:id/dns/zone — re-enable DNS management for a
// domain whose DNS facet was previously dropped ("host DNS here again"). Admin
// or the domain's owner. Flips dns_disabled=false and nudges the reconciler,
// which re-creates the zone row, bootstraps its records, and pushes the zone
// into PowerDNS on its next tick. No agent round-trip and no GH #466 gate: the
// reconciler system-seeds the zone exactly as it does at domain create, so this
// grants no ability to write an otherwise-forbidden record type. Idempotent for
// a domain whose DNS is already enabled.
func (h *dnsHandler) enableZone(c *gin.Context) {
	ctx := c.Request.Context()

	dom := h.loadDomainOwned(c, c.Param("id"))
	if dom == nil {
		return
	}

	if err := dnsops.EnableZone(context.WithoutCancel(ctx), dnsops.Deps{Domains: h.cfg.Domains}, dom); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	if h.cfg.Reconciler != nil {
		h.cfg.Reconciler.Schedule(dom.ID)
	}
	c.JSON(http.StatusOK, gin.H{"dns_enabled": true})
}
