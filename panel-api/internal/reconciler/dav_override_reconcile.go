package reconciler

import (
	"context"
	"strconv"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dnscompile"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// reconcileDAVOverrideRecords (GH #1462) converges a jabali-mail domain's
// CalDAV / CardDAV SRV records to its per-domain override.
//
// BuildEmailRecords seeds four RFC 6764 rows pointing at mail.<domain>:
//
//	_caldavs._tcp  SRV  "1 443 mail.<domain>"   (secure, 443)
//	_carddavs._tcp SRV  "1 443 mail.<domain>"
//	_caldav._tcp   SRV  "1 80 mail.<domain>"    (insecure, 80)
//	_carddav._tcp  SRV  "1 80 mail.<domain>"
//
// When the operator sets domain.CalDAVHost / CardDAVHost, this repoints the
// SECURE record at that host (Thunderbird 155+, Apple Mail follow it straight
// to e.g. Nextcloud) and DELETES the insecure :80 record — an external DAV
// server is HTTPS-only, and advertising a plaintext endpoint we don't control
// is wrong. Clearing the override restores both defaults. Fully owns the four
// rows so the round-trip (set → clear) converges either way; idempotent, only
// writes on drift. Runs after reconcileMailProviderRecords, which owns the
// rest of the mail DNS.
func (r *Reconciler) reconcileDAVOverrideRecords(ctx context.Context, zone *models.DNSZone, domain *models.Domain, srv *models.ServerSettings) {
	if r.dnsRecords == nil || zone == nil || domain == nil {
		return
	}
	// The m6 DAV rows only exist for jabali-hosted mail. External / none
	// providers have them pruned; don't synthesise DAV SRVs there.
	if !domain.EmailEnabled || (domain.MailProvider != "" && domain.MailProvider != models.MailProviderJabali) {
		return
	}

	existing, err := r.dnsRecords.ListByZoneID(ctx, zone.ID)
	if err != nil {
		r.log.Warn("dav override: list records", "zone", zone.Name, "err", err)
		return
	}
	mailTarget := "mail." + strings.TrimSuffix(zone.Name, ".")
	r.convergeDAVService(ctx, zone, existing, "_caldavs._tcp", "_caldav._tcp", domain.CalDAVHost, mailTarget)
	r.convergeDAVService(ctx, zone, existing, "_carddavs._tcp", "_carddav._tcp", domain.CardDAVHost, mailTarget)
}

// convergeDAVService owns the secure (:443) and insecure (:80) SRV rows for one
// DAV service. override empty → both point at mailTarget (443 / 80); override
// set → secure points at the override host, insecure is removed.
func (r *Reconciler) convergeDAVService(ctx context.Context, zone *models.DNSZone, existing []models.DNSRecord, secureName, insecureName, override, mailTarget string) {
	var secureRow, insecureRow *models.DNSRecord
	for i := range existing {
		e := existing[i]
		if managedBy(e) != dnscompile.EmailRecordsManagedBy || e.Type != "SRV" {
			continue
		}
		switch e.Name {
		case secureName:
			secureRow = &existing[i]
		case insecureName:
			insecureRow = &existing[i]
		}
	}

	if override != "" {
		host, port := splitDAVHostPort(override, 443)
		wantSecure := "1 " + strconv.Itoa(port) + " " + host
		r.upsertDAVRow(ctx, zone.ID, secureRow, secureName, wantSecure)
		// External DAV is HTTPS-only — drop the plaintext :80 record.
		if insecureRow != nil {
			if err := r.dnsRecords.Delete(ctx, insecureRow.ID); err != nil {
				r.log.Warn("dav override: delete insecure SRV", "name", insecureName, "err", err)
			}
		}
		return
	}

	// No override — restore the mail.<domain> defaults for both.
	r.upsertDAVRow(ctx, zone.ID, secureRow, secureName, "1 443 "+mailTarget)
	r.upsertDAVRow(ctx, zone.ID, insecureRow, insecureName, "1 80 "+mailTarget)
}

// upsertDAVRow creates the m6-managed SRV row when absent, or updates its
// content when it drifts. No-op when already correct.
func (r *Reconciler) upsertDAVRow(ctx context.Context, zoneID string, row *models.DNSRecord, name, content string) {
	if row != nil {
		if row.Content == content {
			return
		}
		row.Content = content
		row.UpdatedAt = time.Now().UTC()
		if err := r.dnsRecords.Update(ctx, row); err != nil {
			r.log.Warn("dav override: update SRV", "name", name, "err", err)
		}
		return
	}
	m6 := dnscompile.EmailRecordsManagedBy
	rec := &models.DNSRecord{
		ID: ids.NewULID(), ZoneID: zoneID, Name: name, Type: "SRV", Content: content,
		TTL: 3600, Priority: 0, Managed: true, ManagedBy: &m6, IsEnabled: true,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := r.dnsRecords.Create(ctx, rec); err != nil {
		r.log.Warn("dav override: create SRV", "name", name, "err", err)
	}
}

// splitDAVHostPort parses a validated "host" or "host:port" override into
// (host, port), defaulting the port. Input is API-validated (davHostRe), so
// this only has to separate a trailing :port; a malformed value can't reach it.
func splitDAVHostPort(override string, defPort int) (string, int) {
	override = strings.TrimSpace(override)
	if i := strings.LastIndexByte(override, ':'); i > 0 {
		if p, err := strconv.Atoi(override[i+1:]); err == nil && p > 0 && p <= 65535 {
			return override[:i], p
		}
	}
	return override, defPort
}
