package reconciler

import (
	"context"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dnscompile"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// reconcileMailProviderRecords converges a zone's mail DNS to the domain's
// MailProvider (GH#181 / ADR-0120). Called from reconcileDNSZone before the
// records are listed + compiled + pushed to PowerDNS.
//
// Scopes:
//   - jabali (default): the M4 bootstrap NULL apex (MX mail.<d>, SPF,
//     _dmarc) + the m6 set own mail DNS. We only clean up leftover
//     mail-apex / mail-provider rows from a prior switch and restore the
//     bootstrap apex if a prior external/none switch removed it.
//   - m365 / google: publish the per-provider apex (mail-apex scope) +
//     non-apex (autodiscover, DKIM) rows, and PRUNE the conflicting jabali
//     rows (bootstrap apex by content-match, the m6 set, mail A/AAAA).
//   - none: prune all jabali mail rows + any mail-apex/provider rows.
//
// The apex SPF is special-cased to guarantee exactly one v=spf1 TXT at the
// apex (two = RFC 7208 permerror): for non-jabali we delete every apex SPF
// that isn't the mail-apex one before/while publishing.
func (r *Reconciler) reconcileMailProviderRecords(ctx context.Context, zone *models.DNSZone, domain *models.Domain, srv *models.ServerSettings) {
	if r.dnsRecords == nil || zone == nil || domain == nil {
		return
	}
	provider := domain.MailProvider
	if provider == "" {
		provider = models.MailProviderJabali
	}

	existing, err := r.dnsRecords.ListByZoneID(ctx, zone.ID)
	if err != nil {
		r.log.Warn("mail-provider reconcile: list records", "zone", zone.Name, "err", err)
		return
	}
	now := time.Now().UTC()

	if provider == models.MailProviderJabali {
		// Clean up any rows from a prior non-jabali switch, then make sure
		// the bootstrap apex is present (restore after switching back).
		r.deleteMailScope(ctx, existing, dnscompile.ApexMailManagedBy)
		r.deleteMailScope(ctx, existing, dnscompile.ProviderMailManagedBy(models.MailProviderM365))
		r.deleteMailScope(ctx, existing, dnscompile.ProviderMailManagedBy(models.MailProviderGoogle))
		r.restoreBootstrapApex(ctx, zone, domain, srv, existing, now)
		return
	}

	// provider is none / m365 / google.
	// 1. Prune conflicting jabali mail rows (bootstrap apex by content,
	//    m6 set, mail A/AAAA, and — for SPF — any non-mail-apex apex SPF).
	r.pruneJabaliMailRecords(ctx, zone, srv, existing)

	// 2. Reconcile the mail-apex scope to the provider's apex set.
	desiredApex := dnscompile.BuildApexMailRecords(provider, zone.Name, srv, ids.NewULID, now)
	stampZone(desiredApex, zone.ID)
	r.reconcileMailScope(ctx, zone.ID, existing, dnscompile.ApexMailManagedBy, desiredApex)

	// 3. Reconcile the provider non-apex scope (autodiscover + DKIM).
	var m365, gdkim string
	if domain.M365Onmicrosoft != nil {
		m365 = *domain.M365Onmicrosoft
	}
	if domain.GoogleDKIM != nil {
		gdkim = *domain.GoogleDKIM
	}
	desiredProv := dnscompile.BuildProviderMailRecords(provider, zone.Name, m365, gdkim, srv, ids.NewULID, now)
	stampZone(desiredProv, zone.ID)
	marker := dnscompile.ProviderMailManagedBy(provider)
	if marker != "" {
		r.reconcileMailScope(ctx, zone.ID, existing, marker, desiredProv)
	}
	// none: ensure no stray provider rows survive.
	if provider == models.MailProviderNone {
		r.deleteMailScope(ctx, existing, dnscompile.ProviderMailManagedBy(models.MailProviderM365))
		r.deleteMailScope(ctx, existing, dnscompile.ProviderMailManagedBy(models.MailProviderGoogle))
	}
}

func stampZone(recs []models.DNSRecord, zoneID string) {
	for i := range recs {
		recs[i].ZoneID = zoneID
	}
}

func managedBy(rec models.DNSRecord) string {
	if rec.ManagedBy == nil {
		return ""
	}
	return *rec.ManagedBy
}

func isApexSPF(rec models.DNSRecord) bool {
	return rec.Name == "@" && rec.Type == "TXT" && strings.HasPrefix(rec.Content, `"v=spf1`)
}

// reconcileMailScope makes the rows tagged with `marker` exactly equal to
// `desired` (matched by name+type+content): deletes scope rows not desired,
// creates desired rows not already present. Idempotent.
func (r *Reconciler) reconcileMailScope(ctx context.Context, zoneID string, existing []models.DNSRecord, marker string, desired []models.DNSRecord) {
	key := func(name, typ, content string) string { return name + "\x00" + typ + "\x00" + content }
	want := make(map[string]bool, len(desired))
	for _, d := range desired {
		want[key(d.Name, d.Type, d.Content)] = true
	}
	have := make(map[string]bool)
	for _, e := range existing {
		if managedBy(e) != marker {
			continue
		}
		k := key(e.Name, e.Type, e.Content)
		if !want[k] {
			if err := r.dnsRecords.Delete(ctx, e.ID); err != nil {
				r.log.Warn("mail-provider reconcile: delete stale", "marker", marker, "err", err)
			}
			continue
		}
		have[k] = true
	}
	for i := range desired {
		d := desired[i]
		if have[key(d.Name, d.Type, d.Content)] {
			continue
		}
		rec := d
		if err := r.dnsRecords.Create(ctx, &rec); err != nil {
			r.log.Warn("mail-provider reconcile: create", "marker", marker, "name", d.Name, "err", err)
		}
	}
}

// deleteMailScope removes every record tagged with `marker`.
func (r *Reconciler) deleteMailScope(ctx context.Context, existing []models.DNSRecord, marker string) {
	if marker == "" {
		return
	}
	for _, e := range existing {
		if managedBy(e) == marker {
			if err := r.dnsRecords.Delete(ctx, e.ID); err != nil {
				r.log.Warn("mail-provider reconcile: delete scope", "marker", marker, "err", err)
			}
		}
	}
}

// pruneJabaliMailRecords removes the jabali-owned mail rows that must not
// coexist with an external/none provider: the M4 bootstrap apex MX/SPF/
// _dmarc (matched by content so operator-authored apex records are NOT
// touched), the m6 record set, the mail A/AAAA host records, and any apex
// SPF that isn't the mail-apex one (the double-SPF guard).
func (r *Reconciler) pruneJabaliMailRecords(ctx context.Context, zone *models.DNSZone, srv *models.ServerSettings, existing []models.DNSRecord) {
	bootSPF := dnscompile.BuildSPFString(srv)
	bootMX := "mail." + zone.Name
	for _, e := range existing {
		del := false
		switch {
		case managedBy(e) == dnscompile.EmailRecordsManagedBy: // m6 set
			del = true
		case e.Name == "mail" && (e.Type == "A" || e.Type == "AAAA"):
			del = true
		case e.Name == "@" && e.Type == "MX" && e.Content == bootMX:
			del = true
		case e.Name == "_dmarc" && e.Type == "TXT" && managedBy(e) == "":
			del = true
		case isApexSPF(e) && managedBy(e) != dnscompile.ApexMailManagedBy:
			// any non-mail-apex apex SPF (bootstrap NULL or operator) —
			// remove so the mail-apex SPF is the only v=spf1 at the apex.
			del = true
		case e.Name == "@" && e.Type == "TXT" && e.Content == bootSPF:
			del = true
		}
		if del {
			if err := r.dnsRecords.Delete(ctx, e.ID); err != nil {
				r.log.Warn("mail-provider reconcile: prune jabali", "name", e.Name, "type", e.Type, "err", err)
			}
		}
	}
}

// restoreBootstrapApex re-creates the jabali bootstrap apex MX / SPF /
// _dmarc when they are absent (e.g. after switching back from an external
// provider that pruned them). Only inserts what's missing — never
// duplicates an existing apex record.
func (r *Reconciler) restoreBootstrapApex(ctx context.Context, zone *models.DNSZone, domain *models.Domain, srv *models.ServerSettings, existing []models.DNSRecord, now time.Time) {
	hasMX, hasSPF, hasDMARC := false, false, false
	hasMailA, hasMailAAAA, hasMailCNAME := false, false, false
	var dmarcRec *models.DNSRecord
	for i := range existing {
		e := existing[i]
		switch {
		case e.Name == "@" && e.Type == "MX":
			hasMX = true
		case isApexSPF(e):
			hasSPF = true
		case e.Name == "_dmarc" && e.Type == "TXT":
			hasDMARC = true
			dmarcRec = &existing[i]
		case e.Name == "mail" && e.Type == "A":
			hasMailA = true
		case e.Name == "mail" && e.Type == "AAAA":
			hasMailAAAA = true
		case e.Name == "mail" && e.Type == "CNAME":
			hasMailCNAME = true
		}
	}
	mk := func(name, typ, content string, prio int) *models.DNSRecord {
		return &models.DNSRecord{
			ID: ids.NewULID(), ZoneID: zone.ID, Name: name, Type: typ, Content: content,
			TTL: models.EffectiveDNSTTL(srv), Priority: prio, Managed: true, IsEnabled: true, CreatedAt: now, UpdatedAt: now,
		}
	}
	if !hasMX && zone.Name != "" {
		if err := r.dnsRecords.Create(ctx, mk("@", "MX", "mail."+zone.Name, 10)); err != nil {
			r.log.Warn("mail-provider reconcile: restore MX", "err", err)
		}
	}
	// GH #723: the `mail` A/AAAA is the MX target — mail can't deliver and the
	// mail-cert preflight ("mail.<domain> must resolve") can't pass without it.
	// BootstrapRecords seeds it on a FRESH zone, but a migrated zone created by
	// the bind import (bind_parse.go drops the source's mail records via
	// isMailInfraRecord, then creates the zone row when a non-mail record
	// survives) makes reconcileDNSZone skip bootstrap — so restoring MX above
	// while omitting its A target left `mail.<zone>` unresolvable. Restore it as
	// the paired half of MX. Pinned to the server primary (the MTA listens
	// there), like BootstrapRecords. Presence is checked by name+type only so an
	// operator's own `mail A` is never duplicated; a `mail` CNAME blocks both
	// families (an A/AAAA beside a same-name CNAME is a zone error).
	if !hasMailCNAME && srv != nil {
		if !hasMailA && srv.PublicIPv4 != "" {
			if err := r.dnsRecords.Create(ctx, mk("mail", "A", srv.PublicIPv4, 0)); err != nil {
				r.log.Warn("mail-provider reconcile: restore mail A", "err", err)
			}
		}
		if !hasMailAAAA && srv.PublicIPv6 != "" {
			if err := r.dnsRecords.Create(ctx, mk("mail", "AAAA", srv.PublicIPv6, 0)); err != nil {
				r.log.Warn("mail-provider reconcile: restore mail AAAA", "err", err)
			}
		}
	}
	if !hasSPF {
		if err := r.dnsRecords.Create(ctx, mk("@", "TXT", dnscompile.BuildSPFString(srv), 0)); err != nil {
			r.log.Warn("mail-provider reconcile: restore SPF", "err", err)
		}
	}
	// GH #648: the canonical _dmarc reflects the domain's DMARCbis settings
	// (np / testing). Create it when absent; re-render it when a
	// jabali-authored record has drifted from the current settings. A
	// fully operator-customised _dmarc (not a canonical variant) is left
	// untouched.
	expectedDMARC := dnscompile.BuildDMARCString(domain.DmarcNP, domain.DmarcTesting)
	if !hasDMARC {
		if err := r.dnsRecords.Create(ctx, mk("_dmarc", "TXT", expectedDMARC, 0)); err != nil {
			r.log.Warn("mail-provider reconcile: restore DMARC", "err", err)
		}
	} else if dmarcRec != nil && dnscompile.IsCanonicalDMARC(dmarcRec.Content) && dmarcRec.Content != expectedDMARC {
		dmarcRec.Content = expectedDMARC
		dmarcRec.UpdatedAt = now
		if err := r.dnsRecords.Update(ctx, dmarcRec); err != nil {
			r.log.Warn("mail-provider reconcile: re-render DMARC", "err", err)
		}
	}
}
