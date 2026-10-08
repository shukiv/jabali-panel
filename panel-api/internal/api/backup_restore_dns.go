package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// backup_restore_dns.go — GH #1993. No restore brought back a domain's custom
// DNS records: Apply rebuilds the domain row, the reconciler later creates its
// zone with only the panel's own records, and the records the backup carried
// were dropped. RestoreBundleDNS adds them once the zone exists, through the
// checks a record created in the panel goes through.

// DomainScheduler asks the reconciler to converge a domain soon
// (*reconciler.Reconciler).
type DomainScheduler interface {
	Schedule(domainID string)
}

// RestoreDNSDeps is what RestoreBundleDNS reads and writes. Settings may be
// nil (defaults then apply).
type RestoreDNSDeps struct {
	Domains  repository.DomainRepository
	Zones    repository.DNSZoneRepository
	Records  repository.DNSRecordRepository
	Settings repository.ServerSettingsRepository
	// Scheduler, when set, has the reconciler create a restored domain's zone
	// now and republish it once the records are in. Nil: the periodic pass
	// does both (within a minute).
	Scheduler DomainScheduler
	// Untrusted: the bundle came from an uploaded file. Its records then
	// follow the account's record-type policy, as a record the account
	// created would; from this server's own backup they are restored as an
	// admin create is.
	Untrusted bool
}

var (
	// restoreDNSZoneWait bounds how long a restore waits, across all its
	// domains, for the reconciler to create their zones.
	restoreDNSZoneWait = 3 * time.Minute
	restoreDNSPoll     = 2 * time.Second
)

// restoreDNSDomain is one of the account's domains with records to restore.
type restoreDNSDomain struct {
	row     *models.Domain
	records []internalbackup.MetadataDNSRecord
	// seen: the zone was found once already; the next look applies the
	// records (the reconciler adds the zone's own records in the same pass
	// that creates it, and the conflict checks must see them).
	seen bool
	// mtasts: the restore turned the domain's MTA-STS on, so its records are
	// published too.
	mtasts bool
}

// RestoreBundleDNS restores the custom DNS records metaRaw carries for the
// domains of accountID it names. Only the account's own domains are touched;
// one Apply refused is not the account's and is skipped (Apply reported it).
// Records already in the zone are left alone (an exact duplicate counts as
// already there); nothing is removed. A domain whose MTA-STS the backup had
// on, and which has it on here (Apply turns it on when the restore runs
// this), gets its MTA-STS records too. It returns a line per domain restored
// and one per record or domain it could not restore.
func RestoreBundleDNS(ctx context.Context, d RestoreDNSDeps, metaRaw json.RawMessage, accountID string) (applied, warnings []string) {
	if len(metaRaw) == 0 || d.Domains == nil || d.Zones == nil || d.Records == nil {
		return nil, nil
	}
	var meta internalbackup.AccountMetadata
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		return nil, nil // Apply already reported an unreadable bundle
	}
	var pending []*restoreDNSDomain
	for _, dm := range meta.Domains {
		if len(dm.DNSRecords) == 0 && !dm.MTASTSEnabled {
			continue
		}
		row, err := d.Domains.FindByName(ctx, dm.Name)
		if err != nil || row == nil || row.UserID != accountID {
			continue
		}
		mtasts := dm.MTASTSEnabled && row.MTASTSEnabled
		if len(dm.DNSRecords) == 0 && !mtasts {
			continue
		}
		// Apply leaves MTA-STS off on such a domain and says why; the lines
		// here are for the records.
		switch {
		case row.DNSDisabled:
			if len(dm.DNSRecords) > 0 {
				warnings = append(warnings, fmt.Sprintf("dns %s: %d records not restored: DNS for this domain is hosted elsewhere", row.Name, len(dm.DNSRecords)))
			}
			continue
		case !domainops.OwnershipVerified(row):
			if len(dm.DNSRecords) > 0 {
				warnings = append(warnings, fmt.Sprintf("dns %s: %d records not restored: the domain's ownership isn't verified yet, so it has no DNS zone; add them under DNS once it is", row.Name, len(dm.DNSRecords)))
			}
			continue
		}
		pd := &restoreDNSDomain{row: row, records: dm.DNSRecords, mtasts: mtasts}
		if _, err := d.Zones.FindByDomainID(ctx, row.ID); err == nil {
			pd.seen = true // the zone was there before the restore: no wait
		} else if d.Scheduler != nil {
			d.Scheduler.Schedule(row.ID)
		}
		pending = append(pending, pd)
	}
	if len(pending) == 0 {
		return applied, warnings
	}

	var srv *models.ServerSettings
	if d.Settings != nil {
		srv, _ = d.Settings.Get(ctx)
	}
	deadline := time.Now().Add(restoreDNSZoneWait)
	for len(pending) > 0 {
		var still []*restoreDNSDomain
		for _, pd := range pending {
			zone, err := d.Zones.FindByDomainID(ctx, pd.row.ID)
			if err != nil || zone == nil {
				still = append(still, pd)
				continue
			}
			if !pd.seen {
				pd.seen = true
				still = append(still, pd)
				continue
			}
			if len(pd.records) > 0 {
				a, w := restoreZoneRecords(ctx, d, srv, pd.row, zone, pd.records)
				applied = append(applied, a...)
				warnings = append(warnings, w...)
			}
			if pd.mtasts {
				a, w := publishRestoredMTASts(ctx, d, srv, pd.row, zone)
				applied = append(applied, a...)
				warnings = append(warnings, w...)
			}
		}
		pending = still
		if len(pending) == 0 || time.Now().After(deadline) || ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(restoreDNSPoll):
		}
	}
	for _, pd := range pending {
		if len(pd.records) > 0 {
			warnings = append(warnings, fmt.Sprintf("dns %s: %d records not restored: its DNS zone wasn't created in time; add them under DNS", pd.row.Name, len(pd.records)))
		}
		if pd.mtasts {
			warnings = append(warnings, fmt.Sprintf("dns %s: MTA-STS records not published: its DNS zone wasn't created in time; %s", pd.row.Name, mtaStsRetry))
		}
	}
	return applied, warnings
}

