package reconciler

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// steadyTickObservations are the Agent methods an unchanged second normal
// tick may still send: reads that learn the host's state, never writes.
// JAB-369's first acceptance criterion is that such a tick performs no
// deterministic projection Agent calls, so this list is closed. A new pass
// that sends a write on every tick fails the tests below until it goes
// through the planner's ledger (phaseDecide / phaseApplied / phaseFailed)
// or is shown to be a read and added here.
var steadyTickObservations = map[string]bool{
	"domain.list": true,
}

// steadyStateFixture extends plannerFixture into a host with more of the
// tick's projections live: a second domain with a rate limit, a panel
// hostname (the recursor self-zone), an orphan site the Agent still lists
// (fakeAgent's domain.list reports foo.bar.com, which has no row), and,
// when webmailOn, issued certificates plus one domain with webmail and one
// without, so the webmail sweep writes one vhost and removes another.
func steadyStateFixture(t *testing.T, webmailOn bool) (*Reconciler, *fakeAgent) {
	t.Helper()
	r, ag, dom := plannerFixture(t)
	settings := r.serverSettings.(*fakeServerSettingsRepo).settings
	settings.Hostname = "panel.example.com"
	settings.WebmailEnabled = webmailOn

	domains := r.domains.(*fakeDomainRepo)
	now := time.Now().UTC()
	shop := &models.Domain{
		ID:           "domain-2",
		UserID:       "user-1",
		Name:         "shop.example.net",
		DocRoot:      "/home/alice/domains/shop.example.net/public_html",
		IsEnabled:    true,
		RateLimitRPS: 5,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	domains.domains[shop.ID] = shop

	if webmailOn {
		dom.EmailEnabled = true
		dom.WebmailEnabled = true
		// A converged host has already provisioned the domain's mail: the
		// DKIM selector is set, so ensureTenantEmailEnabled is a DB-only
		// no-op. (fakeAgent's domain.email_enable reply is empty, so
		// without this the fixture would retry the provisioning forever.)
		selector := "jabali"
		dom.DkimSelector = &selector
		certs := newFakeSSLCertRepo()
		expires := now.Add(60 * 24 * time.Hour)
		for _, d := range []*models.Domain{dom, shop} {
			cp := "/etc/jabali/ssl/" + d.Name + "/fullchain.pem"
			kp := "/etc/jabali/ssl/" + d.Name + "/privkey.pem"
			certs.byDomain[d.ID] = &models.SSLCertificate{
				ID: "cert-" + d.ID, DomainID: d.ID, Status: models.SSLStatusIssued,
				IssuedAt: &now, ExpiresAt: &expires, CertPath: &cp, KeyPath: &kp,
			}
		}
		r.WithSSLCerts(certs)
	}
	return r, ag
}

// secondTickWrites runs two normal ticks and returns the methods the second
// one sent that are not observations, with their counts.
func secondTickWrites(t *testing.T, r *Reconciler, ag *fakeAgent) map[string]int {
	t.Helper()
	ctx := context.Background()
	if _, err := r.Run(ctx, RunNormal); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	ag.mu.Lock()
	first := len(ag.calls)
	ag.mu.Unlock()
	if _, err := r.Run(ctx, RunNormal); err != nil {
		t.Fatalf("second tick: %v", err)
	}
	ag.mu.Lock()
	defer ag.mu.Unlock()
	writes := map[string]int{}
	for _, c := range ag.calls[first:] {
		if !steadyTickObservations[c.method] {
			writes[c.method]++
		}
	}
	return writes
}

func formatCounts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s x%d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

// JAB-369 AC1, fail-closed: an unchanged second normal tick sends the
// Agent nothing but observations. The gate covers the passes these
// fixtures reach; widening a fixture widens what it protects.
func TestReconcileAll_UnchangedSecondTickSendsNoProjections(t *testing.T) {
	for _, webmailOn := range []bool{false, true} {
		t.Run(fmt.Sprintf("webmail=%v", webmailOn), func(t *testing.T) {
			r, ag := steadyStateFixture(t, webmailOn)
			if writes := secondTickWrites(t, r, ag); len(writes) > 0 {
				t.Fatalf("an unchanged second tick re-sent projections: %s", formatCounts(writes))
			}
		})
	}
}
