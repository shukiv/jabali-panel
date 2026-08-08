package reconciler

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// sslWebrootFixture wires a reconciler whose domain has a self-signed cert (so
// reconcileSSLForDomain routes to the ACME-upgrade path) and an ssl.issue that
// fails with the given error.
func sslWebrootFixture(t *testing.T, issueErr error) (*Reconciler, *fakeAgent, *fakeSSLCertRepo, *models.Domain) {
	t.Helper()
	dr := newFakeDomainRepo()
	sc := newFakeSSLCertRepo()
	srv := &fakeServerSettingsRepo{settings: &models.ServerSettings{AdminEmail: "admin@example.com"}}
	ag := &fakeAgent{errByMethod: map[string]error{"ssl.issue": issueErr}}

	dom := &models.Domain{
		ID: "d1", Name: "sub.example.com", UserID: "u1",
		DocRoot: "/home/u1/domains/sub.example.com/public_html",
		SSLMode: models.SSLModeLE, IsEnabled: true,
	}
	dr.domains[dom.ID] = dom
	// A self-signed cert already exists (the #916 bootstrap), so HTTPS works and
	// reconcileSSLForDomain takes the self_signed -> tryACMEOrFallback branch.
	cp, kp := "/etc/ssl/self/sub.crt", "/etc/ssl/self/sub.key"
	exp := time.Now().UTC().Add(300 * 24 * time.Hour)
	sc.byDomain[dom.ID] = &models.SSLCertificate{
		ID: "c1", DomainID: dom.ID, Status: models.SSLStatusSelfSigned,
		CertPath: &cp, KeyPath: &kp, ExpiresAt: &exp,
	}

	r := New(dr, nil, ag, slog.Default(), Config{}).
		WithSSLCerts(sc).
		WithDNSRepos(nil, nil, srv)
	return r, ag, sc, dom
}

func agentCalled(ag *fakeAgent, method string) bool {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	for _, c := range ag.calls {
		if c.method == method {
			return true
		}
	}
	return false
}

// GH #887: a webroot_not_ready ssl.issue error must be a QUIET deferral — no
// recorded ACME failure (no scary "docroot does not exist" surfaced, no retry
// bump), no self-sign churn. The domain keeps its cert and retries next tick.
func TestReconcileSSL_WebrootNotReady_IsQuietSkip(t *testing.T) {
	r, ag, sc, dom := sslWebrootFixture(t,
		errors.New("agent: failed_precondition: webroot_not_ready: /home/u1/domains/sub.example.com/public_html does not exist yet"))
	r.reconcileSSLForDomain(context.Background(), dom)

	if !agentCalled(ag, "ssl.issue") {
		t.Fatal("expected ssl.issue to be attempted")
	}
	if sc.acmeFailures != 0 {
		t.Errorf("webroot_not_ready must record NO ACME failure, got %d", sc.acmeFailures)
	}
	if agentCalled(ag, "ssl.self_sign") {
		t.Error("webroot_not_ready must not churn a self-sign fallback")
	}
}

// Contrast: a real ACME error must still record a failure (retry scheduled).
func TestReconcileSSL_RealACMEError_RecordsFailure(t *testing.T) {
	r, ag, sc, dom := sslWebrootFixture(t,
		errors.New("agent: internal: certbot issue failed (unauthorized): DNS problem"))
	r.reconcileSSLForDomain(context.Background(), dom)

	if !agentCalled(ag, "ssl.issue") {
		t.Fatal("expected ssl.issue to be attempted")
	}
	if sc.acmeFailures == 0 {
		t.Error("a real ACME failure must be recorded (retry scheduled)")
	}
}

func TestIsWebrootNotReady(t *testing.T) {
	if !isWebrootNotReady(errors.New("x: webroot_not_ready: /p does not exist")) {
		t.Error("must match the marker")
	}
	if isWebrootNotReady(errors.New("certbot issue failed: rate limited")) {
		t.Error("must not match a real failure")
	}
	if isWebrootNotReady(nil) {
		t.Error("nil is not webroot_not_ready")
	}
}