// mtaStsRetry tells the admin how to publish a domain's MTA-STS records the
// restore couldn't: the toggle publishes them.
const mtaStsRetry = "turn MTA-STS off and on again in the domain's mail settings"

// publishRestoredMTASts publishes the MTA-STS records of a domain the restore
// turned MTA-STS on for, once its zone exists.
func publishRestoredMTASts(ctx context.Context, d RestoreDNSDeps, srv *models.ServerSettings, domain *models.Domain, zone *models.DNSZone) (applied, warnings []string) {
	if reason := dnsZoneNotServedReason(domain, zone); reason != "" {
		return nil, []string{fmt.Sprintf("dns %s: MTA-STS records not published: %s", domain.Name, reason)}
	}
	if srv == nil {
		return nil, []string{fmt.Sprintf("dns %s: MTA-STS records not published: the server settings couldn't be read; %s", domain.Name, mtaStsRetry)}
	}
	wrote, err := PublishMTAStsRecords(ctx, d.Records, zone, srv, domain)
	if err != nil {
		return nil, []string{fmt.Sprintf("dns %s: MTA-STS records not published: %v; %s", domain.Name, err, mtaStsRetry)}
	}
	if !wrote {
		return []string{fmt.Sprintf("dns → %s: MTA-STS records already there", domain.Name)}, nil
	}
	if d.Scheduler != nil {
		d.Scheduler.Schedule(domain.ID)
	}
	return []string{fmt.Sprintf("dns → %s: MTA-STS records published", domain.Name)}, nil
}

