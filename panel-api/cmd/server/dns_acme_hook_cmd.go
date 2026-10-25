package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/hostedsvc"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/dnsverify"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/cftoken"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dns01"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dnscompile"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/domainops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/freehostname"
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
	mk := func(use string, fn func(ctx context.Context, lineage, domain, validation string) error) *cobra.Command {
		return &cobra.Command{
			Use:     use,
			Args:    cobra.NoArgs,
			PreRunE: requireDBAndAgent,
			RunE:    acmeHookRunE(fn),
		}
	}
	cmd.AddCommand(
		mk("auth", acmeHookAuth),
		mk("cleanup", acmeHookCleanup),
	)
	return cmd
}

// acmeHookRunE runs fn with what certbot and the agent put in the
// environment.
func acmeHookRunE(fn func(ctx context.Context, lineage, domain, validation string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		in, err := acmeHookEnv(os.Getenv)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 90*time.Second)
		defer cancel()
		return fn(ctx, in.lineage, in.domain, in.validation)
	}
}

// acmeHookInput is what certbot and the agent hand the hook.
type acmeHookInput struct {
	// lineage is the certificate's --cert-name, which the agent prefixes
	// to the hook as JABALI_ACME_LINEAGE (GH #2032). Empty for a renewal
	// conf written before that: the server's Cloudflare token only, as then.
	lineage    string
	domain     string
	validation string
}

