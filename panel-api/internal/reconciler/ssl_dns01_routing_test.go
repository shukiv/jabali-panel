package reconciler

// JAB-235 — DNS-01 fallback routing. Four properties pinned here:
//
//  1. Fronted apex + available provider → ssl.issue_dns01 (never ssl.issue:
//     the edge would eat the HTTP-01 token and burn LE quota), shared
//     lineage name, sans = apex + www, issue_method recorded.
//  2. Fronted apex + NO provider → NO agent ACME call at all, retry count
//     unchanged (no LE quota spent — the #1049 cap must not tick), clear
//     reason, daily re-check.
//  3. Apex resolving to us → HTTP-01 exactly as before.
//  4. Unknown own IP (fresh install) → fail open to HTTP-01.
//
// A REAL DNS-01 failure, by contrast, is a spent LE attempt and must keep
// counting toward acmeMaxRetries (property 5).

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

const dns01TestOwnIP = "203.0.113.7"

// cfEdgeAddrs is what a Cloudflare-proxied apex resolves to — anything
// disjoint from dns01TestOwnIP works.
var cfEdgeAddrs = []string{"104.21.32.5", "172.67.140.9"}

type fixedZoneFinder struct{ id string }

func (f fixedZoneFinder) FindZoneID(context.Context, string) (string, error) { return f.id, nil }

// dns01Fixture builds on the webroot fixture: apex fronted by a CDN, our
// public IP known, NS delegation answered by the test seam.
func dns01Fixture(t *testing.T, nsHosts []string, zoneID string) (*Reconciler, *fakeAgent, *fakeSSLCertRepo, *models.Domain) {
	t.Helper()
	r, ag, sc, dom := sslWebrootFixture(t, nil)
	r.serverSettings.(*fakeServerSettingsRepo).settings.PublicIPv4 = dns01TestOwnIP
	r.dnsPreflight = func(context.Context, string) ([]string, bool) {
		return cfEdgeAddrs, true
	}
	r.dns01LookupNS = func(_ context.Context, name string) ([]string, bool) {
		if name == "example.com" {
			return nsHosts, true
		}
		return nil, true // below the zone apex
	}
	if zoneID != "" {
		r.dns01ZoneFinder = fixedZoneFinder{id: zoneID}
	}
	if ag.resultByMethod == nil {
		ag.resultByMethod = map[string]json.RawMessage{}
	}
	ag.resultByMethod["ssl.issue_dns01"] = json.RawMessage(`{
		"cert_path": "/etc/letsencrypt/live/sub.example.com/fullchain.pem",
		"key_path":  "/etc/letsencrypt/live/sub.example.com/privkey.pem",
		"issued_at": "2026-08-11T00:00:00Z",
		"expires_at": "2026-11-09T00:00:00Z"
	}`)
	return r, ag, sc, dom
}

var errBoom = errors.New("boom")

// findAgentCall returns the recorded call for method, if any.
func findAgentCall(ag *fakeAgent, method string) (fakeCall, bool) {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	for _, c := range ag.calls {
		if c.method == method {
			return c, true
		}
	}
	return fakeCall{}, false
}

func TestDNS01_FrontedCovered_IssuesViaDNS01(t *testing.T) {
	r, ag, sc, dom := dns01Fixture(t, []string{"kip.ns.cloudflare.com"}, "zone123")

	r.reconcileSSLForDomain(context.Background(), dom)

	if agentCalled(ag, "ssl.issue") {
		t.Fatal("fronted domain must NOT attempt HTTP-01 — the CDN edge eats the token and burns LE quota")
	}
	call, ok := findAgentCall(ag, "ssl.issue_dns01")
	if !ok {
		t.Fatal("fronted + covered domain must issue via ssl.issue_dns01")
	}
	params := call.params.(map[string]any)
	if params["cert_name"] != dom.Name {
		t.Errorf("cert_name = %v, want %s (shared lineage with the HTTP-01 path)", params["cert_name"], dom.Name)
	}
	sans := params["sans"].([]string)
	if len(sans) != 2 || sans[0] != dom.Name || sans[1] != "www."+dom.Name {
		t.Errorf("sans = %v, want [apex, www]", sans)
	}
	if sc.issueMethods["c1"] != issueMethodDNS01 {
		t.Errorf("issue_method = %q, want %q", sc.issueMethods["c1"], issueMethodDNS01)
	}
}