// restoreZoneRecords adds records to zone (domain's), each through the
// panel's create checks.
func restoreZoneRecords(ctx context.Context, d RestoreDNSDeps, srv *models.ServerSettings, domain *models.Domain, zone *models.DNSZone, records []internalbackup.MetadataDNSRecord) (applied, warnings []string) {
	if reason := dnsZoneNotServedReason(domain, zone); reason != "" {
		return nil, []string{fmt.Sprintf("dns %s: %d records not restored: %s", domain.Name, len(records), reason)}
	}
	policy := models.DefaultDNSUserRecordPolicy()
	ttl := 300
	var ownIPs = map[string]bool{}
	if srv != nil {
		if len(srv.DNSUserRecordPolicy) > 0 {
			policy = srv.DNSUserRecordPolicy
		}
		if srv.DefaultDNSTTL > 0 {
			ttl = int(srv.DefaultDNSTTL)
		}
		for _, ip := range []string{srv.PublicIPv4, srv.PublicIPv6} {
			if ip = strings.TrimSpace(ip); ip != "" {
				ownIPs[ip] = true
			}
		}
	}
	// The names where this server publishes its own A/AAAA (apex, www, mail):
	// a restored address record there would be served alongside it — for a
	// backup from another server, its old address.
	ownAddr := map[string]bool{}
	if existing, err := d.Records.ListByZoneID(ctx, zone.ID); err == nil {
		for _, e := range existing {
			if e.Managed && (e.Type == "A" || e.Type == "AAAA") {
				ownAddr[strings.ToLower(strings.TrimSpace(e.Name))+" "+e.Type] = true
			}
		}
	}

	restored, already := 0, 0
	var elsewhere []string
	for _, rec := range records {
		r := &models.DNSRecord{
			ID: ids.NewULID(), ZoneID: zone.ID,
			Name: rec.Name, Type: rec.Type, Content: rec.Content,
			TTL: rec.TTL, Priority: rec.Priority,
			Managed: false, IsEnabled: rec.IsEnabled,
		}
		if r.TTL <= 0 {
			r.TTL = ttl
		}
		NormaliseSRVRecord(r, nil)
		label := strings.TrimSpace(rec.Name) + " " + strings.ToUpper(strings.TrimSpace(rec.Type))
		if err := ValidateDNSRecord(r); err != nil {
			warnings = append(warnings, fmt.Sprintf("dns %s: record %s not restored: %v", domain.Name, label, err))
			continue
		}
		if d.Untrusted && !policy.Allows(r.Type, "create") {
			warnings = append(warnings, fmt.Sprintf("dns %s: record %s not restored: accounts on this server can't create %s records", domain.Name, label, r.Type))
			continue
		}
		if (r.Type == "A" || r.Type == "AAAA") && ownAddr[strings.ToLower(r.Name)+" "+r.Type] && !ownIPs[r.Content] {
			warnings = append(warnings, fmt.Sprintf("dns %s: record %s not restored: this server publishes its own %s record at %s", domain.Name, label, r.Type, r.Name))
			continue
		}
		if err := CheckDNSRecordConflict(ctx, d.Records, zone.ID, r, ""); err != nil {
			if strings.HasPrefix(err.Error(), "duplicate record") {
				already++
				continue
			}
			warnings = append(warnings, fmt.Sprintf("dns %s: record %s not restored: %v", domain.Name, label, err))
			continue
		}
		if err := d.Records.Create(ctx, r); err != nil {
			if errors.Is(err, repository.ErrConflict) {
				already++
				continue
			}
			warnings = append(warnings, fmt.Sprintf("dns %s: record %s not restored: %v", domain.Name, label, err))
			continue
		}
		restored++
		if d.Untrusted && (r.Type == "A" || r.Type == "AAAA") && !ownIPs[r.Content] {
			elsewhere = append(elsewhere, r.Name+" "+r.Type+" "+r.Content)
		}
	}
	if restored > 0 && d.Scheduler != nil {
		d.Scheduler.Schedule(domain.ID)
	}
	line := fmt.Sprintf("dns → %s (%d records restored", domain.Name, restored)
	if already > 0 {
		line += fmt.Sprintf(", %d already there", already)
	}
	applied = append(applied, line+")")
	if len(elsewhere) > 0 {
		warnings = append(warnings, fmt.Sprintf("dns %s: check that these records still point where you want; they don't point at this server: %s", domain.Name, strings.Join(elsewhere, ", ")))
	}
	return applied, warnings
}

// restoreDNSDeps wires RestoreBundleDNS from the handler's config.
func (h *backupHandler) restoreDNSDeps(untrusted bool) RestoreDNSDeps {
	return RestoreDNSDeps{
		Domains: h.cfg.Domains, Zones: h.cfg.DNSZones, Records: h.cfg.DNSRecords,
		Settings: h.cfg.ServerSettings, Scheduler: h.cfg.Scheduler, Untrusted: untrusted,
	}
}

// metadataUserID is the user id a metadata bundle names, or "".
func metadataUserID(metaRaw json.RawMessage) string {
	var m struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if json.Unmarshal(metaRaw, &m) != nil {
		return ""
	}
	return m.User.ID
}
