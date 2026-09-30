package reconciler

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1820: a box with the DNS module off (server_settings.dns_enabled=0) has
// no PowerDNS and no recursor. The reconciler still keeps the zone rows in the
// panel DB, but must not push zones or recursor forwards to the agent: on the
// reporter's box that was two failed agent calls per domain per minute
// ("powerdns backend not available", "controlsocket missing").
func dnsModuleFixture(t *testing.T, dnsEnabled bool, withDomain bool) (*Reconciler, *fakeAgent, *fakeDNSZoneRepo, *fakeServerSettingsRepo) {
	t.Helper()
	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: map[string]*models.Domain{}}
	userRepo := &fakeUserRepo{users: map[string]*models.User{}}
	username := "alice"
	userRepo.users["user-1"] = &models.User{ID: "user-1", Email: "alice@example.com", Username: &username}
	if withDomain {
		now := time.Now().UTC()
		domainRepo.domains["domain-1"] = &models.Domain{
			ID:        "domain-1",
			UserID:    "user-1",
			Name:      "example.com",
			DocRoot:   "/home/alice/domains/example.com/public_html",
			IsEnabled: true,
			CreatedAt: now,
			UpdatedAt: now,
		}
	}
	zones := &fakeDNSZoneRepo{zones: map[string]*models.DNSZone{}}
	settings := &fakeServerSettingsRepo{settings: &models.ServerSettings{
		Hostname:   "panel.example.com",
		PublicIPv4: "192.0.2.1",
		NS1Name:    "ns1.example.com",
		NS2Name:    "ns2.example.com",
		AdminEmail: "admin@example.com",
		DNSEnabled: dnsEnabled,
	}}
	r := New(domainRepo, userRepo, agent, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Interval: time.Second}).
		WithDNSRepos(zones, &fakeDNSRecordRepo{records: map[string]*models.DNSRecord{}}, settings)
	return r, agent, zones, settings
}

func TestReconcileAll_DNSModuleOff_KeepsZoneRowsButPushesNothing(t *testing.T) {
	r, agent, zones, _ := dnsModuleFixture(t, false, true)
	if err := r.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}

	for _, m := range []string{"dns.zone.upsert", "pdns.recursor_add_zone"} {
		if got := countMethod(agent, m); got != 0 {
			t.Errorf("DNS module off: %s calls = %d, want 0", m, got)
		}
	}
	if _, err := zones.FindByDomainID(context.Background(), "domain-1"); err != nil {
		t.Errorf("DNS module off must still keep the zone row in the panel DB: %v", err)
	}
	// The web side is unaffected.
	if got := countMethod(agent, "domain.create"); got != 1 {
		t.Errorf("domain.create calls = %d, want 1", got)
	}
}

func TestReconcileAll_DNSModuleTurnedOn_PushesOnTheNextRun(t *testing.T) {
	r, agent, _, settings := dnsModuleFixture(t, false, true)
	ctx := context.Background()
	if err := r.ReconcileAll(ctx); err != nil {
		t.Fatal(err)
	}
	if got := countMethod(agent, "dns.zone.upsert"); got != 0 {
		t.Fatalf("DNS module off: dns.zone.upsert calls = %d, want 0", got)
	}

	// Nothing was stamped while the module was off, so turning it on must push
	// at once rather than wait out the audit interval.
	settings.settings.DNSEnabled = true
	if err := r.ReconcileAll(ctx); err != nil {
		t.Fatal(err)
	}
	if got := countMethod(agent, "dns.zone.upsert"); got != 1 {
		t.Errorf("after DNS was turned on: dns.zone.upsert calls = %d, want 1", got)
	}
	if got := countMethod(agent, "pdns.recursor_add_zone"); got == 0 {
		t.Error("after DNS was turned on: expected recursor forwards")
	}
}

func TestReconcileAll_DNSModuleOff_NoRecursorRemoveForOrphan(t *testing.T) {
	// No DB domains: fakeAgent's domain.list reports example.com and
	// foo.bar.com, so both are orphans.
	r, agent, _, _ := dnsModuleFixture(t, false, false)
	if err := r.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := countMethod(agent, "pdns.recursor_remove_zone"); got != 0 {
		t.Errorf("DNS module off: pdns.recursor_remove_zone calls = %d, want 0", got)
	}
}
