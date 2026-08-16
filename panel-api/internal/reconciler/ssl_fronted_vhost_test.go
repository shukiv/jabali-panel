package reconciler

// JAB-237 — the vhost gates for CDN-fronted domains. The 2026-08-11 fleet
// outage: an le-mode domain parked on its self-signed bootstrap cert
// rendered :80-only, and Cloudflare Full (which dials origin :443) 520'd
// every zone. Pinned here:
//
//  1. Fronted + self-signed → serve_https TRUE, redirect_https FALSE
//     (the 520 fix: :443 exists; no origin 301, which loops CF Flexible).
//  2. Fronted + issued → same shape (the latent Flexible-loop guard).
//  3. Direct + self-signed bootstrap → both FALSE (GH #896 preserved).
//  4. Direct + issued → both TRUE (unchanged).
//  5. Inconclusive apex lookup keeps the last cached fronted value —
//     a resolver blip must not re-render fronted vhosts :80-only.
//  6. A dns-01-issued cert implies fronted without any lookup.

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

const (
	frontedTestOwnIP    = "203.0.113.7"
	selfSignedCertPath  = "/etc/ssl/jabali-selfsigned/f.example/fullchain.pem"
	selfSignedKeyPath   = "/etc/ssl/jabali-selfsigned/f.example/privkey.pem"
	letsEncryptCertPath = "/etc/letsencrypt/live/f.example/fullchain.pem"
	letsEncryptKeyPath  = "/etc/letsencrypt/live/f.example/privkey.pem"
)

func frontedVhostFixture(t *testing.T, certPath, keyPath string, apexAddrs []string, queried bool) (*Reconciler, *fakeAgent, *models.Domain, *fakeSSLCertRepo) {
	t.Helper()
	uname := "u1"
	dr := newFakeDomainRepo()
	ur := &fakeUserRepo{users: map[string]*models.User{
		"u1": {ID: "u1", Email: "u1@example.com", Username: &uname},
	}}
	sc := newFakeSSLCertRepo()
	srv := &fakeServerSettingsRepo{settings: &models.ServerSettings{PublicIPv4: frontedTestOwnIP}}
	ag := &fakeAgent{}

	dom := &models.Domain{
		ID: "d1", Name: "f.example", UserID: "u1",
		DocRoot: "/home/u1/domains/f.example/public_html",
		SSLMode: models.SSLModeLE, IsEnabled: true,
	}
	dr.domains[dom.ID] = dom
	sc.byDomain[dom.ID] = &models.SSLCertificate{
		ID: "c1", DomainID: dom.ID, Status: models.SSLStatusSelfSigned,
		CertPath: &certPath, KeyPath: &keyPath,
	}

	r := New(dr, ur, ag, slog.Default(), Config{}).
		WithSSLCerts(sc).
		WithDNSRepos(nil, nil, srv)
	r.dnsPreflight = func(context.Context, string) ([]string, bool) {
		return apexAddrs, queried
	}
	return r, ag, dom, sc
}

func vhostHTTPSParams(t *testing.T, ag *fakeAgent) (redirect, serve bool) {
	t.Helper()
	call, ok := findAgentCall(ag, "domain.create")
	if !ok {
		t.Fatal("domain.create was not dispatched")
	}
	params := call.params.(map[string]any)
	redirect, rOK := params["redirect_https"].(bool)
	serve, sOK := params["serve_https"].(bool)
	if !rOK || !sOK {
		t.Fatalf("redirect_https/serve_https missing or untyped in params: %v / %v", params["redirect_https"], params["serve_https"])
	}
	return redirect, serve
}

func TestFrontedVhost_SelfSignedServes443NoRedirect(t *testing.T) {
	r, ag, dom, _ := frontedVhostFixture(t, selfSignedCertPath, selfSignedKeyPath, cfEdgeAddrs, true)

	r.createDomainOnAgent(context.Background(), dom)

	redirect, serve := vhostHTTPSParams(t, ag)
	if !serve {
		t.Error("fronted + self-signed must SERVE :443 — CF Full dials origin :443 and 520s on a closed port (the 2026-08-11 outage)")
	}
	if redirect {
		t.Error("fronted domain must not origin-redirect :80 — CF Flexible fetches over :80 and a 301 loops it")
	}
}

func TestFrontedVhost_IssuedServes443NoRedirect(t *testing.T) {
	r, ag, dom, sc := frontedVhostFixture(t, letsEncryptCertPath, letsEncryptKeyPath, cfEdgeAddrs, true)
	sc.byDomain[dom.ID].Status = models.SSLStatusIssued

	r.createDomainOnAgent(context.Background(), dom)

	redirect, serve := vhostHTTPSParams(t, ag)
	if !serve || redirect {
		t.Errorf("fronted + issued: (redirect=%v, serve=%v), want (false, true) — the origin redirect loops CF Flexible the moment a real cert lands", redirect, serve)
	}
}