func TestDNS01_FrontedUncovered_ParksWithoutLEAttempt(t *testing.T) {
	// Cloudflare NS, but the token doesn't cover the zone (FindZoneID "").
	r, ag, sc, dom := dns01Fixture(t, []string{"kip.ns.cloudflare.com"}, "")
	r.dns01ZoneFinder = fixedZoneFinder{id: ""}
	sc.byDomain[dom.ID].RetryCount = 5

	r.reconcileSSLForDomain(context.Background(), dom)

	if agentCalled(ag, "ssl.issue") || agentCalled(ag, "ssl.issue_dns01") {
		t.Fatal("fronted + uncovered must not attempt ANY ACME call — there is no path that can succeed")
	}
	cert := sc.byDomain[dom.ID]
	if cert.RetryCount != 5 {
		t.Errorf("retry count %d, want 5 (unchanged) — no LE attempt was made, the #1049 cap must not tick", cert.RetryCount)
	}
	if cert.LastError == nil || !strings.Contains(*cert.LastError, "DNS-01 is not available") {
		t.Errorf("last error should carry the no-provider reason, got %v", cert.LastError)
	}
	if cert.NextRetryAt == nil || time.Until(*cert.NextRetryAt) < 23*time.Hour {
		t.Errorf("next check should be ~daily, got %v", cert.NextRetryAt)
	}
}

func TestDNS01_ApexPointsAtUs_KeepsHTTP01(t *testing.T) {
	r, ag, _, dom := dns01Fixture(t, []string{"kip.ns.cloudflare.com"}, "zone123")
	r.dnsPreflight = func(context.Context, string) ([]string, bool) {
		return []string{dns01TestOwnIP}, true
	}

	r.reconcileSSLForDomain(context.Background(), dom)

	if !agentCalled(ag, "ssl.issue") {
		t.Fatal("a direct (not fronted) domain must stay on HTTP-01")
	}
	if agentCalled(ag, "ssl.issue_dns01") {
		t.Fatal("direct domain needlessly routed through DNS-01")
	}
}

func TestDNS01_UnknownOwnIP_FailsOpenToHTTP01(t *testing.T) {
	r, ag, _, dom := dns01Fixture(t, []string{"kip.ns.cloudflare.com"}, "zone123")
	r.serverSettings.(*fakeServerSettingsRepo).settings.PublicIPv4 = ""

	r.reconcileSSLForDomain(context.Background(), dom)

	if !agentCalled(ag, "ssl.issue") {
		t.Fatal("without a known public IP the fronted judgement cannot be made — must fail open to HTTP-01")
	}
}

func TestDNS01_IssueFailureCountsTowardCap(t *testing.T) {
	r, ag, sc, dom := dns01Fixture(t, []string{"kip.ns.cloudflare.com"}, "zone123")
	delete(ag.resultByMethod, "ssl.issue_dns01")
	ag.errByMethod = map[string]error{"ssl.issue_dns01": errBoom}
	sc.byDomain[dom.ID].RetryCount = 2

	r.reconcileSSLForDomain(context.Background(), dom)

	cert := sc.byDomain[dom.ID]
	if cert.RetryCount != 3 {
		t.Errorf("retry count %d, want 3 — a REAL DNS-01 failure is a spent LE attempt and must count toward the cap", cert.RetryCount)
	}
}

// jabali-authoritative fronted zone routes DNS-01 through pdns (no CF
// involvement) — e.g. an operator pointing the apex at a third-party proxy
// while we host the DNS.
func TestDNS01_FrontedPDNSZone_IssuesViaDNS01(t *testing.T) {
	r, ag, _, dom := dns01Fixture(t, []string{"ns1.panel.example"}, "")
	r.serverSettings.(*fakeServerSettingsRepo).settings.NS1Name = "ns1.panel.example"

	r.reconcileSSLForDomain(context.Background(), dom)

	if !agentCalled(ag, "ssl.issue_dns01") {
		t.Fatal("fronted + jabali-authoritative must issue via DNS-01 through pdns")
	}
}
