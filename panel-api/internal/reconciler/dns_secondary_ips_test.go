package reconciler

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #2131: the zone push gives the secondary DNS servers (dns_secondary_ips)
// the same AXFR permission and NOTIFY as ns2.
func TestReconcile_PassesSecondaryDNSServersToAgent(t *testing.T) {
	ctx := context.Background()
	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: make(map[string]*models.Domain)}
	userRepo := &fakeUserRepo{users: make(map[string]*models.User)}
	dnsZoneRepo := &fakeDNSZoneRepo{zones: make(map[string]*models.DNSZone)}
	dnsRecordRepo := &fakeDNSRecordRepo{records: make(map[string]*models.DNSRecord)}
	srv := &fakeServerSettingsRepo{settings: &models.ServerSettings{
		PublicIPv4:      "192.0.2.1",
		NS1Name:         "ns1.example.com",
		NS2Name:         "ns2.example.com",
		NS2IPv4:         "198.51.100.7",
		DNSSecondaryIPs: "192.0.2.53,2001:db8::53",
		AdminEmail:      "admin@example.com",
		DNSEnabled:      true,
	}}
	username := "alice"
	userRepo.users["user-1"] = &models.User{ID: "user-1", Email: "alice@example.com", Username: &username}
	now := time.Now().UTC()
	dom := &models.Domain{
		OwnershipState: verifiedOwnership,
		ID:             "domain-1", UserID: "user-1", Name: "example.com",
		DocRoot:   "/home/alice/domains/example.com/public_html",
		IsEnabled: true, CreatedAt: now, UpdatedAt: now,
	}
	domainRepo.domains[dom.ID] = dom

	r := New(domainRepo, userRepo, agent, slog.New(slog.NewTextHandler(os.Stderr, nil)), Config{Interval: time.Second}).
		WithDNSRepos(dnsZoneRepo, dnsRecordRepo, srv)
	require.NoError(t, r.ReconcileOne(ctx, dom.ID))

	var params map[string]any
	for _, c := range agent.calls {
		if c.method == "dns.zone.upsert" {
			params, _ = c.params.(map[string]any)
		}
	}
	require.NotNil(t, params, "dns.zone.upsert was not called")
	require.Equal(t, []string{"198.51.100.7", "192.0.2.53", "2001:db8::53", "127.0.0.1"}, params["allow_axfr_from"])
	require.Equal(t, []string{"198.51.100.7", "192.0.2.53", "2001:db8::53"}, params["also_notify"])
}
