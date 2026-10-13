package reconciler

import (
	"context"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// ssl_panel_hostname_test.go — GH #2126. The panel hostname's row
// (is_panel_primary) has no document root, and its certificate is the panel's
// own (panel_certificate). A per-domain certificate row for it reached
// tryACMEOrFallback through the 1-minute retry sweep, which handed the agent
// webroot "" — "invalid webroot \"\"" twenty times, then a failed row in the
// SSL Manager with "manual retry required".

// healCertRepo is the shared fake plus the two methods these tests need to
// observe: the retry sweep's due list, and deletes.
type healCertRepo struct {
	*fakeSSLCertRepo
	due     []models.SSLCertificate
	deleted []string
}

func (f *healCertRepo) ListDueForACMERetry(context.Context, time.Time, int) ([]models.SSLCertificate, error) {
	return f.due, nil
}

func (f *healCertRepo) DeleteByDomainID(_ context.Context, domainID string) error {
	f.deleted = append(f.deleted, domainID)
	delete(f.byDomain, domainID)
	return nil
}

func panelHostnameFixture(t *testing.T) (*Reconciler, *fakeAgent, *healCertRepo, *models.Domain) {
	t.Helper()
	r, ag, sc, _ := sslWebrootFixture(t, nil)
	hc := &healCertRepo{fakeSSLCertRepo: sc}
	r.WithSSLCerts(hc)
	dom := &models.Domain{
		OwnershipState: verifiedOwnership,
		ID:             "pp", Name: "panel.example.com", UserID: "admin",
		DocRoot: "", IsPanelPrimary: true, IsEnabled: true,
		SSLMode: models.SSLModeLE,
	}
	r.domains.(*fakeDomainRepo).domains[dom.ID] = dom
	return r, ag, hc, dom
}

func agentCalledWith(ag *fakeAgent, method string) []map[string]any {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	var out []map[string]any
	for _, c := range ag.calls {
		if c.method == method {
			p, _ := c.params.(map[string]any)
			out = append(out, p)
		}
	}
	return out
}

// A domain with no document root can't answer an HTTP-01 challenge from one,
// so ssl.issue must never be sent webroot "". It falls back to self-signed and
// says why.
func TestTryACMEOrFallback_NoDocRoot_NeverSendsEmptyWebroot(t *testing.T) {
	r, ag, sc, _ := sslWebrootFixture(t, nil)
	dom := &models.Domain{
		OwnershipState: verifiedOwnership,
		ID:             "d2", Name: "app.example.com", UserID: "u1",
		DocRoot: "", SSLMode: models.SSLModeLE, IsEnabled: true,
		ManagedBy: models.DomainManagedByDockerApp,
	}
	r.domains.(*fakeDomainRepo).domains[dom.ID] = dom
	past := time.Now().UTC().Add(-time.Minute)
	cert := &models.SSLCertificate{ID: "c2", DomainID: dom.ID, Status: models.SSLStatusPendingACMERetry, RetryCount: 1, NextRetryAt: &past}
	sc.byDomain[dom.ID] = cert

	r.tryACMEOrFallback(context.Background(), dom, cert)

	for _, p := range agentCalledWith(ag, "ssl.issue") {
		t.Fatalf("ssl.issue was sent for a domain with no document root: %v", p)
	}
	if sc.acmeFailures != 1 {
		t.Fatalf("want the attempt recorded once (self-signed + backoff), got %d", sc.acmeFailures)
	}
	if cert.LastError == nil || !strings.Contains(*cert.LastError, "no document root") {
		t.Fatalf("last_error should say there is no document root, got %v", cert.LastError)
	}
}

// The control: a domain with a document root still goes to ssl.issue with it.
func TestTryACMEOrFallback_DocRootStillIssues(t *testing.T) {
	r, ag, sc, dom := sslWebrootFixture(t, nil)
	r.tryACMEOrFallback(context.Background(), dom, sc.byDomain[dom.ID])
	calls := agentCalledWith(ag, "ssl.issue")
	if len(calls) != 1 || calls[0]["webroot"] != dom.DocRoot {
		t.Fatalf("want one ssl.issue with webroot %q, got %v", dom.DocRoot, calls)
	}
}

// The retry sweep leaves the panel hostname alone: no ACME, no self-signed
// fallback, no failure recorded.
func TestRetrySSLDueForACME_SkipsPanelHostname(t *testing.T) {
	r, ag, hc, dom := panelHostnameFixture(t)
	past := time.Now().UTC().Add(-time.Minute)
	row := models.SSLCertificate{ID: "cp", DomainID: dom.ID, Status: models.SSLStatusPendingACMERetry, RetryCount: 3, NextRetryAt: &past}
	hc.byDomain[dom.ID] = &row
	hc.due = []models.SSLCertificate{row}

	r.RetrySSLDueForACME(context.Background())

	if n := len(agentCalledWith(ag, "ssl.issue")) + len(agentCalledWith(ag, "ssl.self_sign")); n != 0 {
		t.Fatalf("the sweep called the agent %d times for the panel hostname", n)
	}
	if hc.acmeFailures != 0 {
		t.Fatalf("the sweep recorded %d ACME failures for the panel hostname", hc.acmeFailures)
	}
}

// The control: an ordinary due row still goes through.
func TestRetrySSLDueForACME_OrdinaryDomainStillRetried(t *testing.T) {
	r, ag, hc, _ := panelHostnameFixture(t)
	hc.due = []models.SSLCertificate{*hc.byDomain["d1"]}
	r.RetrySSLDueForACME(context.Background())
	if len(agentCalledWith(ag, "ssl.issue")) != 1 {
		t.Fatal("an ordinary due certificate was not retried")
	}
}

// Rows left by the per-domain doors before they refused the panel hostname are
// removed on the hostname's reconcile — only the ACME rows that can never
// succeed. Anything else stays.
func TestReconcile_PanelHostname_DropsStuckACMERow(t *testing.T) {
	cp := "/etc/ssl/jabali-selfsigned/panel.example.com/fullchain.pem"
	cases := []struct {
		status, mode string
		drop         bool
	}{
		{models.SSLStatusPending, models.SSLModeLE, true},
		{models.SSLStatusPendingACMERetry, models.SSLModeLE, true},
		{models.SSLStatusFailed, models.SSLModeLE, true},
		{models.SSLStatusFailed, "", true},
		{models.SSLStatusIssued, models.SSLModeLE, false},
		{models.SSLStatusSelfSigned, models.SSLModeLE, false},
		{models.SSLStatusRenewing, models.SSLModeLE, false},
		{models.SSLStatusFailed, models.SSLModeCustom, false},
		{models.SSLStatusPendingACMERetry, models.SSLModeSelf, false},
	}
	for _, tc := range cases {
		t.Run(tc.status+"/"+tc.mode, func(t *testing.T) {
			r, _, hc, dom := panelHostnameFixture(t)
			dom.SSLMode = tc.mode
			hc.byDomain[dom.ID] = &models.SSLCertificate{ID: "cp", DomainID: dom.ID, Status: tc.status, CertPath: &cp}

			r.reconcileEnabledDomain(context.Background(), dom.Name, dom, map[string]bool{})

			got := len(hc.deleted) == 1 && hc.deleted[0] == dom.ID
			if got != tc.drop {
				t.Fatalf("deleted=%v, want drop=%v", hc.deleted, tc.drop)
			}
			if _, ok := hc.byDomain["d1"]; !ok {
				t.Fatal("another domain's certificate was touched")
			}
		})
	}
}

// Without a row there is nothing to delete.
func TestReconcile_PanelHostname_NoRowNoDelete(t *testing.T) {
	r, _, hc, dom := panelHostnameFixture(t)
	r.reconcileEnabledDomain(context.Background(), dom.Name, dom, map[string]bool{})
	if len(hc.deleted) != 0 {
		t.Fatalf("deleted %v with no row for the panel hostname", hc.deleted)
	}
}