func TestDirectVhost_SelfSignedBootstrapStaysHTTPOnly(t *testing.T) {
	r, ag, dom, _ := frontedVhostFixture(t, selfSignedCertPath, selfSignedKeyPath, []string{frontedTestOwnIP}, true)

	r.createDomainOnAgent(context.Background(), dom)

	redirect, serve := vhostHTTPSParams(t, ag)
	if redirect || serve {
		t.Errorf("direct + self-signed bootstrap: (redirect=%v, serve=%v), want (false, false) — GH #896's no-cert-warning window must survive JAB-237", redirect, serve)
	}
}

func TestDirectVhost_IssuedRedirectsAndServes(t *testing.T) {
	r, ag, dom, sc := frontedVhostFixture(t, letsEncryptCertPath, letsEncryptKeyPath, []string{frontedTestOwnIP}, true)
	sc.byDomain[dom.ID].Status = models.SSLStatusIssued

	r.createDomainOnAgent(context.Background(), dom)

	redirect, serve := vhostHTTPSParams(t, ag)
	if !redirect || !serve {
		t.Errorf("direct + issued: (redirect=%v, serve=%v), want (true, true)", redirect, serve)
	}
}

// JAB-271 defect #2: the reporter hypothesised the vhost "was never
// re-rendered after the cert went self-signed→LE". It is: createDomainOnAgent
// re-derives redirect_https/serve_https from the LIVE cert row on EVERY tick
// (the reconciler dispatches domain.create unconditionally each pass, SSL
// converging first, and the agent's writeVhost gate compares full rendered
// bytes). Drive one direct domain through the transition and assert the params
// flip on the second dispatch — a guard against a future reconciler-level
// dispatch hash that omits cert state (which WOULD strand the :80 shape).
func TestDirectVhost_SelfSignedToLETransitionReRenders(t *testing.T) {
	r, ag, dom, sc := frontedVhostFixture(t, selfSignedCertPath, selfSignedKeyPath, []string{frontedTestOwnIP}, true)

	// Tick 1: still on the self-signed bootstrap placeholder.
	r.createDomainOnAgent(context.Background(), dom)
	if redirect, serve := vhostHTTPSParams(t, ag); redirect || serve {
		t.Fatalf("pre-transition: (redirect=%v, serve=%v), want (false, false)", redirect, serve)
	}

	// LE lands: the cert row's path moves to the letsencrypt live dir.
	leCert, leKey := letsEncryptCertPath, letsEncryptKeyPath
	sc.byDomain[dom.ID].Status = models.SSLStatusIssued
	sc.byDomain[dom.ID].CertPath = &leCert
	sc.byDomain[dom.ID].KeyPath = &leKey

	ag.mu.Lock()
	ag.calls = nil
	ag.mu.Unlock()

	// Tick 2: the very next dispatch must reflect the trusted cert.
	r.createDomainOnAgent(context.Background(), dom)
	if redirect, serve := vhostHTTPSParams(t, ag); !redirect || !serve {
		t.Fatalf("post-transition: (redirect=%v, serve=%v), want (true, true) — the :80 shape must update the tick a real cert lands", redirect, serve)
	}
}

func TestFrontedVhost_InconclusiveLookupKeepsCachedValue(t *testing.T) {
	r, ag, dom, _ := frontedVhostFixture(t, selfSignedCertPath, selfSignedKeyPath, cfEdgeAddrs, true)

	// First render: definitive fronted answer, cached.
	r.createDomainOnAgent(context.Background(), dom)
	// Expire the entry, then blind the resolvers.
	r.frontedMu.Lock()
	e := r.frontedCache[dom.Name]
	e.expires = time.Now().Add(-time.Minute)
	r.frontedCache[dom.Name] = e
	r.frontedMu.Unlock()
	r.dnsPreflight = func(context.Context, string) ([]string, bool) { return nil, false }

	ag.mu.Lock()
	ag.calls = nil
	ag.mu.Unlock()
	r.createDomainOnAgent(context.Background(), dom)

	redirect, serve := vhostHTTPSParams(t, ag)
	if !serve || redirect {
		t.Errorf("resolver blip: (redirect=%v, serve=%v), want the cached fronted shape kept — flapping to :80-only re-creates the 520 outage", redirect, serve)
	}
}

func TestFrontedVhost_DNS01IssueMethodImpliesFronted(t *testing.T) {
	// Apex lookup says "direct" (stale DNS view), but the cert was issued
	// via DNS-01 — which only happens for fronted domains.
	r, ag, dom, sc := frontedVhostFixture(t, letsEncryptCertPath, letsEncryptKeyPath, []string{frontedTestOwnIP}, true)
	sc.byDomain[dom.ID].Status = models.SSLStatusIssued
	sc.byDomain[dom.ID].IssueMethod = issueMethodDNS01

	r.createDomainOnAgent(context.Background(), dom)

	redirect, serve := vhostHTTPSParams(t, ag)
	if !serve || redirect {
		t.Errorf("dns-01 lineage: (redirect=%v, serve=%v), want (false, true) without trusting the apex lookup", redirect, serve)
	}
}
