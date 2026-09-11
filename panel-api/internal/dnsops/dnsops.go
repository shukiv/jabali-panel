// Package dnsops is the shared DNS-facet lifecycle (GH #1611): the tear-down
// and re-enable operations the REST handlers and the operator CLI both route
// through, so the fail-closed ordering, the transition guards, and the
// PowerDNS/row cleanup have one owner and cannot drift between the two.
//
// A jabali "domain" is one row carrying web + mail + DNS facets. Dropping the
// DNS facet flips dns_disabled=true, deletes the PowerDNS zone on the box, and
// removes the panel's dns_zones + dns_records rows so the end-state is
// indistinguishable from a domain created with ManageDNS=false. Re-enabling
// flips dns_disabled=false; the reconciler then re-creates the zone row,
// bootstraps its records, and pushes the zone into PowerDNS on its next tick
// (the Schedule nudge stays a caller concern to avoid a reconciler import
// cycle).
//
// Authorization stays an Adapter concern: every entry point takes an already-
// loaded, already-authorized domain (the REST handler checks the caller's
// claims and the GH #466 per-type policy; the operator CLI is admin-by-
// construction). TearDownFacet is the UNGUARDED teardown shared with the GH
// #1603 web-facet delete, which runs its own sequence; DeleteZone wraps it with
// the transition guards the two zone-delete entry points (REST + CLI) share.
package dnsops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// CallFunc is the hard agent call TearDownFacet uses to delete the PowerDNS
// zone — its error is folded into warnings (the flag is already flipped, so a
// retry converges), never a hard abort of the teardown.
type CallFunc func(ctx context.Context, cmd string, params any) (json.RawMessage, error)

// Deps carries the collaborators every operation needs. Zones/Records are
// optional (nil in a dev binary without DNS wired): a nil pair just leaves the
// rows in place, non-fatally. Call is required — a nil Call fails closed before
// anything is flipped.
type Deps struct {
	Domains repository.DomainRepository
	Zones   repository.DNSZoneRepository
	Records repository.DNSRecordRepository
	Call    CallFunc
}

var (
	ErrDeps             = errors.New("dnsops: dependencies not wired")
	ErrAgentUnavailable = errors.New("dnsops: agent not configured")
	ErrPanelPrimary     = errors.New("dnsops: the panel's own primary domain is protected")
	ErrDNSSECEnabled    = errors.New("dnsops: zone is DNSSEC-signed; disable DNSSEC and remove the DS record first")
	ErrLastFacet        = errors.New("dnsops: DNS is the domain's only facet; delete the whole domain instead")
)

// TearDownFacet drops the DNS facet of dom: flip dns_disabled=true, delete the
// PowerDNS zone on the box, and remove the panel's dns_zones + dns_records rows
// so the end-state is indistinguishable from a domain created with
// ManageDNS=false (dns_disabled=1, no zone row — the reconciler never
// auto-creates one while disabled). Shared by the GH #1603 web-facet delete and
// the GH #1611 DNS-zone delete, so the two paths can never drift on how a zone
// is torn down.
//
// Ordering is fail-closed: the flip runs FIRST and, if it fails, the function
// returns immediately WITHOUT deleting anything on the box — dns_disabled is
// still 0, so the reconciler still owns the zone and would resurrect whatever
// we removed. flipErr is that (hard) failure; a zone-delete or row-cleanup
// problem is non-fatal residue returned in warnings (the flag is already set,
// so a retry of the same delete converges). This carries NO transition guards:
// the web-facet delete calls it directly after its own sequence.
func TearDownFacet(ctx context.Context, d Deps, dom *models.Domain) (warnings []string, flipErr error) {
	if d.Domains == nil || dom == nil {
		return nil, ErrDeps
	}
	if d.Call == nil {
		// Fail closed: without an agent we cannot delete the box zone, so do
		// NOT flip the flag and strand the panel rows out of sync.
		return nil, ErrAgentUnavailable
	}

	// 1. dns_disabled=true BEFORE any teardown so the reconciler stops pushing
	//    the zone. On failure the reconciler still owns it → do NOT delete it.
	if err := d.Domains.UpdateDNSDisabled(ctx, dom.ID, true); err != nil {
		return nil, err
	}

	// 2. Delete the PowerDNS zone on the box. A box without the DNS module has
	//    no PowerDNS backend; that is permanent, not a failure.
	zctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	_, zerr := d.Call(zctx, "dns.zone.delete", map[string]string{"zone": dom.Name})
	cancel()
	if zerr != nil && !strings.Contains(zerr.Error(), "powerdns backend not available") {
		warnings = append(warnings, "DNS zone not removed on the server; remove it manually if it lingers")
	}

	// 3. Remove the panel's zone + records rows so the domain matches the
	//    ManageDNS=false shape (no zone row). FindByDomainID → ErrNotFound simply
	//    means there was never a row (created disabled) — nothing to clean. The
	//    repos are optional (nil in a dev binary without DNS wired); a missing
	//    one just leaves the rows in place, non-fatally.
	if d.Zones == nil || d.Records == nil {
		return warnings, nil
	}
	if z, err := d.Zones.FindByDomainID(ctx, dom.ID); err == nil && z != nil {
		if err := d.Records.DeleteByZoneID(ctx, z.ID); err != nil {
			warnings = append(warnings, "DNS records not cleared from the panel database")
		}
		if err := d.Zones.Delete(ctx, z.ID); err != nil {
			warnings = append(warnings, "DNS zone row not cleared from the panel database")
		}
	}
	return warnings, nil
}