func acmeHookEnv(getenv func(string) string) (acmeHookInput, error) {
	in := acmeHookInput{
		lineage:    strings.TrimSpace(getenv("JABALI_ACME_LINEAGE")),
		domain:     strings.TrimPrefix(strings.TrimSpace(getenv("CERTBOT_DOMAIN")), "*."),
		validation: strings.TrimSpace(getenv("CERTBOT_VALIDATION")),
	}
	if in.domain == "" || in.validation == "" {
		return in, fmt.Errorf("CERTBOT_DOMAIN and CERTBOT_VALIDATION must be set (certbot invokes this hook)")
	}
	return in, nil
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

// Cloudflare propagation wait (JAB-235 follow-up). The first version slept a
// flat 10s after the API write — and lost a real race on hoff.co.il
// (2026-08-13): the apex TXT, written 23s before validation, passed; the www
// TXT, written 11s before, was NXDOMAIN at LE's resolver. CF's API→edge
// propagation is usually seconds but not bounded by any constant we can
// hardcode, and every lost race burns rate-limited LE quota (5 failed authz
// per hostname per hour). So instead of sleeping blind, poll the zone's OWN
// authoritative nameservers until the value is actually served, then add a
// short grace for cross-PoP skew (our anycast query and LE's may hit
// different edges).
//
// On timeout we proceed rather than fail: the record may still land before
// LE queries, and the old behaviour (fixed sleep, then hope) is the floor,
// not the ceiling.
const (
	cfChallengePollInterval = 3 * time.Second
	cfChallengePollBudget   = 60 * time.Second // hook ctx is 90s — leave room for the write + grace
	cfChallengeGrace        = 5 * time.Second
)

// waitForChallengeTXT polls each authoritative NS host for name's TXT RRset
// until one of them serves value. lookupNS/lookupTXT are injected for tests;
// production passes dnsverify.LookupNSExternal / dnsverify.LookupTXTOnServer.
// Returns true when the record was confirmed visible.
func waitForChallengeTXT(
	ctx context.Context,
	zone, name, value string,
	lookupNS func(ctx context.Context, name string) ([]string, bool),
	lookupTXT func(ctx context.Context, server, name string) ([]string, error),
	pollInterval, budget time.Duration,
) bool {
	hosts, queried := lookupNS(ctx, zone)
	if !queried || len(hosts) == 0 {
		return false // can't confirm — caller falls back to hoping
	}
	deadline := time.Now().Add(budget)
	for {
		for _, ns := range hosts {
			txts, err := lookupTXT(ctx, ns, name)
			if err != nil {
				continue
			}
			for _, txt := range txts {
				if txt == value {
					return true
				}
			}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(pollInterval):
		}
	}
}

// hookDNS01Setup builds the shared provider-decision config (JAB-235). The
// Cloudflare candidates are the tokens cftoken offers for domain on the
// lineage's certificate (GH #2032): the domain owner's own, then the
// server's. clients[i] is cfg.CF[i]. Without a readable token the decision
// degrades to pdns-or-nothing exactly as before Cloudflare support.
func hookDNS01Setup(ctx context.Context, lineage, domain string) (dns01.Config, []cftoken.Candidate, []*hostedsvc.CloudflareDNS) {
	cfg := dns01.Config{LookupNS: dnsverify.LookupNSExternal}
	srv, err := repository.NewServerSettingsRepository(sharedDB).Get(ctx)
	if err != nil || srv == nil {
		return cfg, nil, nil
	}
	cfg.OurNSHosts = []string{srv.NS1Name, srv.NS2Name}
	key := ssoKeyForCLI()
	if key == nil {
		return cfg, nil, nil
	}
	tokens := cftoken.Resolver{
		Domains: domainRepoFromDB(),
		Tokens:  repository.NewUserCloudflareTokenRepository(sharedDB),
		Key:     key,
	}
	set, err := tokens.For(ctx, srv, lineage, domain)
	if err != nil {
		fmt.Fprintf(os.Stderr, "acme-hook: %v — trying the Cloudflare tokens that could be read\n", err)
	}
	clients := make([]*hostedsvc.CloudflareDNS, 0, len(set.Candidates))
	for _, c := range set.Candidates {
		cf := hostedsvc.NewCloudflareAPI(c.Token)
		clients = append(clients, cf)
		cfg.CF = append(cfg.CF, cf)
	}
	return cfg, set.Candidates, clients
}

// hookRoute decides where this hook writes the challenge for domain.
// Cloudflare wins on a POSITIVE public-NS match even when a local zone row
// exists — the reconciler auto-creates a dns_zones row for every domain, so
// a migrated domain whose DNS really lives at Cloudflare still has a stale
// local row, and writing there would leave LE querying Cloudflare for a
// TXT that only exists in our pdns. Everything else (jabali-authoritative,
// inconclusive NS, resolver outage) keeps the pre-JAB-235 behaviour: use
// the local zone when there is one, else fail with a clear reason.
func hookRoute(ctx context.Context, zones repository.DNSZoneRepository, lineage, domain string) (dns01.Route, *hostedsvc.CloudflareDNS, *models.DNSZone, error) {
	cfg, cands, clients := hookDNS01Setup(ctx, lineage, domain)
	route := dns01.AuthoritativeRoute(ctx, cfg, domain)
	if route.Provider == dns01.ProviderCloudflare {
		cf, whose, err := routeClient(route, cands, clients)
		if err != nil {
			return route, nil, nil, fmt.Errorf("%s: %w", domain, err)
		}
		fmt.Printf("acme-hook: %s is at Cloudflare (zone %s) — writing with %s API token\n", domain, route.Zone, whose)
		return route, cf, nil, nil
	}
	zone, err := findZoneForName(ctx, zones, domain)
	if err != nil {
		if route.Reason != "" {
			return route, nil, nil, fmt.Errorf("%s (and no local DNS zone serves %q)", route.Reason, domain)
		}
		return route, nil, nil, err
	}
	return dns01.Route{Provider: dns01.ProviderPDNS, Zone: zone.Name}, nil, zone, nil
}

// routeClient returns the client whose token found the zone, and whose
// token it is for the log. clients[i] belongs to cands[i].
func routeClient(route dns01.Route, cands []cftoken.Candidate, clients []*hostedsvc.CloudflareDNS) (*hostedsvc.CloudflareDNS, string, error) {
	if route.CFIndex < 0 || route.CFIndex >= len(clients) || route.CFIndex >= len(cands) {
		return nil, "", fmt.Errorf("cloudflare route names token %d of %d", route.CFIndex, len(clients))
	}
	whose := "the server's"
	if cands[route.CFIndex].Source == cftoken.SourceOwner {
		whose = "the domain owner's"
	}
	return clients[route.CFIndex], whose, nil
}

// Free jabalihosted.com hostname (JAB-213). The hostname service publishes
// the box's records under jabalihosted.com and writes _acme-challenge.<label>
// for the box's own token, so the preview wildcard (*.<label>.jabalihosted.com)
// validates through it. Checked BEFORE hookRoute: a DNS-module box carries a
// local zone row for its own hostname (the panel hostname is also a domain),
// and Let's Encrypt never queries it — Cloudflare answers for
// jabalihosted.com. Vars so tests can stub the network.
var (
	freeHostnameHTTP      = freehostname.HTTPClient
	freeHostnameLookupNS  = dnsverify.LookupNSExternal
	freeHostnameLookupTXT = dnsverify.LookupTXTOnServer
	freeHostnameGrace     = cfChallengeGrace
)

// freeHostnameCredentialFor returns this box's free-hostname credential when
// domain IS its free hostname, nil otherwise (no credential, or another
// name). A broken credential file is an error only for a free-hostname name;
// every other domain carries on to the normal routes.
func freeHostnameCredentialFor(domain string) (*freehostname.Credential, error) {
	cred, err := freehostname.ReadCredential(freehostname.EnvPath)
	if errors.Is(err, freehostname.ErrNoCredential) {
		return nil, nil
	}
	if err != nil {
		if models.IsFreeHostnameBase(domain) {
			return nil, err
		}
		return nil, nil
	}
	if !strings.EqualFold(domain, cred.FQDN) {
		return nil, nil
	}
	return cred, nil
}

func freeHostnameAuth(ctx context.Context, cred *freehostname.Credential, domain, validation string) error {
	if err := cred.PresentChallenge(ctx, freeHostnameHTTP(), validation); err != nil {
		return fmt.Errorf("challenge write for %s: %w", domain, err)
	}
	name := acmeChallengeName(domain)
	if waitForChallengeTXT(ctx, models.FreeHostnameBase, name, validation,
		freeHostnameLookupNS, freeHostnameLookupTXT,
		cfChallengePollInterval, cfChallengePollBudget) {
		time.Sleep(freeHostnameGrace)
		fmt.Printf("acme-hook: TXT %s set through the free hostname service — confirmed on the %s nameservers\n", name, models.FreeHostnameBase)
	} else {
		fmt.Printf("acme-hook: TXT %s set through the free hostname service — NOT yet visible on the %s nameservers after %s; proceeding anyway\n",
			name, models.FreeHostnameBase, cfChallengePollBudget)
	}
	return nil
}

func freeHostnameCleanup(ctx context.Context, cred *freehostname.Credential, domain string) error {
	if err := cred.ClearChallenge(ctx, freeHostnameHTTP()); err != nil {
		return fmt.Errorf("challenge cleanup for %s: %w", domain, err)
	}
	fmt.Printf("acme-hook: challenge records for %s cleared through the free hostname service\n", acmeChallengeName(domain))
	return nil
}

func acmeHookAuth(ctx context.Context, lineage, domain, validation string) error {
	if cred, err := freeHostnameCredentialFor(domain); err != nil {
		return err
	} else if cred != nil {
		return freeHostnameAuth(ctx, cred, domain, validation)
	}
	zones := dnsZoneRepoFromDB()
	records := dnsRecordRepoFromDB()

	route, cf, zone, err := hookRoute(ctx, zones, lineage, domain)
	if err != nil {
		return err
	}
	if route.Provider == dns01.ProviderCloudflare {
		name := acmeChallengeName(domain)
		if err := cf.SetChallengeTXT(ctx, route.CFZoneID, name, validation); err != nil {
			return fmt.Errorf("cloudflare challenge write for %s: %w", domain, err)
		}
		if waitForChallengeTXT(ctx, route.Zone, name, validation,
			dnsverify.LookupNSExternal, dnsverify.LookupTXTOnServer,
			cfChallengePollInterval, cfChallengePollBudget) {
			time.Sleep(cfChallengeGrace)
			fmt.Printf("acme-hook: TXT %s set at Cloudflare (zone %s) — confirmed on the zone's nameservers\n", name, route.Zone)
		} else {
			fmt.Printf("acme-hook: TXT %s set at Cloudflare (zone %s) — NOT yet visible on the zone's nameservers after %s; proceeding anyway\n",
				name, route.Zone, cfChallengePollBudget)
		}
		return nil
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

func acmeHookCleanup(ctx context.Context, lineage, domain, validation string) error {
	if cred, err := freeHostnameCredentialFor(domain); err != nil {
		return err
	} else if cred != nil {
		return freeHostnameCleanup(ctx, cred, domain)
	}
	zones := dnsZoneRepoFromDB()
	records := dnsRecordRepoFromDB()

	route, cf, zone, err := hookRoute(ctx, zones, lineage, domain)
	if err != nil {
		return err
	}
	if route.Provider == dns01.ProviderCloudflare {
		name := acmeChallengeName(domain)
		// Clears ALL values at the name — mirrors the pdns path, which
		// also reaps a crashed earlier run's stranded validations.
		if err := cf.ClearChallengeTXT(ctx, route.CFZoneID, name); err != nil {
			return fmt.Errorf("cloudflare challenge cleanup for %s: %w", domain, err)
		}
		fmt.Printf("acme-hook: challenge records for %s cleared at Cloudflare (zone %s)\n", name, route.Zone)
		return nil
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
	if err := zonePushAllowed(ctx, domainRepoFromDB(), zone); err != nil {
		return err
	}
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

	// The same lists the reconciler's zone push sends: a push that left out
	// the secondaries would drop their ALLOW-AXFR-FROM/ALSO-NOTIFY metadata.
	allowAXFR, alsoNotify := dnscompile.TransferTargets(srv)

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

// zoneDomainFinder loads the domain that owns a zone.
type zoneDomainFinder interface {
	FindByID(ctx context.Context, id string) (*models.Domain, error)
}

// zonePushAllowed refuses to publish the zone of a domain whose owner has
// not proven the name (GH #1816 / ADR-0170): the reconciler keeps that zone
// off the authoritative server, and a DNS-01 challenge must not put it back.
// A lookup failure refuses too (fail closed). A zone with no owning domain
// row (none today) is not gated.
func zonePushAllowed(ctx context.Context, domains zoneDomainFinder, zone *models.DNSZone) error {
	if zone == nil || zone.DomainID == "" {
		return nil
	}
	d, err := domains.FindByID(ctx, zone.DomainID)
	if err != nil {
		return fmt.Errorf("zone %s: load owning domain: %w", zone.Name, err)
	}
	if !domainops.OwnershipVerified(d) {
		return fmt.Errorf("zone %s: the domain's ownership is not proven yet; its zone is not published", zone.Name)
	}
	return nil
}
