package api

import (
	"context"
	"errors"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dnscompile"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// EnsureWWWRecord adds the `www CNAME <domain>` record that zone bootstrap
// would have added had www been on at create (GH #225), for a domain whose
// www was switched on later. One-shot, like bootstrap: it never re-creates a
// www the owner removed afterwards. It adds nothing (created=false, nil
// error) for a web-off domain, a www.-prefixed name, a domain without a zone
// served here (DNS hosted elsewhere, or a disabled zone), or a zone that
// already has any www record — a CNAME beside a www A/AAAA is invalid DNS.
// Shared by the PATCH handler and `jabali domain set --www`.
func EnsureWWWRecord(ctx context.Context, zones repository.DNSZoneRepository, records repository.DNSRecordRepository, settings repository.ServerSettingsRepository, d *models.Domain) (bool, error) {
	if zones == nil || records == nil || settings == nil || d == nil || d.WebDisabled || strings.HasPrefix(d.Name, "www.") {
		return false, nil
	}
	zone, err := zones.FindByDomainID(ctx, d.ID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	if dnsZoneNotServedReason(d, zone) != "" {
		return false, nil
	}
	existing, err := records.ListByZoneID(ctx, zone.ID)
	if err != nil {
		return false, err
	}
	for _, r := range existing {
		if isWWWRecordName(r.Name, zone.Name) {
			return false, nil
		}
	}
	srv, err := settings.Get(ctx)
	if err != nil {
		return false, err
	}
	for _, r := range dnscompile.BootstrapRecords(zone.ID, zone.Name, srv, ids.NewULID, false, false, true) {
		if r.Name == "www" && r.Type == "CNAME" {
			rec := r
			if err := records.Create(ctx, &rec); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	return false, nil
}

// isWWWRecordName matches a stored record name for www in this zone, written
// as the short label or the full name, any case, with or without the dot.
func isWWWRecordName(name, zone string) bool {
	n := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	return n == "www" || n == "www."+strings.TrimSuffix(strings.ToLower(zone), ".")
}
