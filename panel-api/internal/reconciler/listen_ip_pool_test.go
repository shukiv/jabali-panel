package reconciler

import (
	"context"
	"slices"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// A domain bound to a secondary IP from the pool (Listen IP) resolves to that
// IP, not to server_settings.public_ipv4. It is still served here, so it is
// not CDN-fronted: it redirects :80→:443 and takes HTTP-01.

const poolTestIP = "198.51.100.20"

func poolIPs(primary string) *fakeManagedIPs {
	return &fakeManagedIPs{rows: []models.ManagedIP{
		{ID: 1, Address: primary, Family: "ipv4", IsDefault: true, IsBound: true},
		{ID: 2, Address: poolTestIP, Family: "ipv4", IsBound: true},
		{ID: 3, Address: "2001:DB8::20", Family: "ipv6", IsBound: true},
	}}
}

func TestOurAddrs_IncludesThePool(t *testing.T) {
	r, _, _, _ := frontedVhostFixture(t, letsEncryptCertPath, letsEncryptKeyPath, nil, true)
	r.WithManagedIPs(poolIPs(frontedTestOwnIP))
	srv, _ := r.settingsGet(context.Background())

	got := r.ourAddrs(context.Background(), srv)
	for _, want := range []string{frontedTestOwnIP, poolTestIP, "2001:db8::20"} {
		if !slices.Contains(got, want) {
			t.Errorf("ourAddrs = %v, missing %s", got, want)
		}
	}
}

func TestPoolIPVhost_IssuedRedirectsAndServes(t *testing.T) {
	r, ag, dom, sc := frontedVhostFixture(t, letsEncryptCertPath, letsEncryptKeyPath, []string{poolTestIP}, true)
	r.WithManagedIPs(poolIPs(frontedTestOwnIP))
	sc.byDomain[dom.ID].Status = models.SSLStatusIssued

	r.createDomainOnAgent(context.Background(), dom, true)

	if redirect, serve := vhostHTTPSParams(t, ag); !redirect || !serve {
		t.Errorf("apex on a pool IP: (redirect=%v, serve=%v), want (true, true) — it is served here, not fronted", redirect, serve)
	}
}

func TestPoolIPVhost_CDNStillFronted(t *testing.T) {
	r, ag, dom, sc := frontedVhostFixture(t, letsEncryptCertPath, letsEncryptKeyPath, cfEdgeAddrs, true)
	r.WithManagedIPs(poolIPs(frontedTestOwnIP))
	sc.byDomain[dom.ID].Status = models.SSLStatusIssued

	r.createDomainOnAgent(context.Background(), dom, true)

	if redirect, serve := vhostHTTPSParams(t, ag); redirect || !serve {
		t.Errorf("apex on a CDN with a pool wired: (redirect=%v, serve=%v), want (false, true)", redirect, serve)
	}
}

func TestPoolIP_IssuesViaHTTP01(t *testing.T) {
	r, ag, _, dom := dns01Fixture(t, []string{"kip.ns.cloudflare.com"}, "zone123")
	r.WithManagedIPs(poolIPs(dns01TestOwnIP))
	r.dnsPreflight = func(context.Context, string) ([]string, bool) {
		return []string{poolTestIP}, true
	}

	r.reconcileSSLForDomain(context.Background(), dom)

	if !agentCalled(ag, "ssl.issue") || agentCalled(ag, "ssl.issue_dns01") {
		t.Fatalf("apex on a pool IP: ssl.issue=%v ssl.issue_dns01=%v, want HTTP-01 only",
			agentCalled(ag, "ssl.issue"), agentCalled(ag, "ssl.issue_dns01"))
	}
}

func TestPoolIP_RestoredCertHandedBack(t *testing.T) {
	r, ag, _, dom := restoredFixture(t, 15*24*time.Hour, models.SSLIssueMethodRestored, poolTestIP)
	r.WithManagedIPs(poolIPs(restoredTestOwnIP))

	r.reconcileSSLForDomain(context.Background(), dom)

	if !agentCalled(ag, "ssl.issue") {
		t.Fatal("a restored cert in its renewal window, on a pool IP, must go back to Let's Encrypt over HTTP-01")
	}
}
