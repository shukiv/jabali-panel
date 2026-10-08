package reconciler

import (
	"context"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1993: a certificate a restore installed from a backup isn't certbot's,
// so certbot's timer never renews it. In its renewal window the reconciler
// hands the domain to Let's Encrypt once a challenge can pass here: the name
// resolves to this server, or this server can write its DNS-01 record. Until
// then, and before the window, it keeps serving: issuing clears the restored
// files first, and a failed attempt leaves a placeholder. An expired one goes
// the way a new domain's certificate does.

const restoredTestOwnIP = "203.0.113.5"

func restoredFixture(t *testing.T, left time.Duration, method string, resolvesTo ...string) (*Reconciler, *fakeAgent, *fakeSSLCertRepo, *models.Domain) {
	t.Helper()
	r, ag, sc, dom := sslWebrootFixture(t, nil)
	r.serverSettings.(*fakeServerSettingsRepo).settings.PublicIPv4 = restoredTestOwnIP
	cp, kp := "/etc/letsencrypt/live/sub.example.com/fullchain.pem", "/etc/letsencrypt/live/sub.example.com/privkey.pem"
	exp := time.Now().UTC().Add(left)
	sc.byDomain[dom.ID] = &models.SSLCertificate{
		ID: "c1", DomainID: dom.ID, Status: models.SSLStatusIssued, IssueMethod: method,
		CertPath: &cp, KeyPath: &kp, ExpiresAt: &exp,
	}
	r.dnsPreflight = func(context.Context, string) ([]string, bool) {
		return resolvesTo, true
	}
	// The zone is delegated to another provider: no DNS-01 route.
	r.dns01LookupNS = func(_ context.Context, name string) ([]string, bool) {
		if name == "example.com" {
			return []string{"ns1.oldhost.net"}, true
		}
		return nil, true
	}
	return r, ag, sc, dom
}

// withDNS01Route delegates the zone to Cloudflare under a stored token.
func withDNS01Route(r *Reconciler) {
	r.dns01LookupNS = func(_ context.Context, name string) ([]string, bool) {
		if name == "example.com" {
			return []string{"kip.ns.cloudflare.com"}, true
		}
		return nil, true
	}
	r.dns01ZoneFinder = fixedZoneFinder{id: "zone123"}
}

func TestHandBackRestoredCert(t *testing.T) {
	const elsewhere = "198.51.100.9"
	const window = 15 * 24 * time.Hour
	restored := models.SSLIssueMethodRestored
	for _, tc := range []struct {
		name     string
		left     time.Duration
		method   string
		resolves []string
		dns01    bool
		webOff   bool
		want     string // the ACME call made, "" for none
	}{
		{"before its renewal window", 60 * 24 * time.Hour, restored, []string{restoredTestOwnIP}, true, false, ""},
		{"DNS still on the old server", window, restored, []string{elsewhere}, false, false, ""},
		{"no DNS record", window, restored, nil, true, false, ""},
		{"DNS here", window, restored, []string{restoredTestOwnIP}, false, false, "ssl.issue"},
		{"behind a proxy, DNS-01 available", window, restored, []string{elsewhere}, true, false, "ssl.issue_dns01"},
		{"no website, DNS here, no DNS-01", window, restored, []string{restoredTestOwnIP}, false, true, ""},
		{"certbot's own certificate", window, "http-01", []string{restoredTestOwnIP}, true, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, ag, sc, dom := restoredFixture(t, tc.left, tc.method, tc.resolves...)
			if tc.dns01 {
				withDNS01Route(r)
			}
			dom.WebDisabled = tc.webOff

			r.reconcileSSLForDomain(context.Background(), dom)

			for _, m := range []string{"ssl.issue", "ssl.issue_dns01"} {
				if got := agentCalled(ag, m); got != (m == tc.want) {
					t.Fatalf("%s called = %v, want %q only", m, got, tc.want)
				}
			}
			if tc.want == "" {
				wantStillServing(t, sc.byDomain[dom.ID], tc.method)
			}
		})
	}
}

// An expired restored certificate takes the path a new domain's does, here
// a proxied domain without DNS-01: parked on a placeholder, re-checked daily.
func TestHandBackRestoredCert_Expired(t *testing.T) {
	r, ag, sc, dom := restoredFixture(t, -time.Hour, models.SSLIssueMethodRestored, "198.51.100.9")

	r.reconcileSSLForDomain(context.Background(), dom)

	if agentCalled(ag, "ssl.issue") || agentCalled(ag, "ssl.issue_dns01") {
		t.Fatal("no ACME route can pass, yet an issue was attempted")
	}
	if c := sc.byDomain[dom.ID]; c.LastError == nil || !strings.Contains(*c.LastError, "DNS-01 is not available") {
		t.Fatalf("certificate %+v, want it parked with the no-DNS-01 reason", c)
	}
}

// A server that doesn't know its own address can't tell which route an
// issue would take (with no address of its own to compare, tryACMEOrFallback
// picks HTTP-01): the restored certificate keeps serving.
func TestHandBackRestoredCert_OwnAddressUnknown(t *testing.T) {
	r, ag, sc, dom := restoredFixture(t, 15*24*time.Hour, models.SSLIssueMethodRestored, "198.51.100.9")
	withDNS01Route(r)
	r.serverSettings.(*fakeServerSettingsRepo).settings.PublicIPv4 = ""

	r.reconcileSSLForDomain(context.Background(), dom)

	if agentCalled(ag, "ssl.issue") || agentCalled(ag, "ssl.issue_dns01") {
		t.Fatal("an issue was attempted for a restored certificate while this server's address is unknown")
	}
	wantStillServing(t, sc.byDomain[dom.ID], models.SSLIssueMethodRestored)
}

// wantStillServing: the pass left the certificate as it was, issued with its
// files, not parked on a placeholder.
func wantStillServing(t *testing.T, c *models.SSLCertificate, method string) {
	t.Helper()
	if c.Status != models.SSLStatusIssued || c.IssueMethod != method || c.LastError != nil ||
		c.CertPath == nil || *c.CertPath != "/etc/letsencrypt/live/sub.example.com/fullchain.pem" {
		t.Fatalf("certificate %+v, want it left serving as it was", c)
	}
}

func TestSSLSANDriftEligible_SkipsRestoredCertificates(t *testing.T) {
	row := emailRow("example.com")
	row.IssueMethod = models.SSLIssueMethodRestored
	if sslSANDriftEligible(row) {
		t.Fatal("a restored certificate must be left to the hand-back, not reissued for SAN drift")
	}
}
