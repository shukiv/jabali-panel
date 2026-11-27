package reconciler

import (
	"context"
	"encoding/json"
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

type failingPoolIPs struct{ *fakeManagedIPs }

func (failingPoolIPs) ListAll(context.Context) ([]models.ManagedIP, error) {
	return nil, errBoom
}

// nil keeps each caller's "can't tell" path (fail open to HTTP-01, keep the
// cached fronted value) instead of judging with a partial address list.
func TestOurAddrs_UnknownStaysUnknown(t *testing.T) {
	ctx := context.Background()
	r, _, _, _ := frontedVhostFixture(t, letsEncryptCertPath, letsEncryptKeyPath, nil, true)

	r.WithManagedIPs(poolIPs(frontedTestOwnIP))
	if got := r.ourAddrs(ctx, &models.ServerSettings{}); got != nil {
		t.Errorf("public address unknown, pool wired: ourAddrs = %v, want nil", got)
	}
	r.WithManagedIPs(failingPoolIPs{poolIPs(frontedTestOwnIP)})
	if got := r.ourAddrs(ctx, &models.ServerSettings{PublicIPv4: frontedTestOwnIP}); got != nil {
		t.Errorf("pool read failed: ourAddrs = %v, want nil", got)
	}
}

func TestPoolIP_UnknownPublicAddressFailsOpenToHTTP01(t *testing.T) {
	r, ag, _, dom := dns01Fixture(t, []string{"kip.ns.cloudflare.com"}, "zone123")
	r.WithManagedIPs(poolIPs(dns01TestOwnIP))
	r.serverSettings.(*fakeServerSettingsRepo).settings.PublicIPv4 = ""

	r.reconcileSSLForDomain(context.Background(), dom)

	if !agentCalled(ag, "ssl.issue") {
		t.Fatal("without a known public IP the fronted judgement cannot be made, pool or not — must fail open to HTTP-01")
	}
}

// A certificate the old check sent through DNS-01 keeps its dns-01 marker, so
// the vhost still skips the redirect. A renewal (the panel's Renew button)
// re-routes it: HTTP-01 now, and the marker follows.
func TestPoolIP_DNS01LineageRenewMovesToHTTP01(t *testing.T) {
	r, ag, sc, dom := dns01Fixture(t, []string{"kip.ns.cloudflare.com"}, "zone123")
	r.WithManagedIPs(poolIPs(dns01TestOwnIP))
	r.dnsPreflight = func(context.Context, string) ([]string, bool) {
		return []string{poolTestIP}, true
	}
	cert := sc.byDomain[dom.ID]
	cert.Status = models.SSLStatusRenewing
	cert.IssueMethod = issueMethodDNS01
	sc.issueMethods = map[string]string{}
	delete(ag.errByMethod, "ssl.issue") // the fixture's nil entry would shadow the result
	ag.resultByMethod["ssl.issue"] = json.RawMessage(`{
		"cert_path": "/etc/letsencrypt/live/sub.example.com/fullchain.pem",
		"key_path":  "/etc/letsencrypt/live/sub.example.com/privkey.pem",
		"issued_at": "2026-11-26T00:00:00Z",
		"expires_at": "2027-02-24T00:00:00Z"
	}`)

	r.reconcileSSLForDomain(context.Background(), dom)

	if !agentCalled(ag, "ssl.issue") || agentCalled(ag, "ssl.issue_dns01") {
		t.Fatalf("renewing a dns-01 lineage on a pool IP: ssl.issue=%v ssl.issue_dns01=%v, want HTTP-01 only",
			agentCalled(ag, "ssl.issue"), agentCalled(ag, "ssl.issue_dns01"))
	}
	if got := sc.issueMethods[cert.ID]; got != issueMethodHTTP01 {
		t.Errorf("issue method after renewal = %q, want %q (the vhost redirect keys off it)", got, issueMethodHTTP01)
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
