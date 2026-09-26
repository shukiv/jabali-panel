package reconciler

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// plannerFixture is one enabled domain with DNS wired, so a tick drives both
// the vhost phase (domain.create) and the DNS phase (dns.zone.upsert).
func plannerFixture(t *testing.T) (*Reconciler, *fakeAgent, *models.Domain) {
	t.Helper()
	agent := &fakeAgent{}
	domainRepo := &fakeDomainRepo{domains: map[string]*models.Domain{}}
	userRepo := &fakeUserRepo{users: map[string]*models.User{}}
	username := "alice"
	userRepo.users["user-1"] = &models.User{ID: "user-1", Email: "alice@example.com", Username: &username}
	now := time.Now().UTC()
	dom := &models.Domain{
		ID:        "domain-1",
		UserID:    "user-1",
		Name:      "example.com",
		DocRoot:   "/home/alice/domains/example.com/public_html",
		IsEnabled: true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	domainRepo.domains[dom.ID] = dom
	r := New(domainRepo, userRepo, agent, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Interval: time.Second}).
		WithDNSRepos(
			&fakeDNSZoneRepo{zones: map[string]*models.DNSZone{}},
			&fakeDNSRecordRepo{records: map[string]*models.DNSRecord{}},
			&fakeServerSettingsRepo{settings: &models.ServerSettings{
				PublicIPv4: "192.0.2.1",
				NS1Name:    "ns1.example.com",
				NS2Name:    "ns2.example.com",
				AdminEmail: "admin@example.com",
			}},
		)
	return r, agent, dom
}

func countMethod(ag *fakeAgent, method string) int {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	n := 0
	for _, c := range ag.calls {
		if c.method == method {
			n++
		}
	}
	return n
}

// JAB-369 AC5: a Force run ignores the process-local ledger. The admin
// "reconcile (force)" endpoint and DR promotion call ReconcileAllForce on a
// long-lived process; before the planner, its DNS pass still consulted the
// push cache, so a zone this process had already pushed was skipped even
// though the operator asked for a full re-convergence.
func TestReconcileAllForce_RepushesAnAlreadyPushedZone(t *testing.T) {
	r, ag, dom := plannerFixture(t)
	ctx := context.Background()

	if err := r.ReconcileOne(ctx, dom.ID); err != nil {
		t.Fatal(err)
	}
	if got := countMethod(ag, "dns.zone.upsert"); got != 1 {
		t.Fatalf("the first reconcile must push the zone once, got %d", got)
	}

	if err := r.ReconcileAllForce(ctx); err != nil {
		t.Fatal(err)
	}
	if got := countMethod(ag, "dns.zone.upsert"); got != 2 {
		t.Fatalf("a force run must re-push an unchanged zone this process already pushed; dns.zone.upsert calls = %d, want 2", got)
	}
}
