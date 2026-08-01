package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dnscompile"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// `jabali dns acme-hook auth|cleanup` — certbot manual DNS-01 hooks.
//
// certbot invokes these (as root, via the agent's ssl.issue_dns01) with
// CERTBOT_DOMAIN and CERTBOT_VALIDATION in the environment, once per
// requested name. auth writes the _acme-challenge TXT record through the
// panel's dns_records table and pushes the zone to PowerDNS immediately;
// cleanup removes every row this subsystem owns for that name and pushes
// again. Going through the panel tables (rather than poking the pdns DB)
// keeps the DB-as-truth invariant: a reconciler tick between hook and
// validation re-pushes the SAME record set instead of wiping the
// challenge.
//
// Rows are tagged managed_by=acmeDNS01ManagedBy so cleanup can never
// touch operator records, and a crashed run's leftovers are identifiable.
const acmeDNS01ManagedBy = "acme-dns01"

// acmeChallengeTTL is deliberately tiny — the record only needs to exist
// for the seconds between hook and LE validation.
const acmeChallengeTTL = 60

func newDNSAcmeHookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "acme-hook",
		Short:  "certbot manual DNS-01 hooks (writes _acme-challenge TXT via panel DNS)",
		Hidden: true, // machine-invoked by certbot, not an operator surface
	}
	mk := func(use string, fn func(ctx context.Context, domain, validation string) error) *cobra.Command {
		return &cobra.Command{
			Use:     use,
			Args:    cobra.NoArgs,
			PreRunE: requireDBAndAgent,
			RunE: func(cmd *cobra.Command, _ []string) error {
				domain := strings.TrimPrefix(strings.TrimSpace(os.Getenv("CERTBOT_DOMAIN")), "*.")
				validation := strings.TrimSpace(os.Getenv("CERTBOT_VALIDATION"))
				if domain == "" || validation == "" {
					return fmt.Errorf("CERTBOT_DOMAIN and CERTBOT_VALIDATION must be set (certbot invokes this hook)")
				}
				ctx, cancel := context.WithTimeout(cmd.Context(), 90*time.Second)
				defer cancel()
				return fn(ctx, domain, validation)
			},
		}
	}
	cmd.AddCommand(
		mk("auth", acmeHookAuth),
		mk("cleanup", acmeHookCleanup),
	)
	return cmd
}

// findZoneForName walks the name's parent chain until a local dns_zones
// row matches: "sub.example.com" tries itself, then "example.com", …
// Refusing names with no local zone makes a DNS-01 attempt against a
// foreign-DNS domain fail fast with a clear message instead of certbot
// timing out on a TXT record that never appears.
func findZoneForName(ctx context.Context, zones repository.DNSZoneRepository, name string) (*models.DNSZone, error) {
	labels := strings.Split(strings.TrimSuffix(name, "."), ".")
	for i := 0; i < len(labels)-1; i++ {
		candidate := strings.Join(labels[i:], ".")
		z, err := zones.FindByName(ctx, candidate)
		if err == nil {
			return z, nil
		}
		if !isNotFound(err) {
			return nil, fmt.Errorf("look up zone %s: %w", candidate, err)
		}
	}
	return nil, fmt.Errorf("no local DNS zone serves %q — DNS-01 needs the domain's DNS hosted on this panel", name)
}

func isNotFound(err error) bool {
	return errors.Is(err, repository.ErrNotFound)
}

// acmeChallengeName returns the FQDN record name for a validated domain.
// Stored fully-qualified; dnscompile.expandName passes FQDNs through.
func acmeChallengeName(domain string) string {
	return "_acme-challenge." + domain
}