// DeleteZone is the guarded zone-delete entry point the REST handler and the
// operator CLI share. It enforces the transition guards, then runs the shared
// TearDownFacet. The guards fire only while DNS is currently managed: if
// dns_disabled is already true the call is an idempotent cleanup (a retry after
// a partial run), so it skips straight to TearDownFacet to mop up any lingering
// zone/rows. Refuses (v1) rather than surprise:
//   - the panel's own primary domain (its DNS underpins the panel);
//   - a DNSSEC-signed zone (deleting it makes the domain bogus for validating
//     resolvers while the DS still sits at the registrar);
//   - the domain's LAST facet (web off + mail off): deleting DNS would leave an
//     empty row — the caller should delete the whole domain instead.
//
// Authorization (owner-or-admin) and the GH #466 per-type policy gate stay with
// the REST adapter; the CLI is admin-by-construction and skips them.
func DeleteZone(ctx context.Context, d Deps, dom *models.Domain) (warnings []string, err error) {
	if d.Domains == nil || dom == nil {
		return nil, ErrDeps
	}
	if d.Call == nil {
		return nil, ErrAgentUnavailable
	}
	// The panel's own primary domain is never facet-deleted, even as an
	// idempotent retry — its DNS underpins the panel.
	if dom.IsPanelPrimary {
		return nil, ErrPanelPrimary
	}
	// Transition guards only fire when DNS is currently managed. If it is
	// already disabled the call is an idempotent cleanup.
	if !dom.DNSDisabled {
		if dom.DNSSECEnabled {
			return nil, ErrDNSSECEnabled
		}
		if dom.WebDisabled && !dom.EmailEnabled {
			return nil, ErrLastFacet
		}
	}
	return TearDownFacet(ctx, d, dom)
}

// EnableZone re-enables DNS management for dom by flipping dns_disabled=false.
// It is the mirror of the DNS-facet delete: the reconciler owns everything
// after the flip — on its next tick it re-creates the dns_zones row, bootstraps
// the zone's records, and pushes the zone into PowerDNS (dns.zone.upsert
// re-creates the box zone). The caller schedules that reconcile pass; the leaf
// deliberately does not import the reconciler.
//
// Idempotent: a domain whose DNS is already enabled returns nil without a write.
// Panel-primary domains are always DNS-enabled, so this is a no-op for them.
func EnableZone(ctx context.Context, d Deps, dom *models.Domain) error {
	if d.Domains == nil || dom == nil {
		return ErrDeps
	}
	if !dom.DNSDisabled {
		return nil // already enabled — nothing to flip
	}
	if err := d.Domains.UpdateDNSDisabled(ctx, dom.ID, false); err != nil {
		return err
	}
	dom.DNSDisabled = false
	return nil
}