func acmeHookAuth(ctx context.Context, domain, validation string) error {
	zones := dnsZoneRepoFromDB()
	records := dnsRecordRepoFromDB()

	zone, err := findZoneForName(ctx, zones, domain)
	if err != nil {
		return err
	}

	managedBy := acmeDNS01ManagedBy
	now := time.Now().UTC()
	rec := &models.DNSRecord{
		ID:     ids.NewULID(),
		ZoneID: zone.ID,
		Name:   acmeChallengeName(domain),
		Type:   "TXT",
		// pdns wants TXT content quoted — same convention as the DMARC
		// compiler (BuildDMARCString).
		Content:   fmt.Sprintf("%q", validation),
		TTL:       acmeChallengeTTL,
		Managed:   true,
		ManagedBy: &managedBy,
		IsEnabled: true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := records.Create(ctx, rec); err != nil {
		return fmt.Errorf("create challenge record: %w", err)
	}
	if err := pushZoneToPdns(ctx, zone); err != nil {
		return err
	}
	// Settle: PowerDNS serves straight from the backend after the upsert
	// (the agent purges its cache), but give LE's first query a moment —
	// a failed validation costs a rate-limited retry, a short sleep is free.
	time.Sleep(3 * time.Second)
	fmt.Printf("acme-hook: TXT %s set in zone %s\n", rec.Name, zone.Name)
	return nil
}

func acmeHookCleanup(ctx context.Context, domain, validation string) error {
	zones := dnsZoneRepoFromDB()
	records := dnsRecordRepoFromDB()

	zone, err := findZoneForName(ctx, zones, domain)
	if err != nil {
		return err
	}
	rows, err := records.ListByZoneID(ctx, zone.ID)
	if err != nil {
		return fmt.Errorf("list zone records: %w", err)
	}
	wantName := acmeChallengeName(domain)
	removed := 0
	for i := range rows {
		r := &rows[i]
		// Only rows this subsystem created, only for this name. Content
		// is deliberately NOT matched: certbot runs cleanup once per
		// auth, but a crashed earlier run may have stranded a different
		// validation value under the same name — reap those too.
		if r.Type != "TXT" || r.Name != wantName {
			continue
		}
		if r.ManagedBy == nil || *r.ManagedBy != acmeDNS01ManagedBy {
			continue
		}
		if err := records.Delete(ctx, r.ID); err != nil {
			return fmt.Errorf("delete challenge record %s: %w", r.ID, err)
		}
		removed++
	}
	if err := pushZoneToPdns(ctx, zone); err != nil {
		return err
	}
	fmt.Printf("acme-hook: removed %d challenge record(s) for %s\n", removed, wantName)
	return nil
}

// pushZoneToPdns compiles the zone from the panel tables and pushes it
// through the agent — the exact flow the reconciler's reconcileDNSZone
// runs, so the hook's push and the next tick's push are idempotent
// twins rather than two competing writers.
func pushZoneToPdns(ctx context.Context, zone *models.DNSZone) error {
	zones := dnsZoneRepoFromDB()
	records := dnsRecordRepoFromDB()
	srv, _ := repository.NewServerSettingsRepository(sharedDB).Get(ctx)

	rows, err := records.ListByZoneID(ctx, zone.ID)
	if err != nil {
		return fmt.Errorf("list records for push: %w", err)
	}
	compiled := dnscompile.Compile(zone, rows, srv)

	zone.Serial = time.Now().UTC().Unix()
	_ = zones.Update(ctx, zone)

	var allowAXFR, alsoNotify []string
	if srv != nil && srv.NS2IPv4 != "" {
		allowAXFR = []string{srv.NS2IPv4}
		alsoNotify = []string{srv.NS2IPv4}
	}
	allowAXFR = append(allowAXFR, "127.0.0.1")

	pushCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := sharedAgent.Call(pushCtx, "dns.zone.upsert", map[string]any{
		"zone":            zone.Name,
		"records":         compiled,
		"allow_axfr_from": allowAXFR,
		"also_notify":     alsoNotify,
	}); err != nil {
		return fmt.Errorf("dns.zone.upsert %s: %w", zone.Name, err)
	}
	return nil
}
