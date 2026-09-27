package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type fakePGGrantRepo struct {
	grants map[string][]string
	err    error
}

func (f *fakePGGrantRepo) ListPGDatabaseGrants(context.Context) (map[string][]string, error) {
	return f.grants, f.err
}

func pgPublicFixture(pgEnabled bool) (*Reconciler, *fakeAgent) {
	return pgPublicFixtureWith(pgEnabled, &fakePGGrantRepo{})
}

func pgPublicFixtureWith(pgEnabled bool, repo *fakePGGrantRepo) (*Reconciler, *fakeAgent) {
	ag := &fakeAgent{}
	r := New(&fakeDomainRepo{}, &fakeUserRepo{}, ag, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Interval: time.Second}).
		WithDNSRepos(&fakeDNSZoneRepo{zones: map[string]*models.DNSZone{}}, &fakeDNSRecordRepo{records: map[string]*models.DNSRecord{}},
			&fakeServerSettingsRepo{settings: &models.ServerSettings{PostgresEnabled: pgEnabled}}).
		WithPGPublicAccess(repo)
	return r, ag
}

// With Postgres enabled, the revoke runs on the first tick, and a steady tick
// inside the audit interval sends nothing.
func TestReconcilePGPublicAccess_RunsOnceThenWaitsForTheInterval(t *testing.T) {
	r, ag := pgPublicFixture(true)
	r.reconcilePGPublicAccess(context.Background())
	r.reconcilePGPublicAccess(context.Background())
	if n := countMethod(ag, "db.postgres.revoke_public_access"); n != 1 {
		t.Fatalf("calls = %d, want 1", n)
	}
}

// With the engine off there is nothing to call (psql may not even exist).
func TestReconcilePGPublicAccess_SkipsWhenPostgresIsDisabled(t *testing.T) {
	r, ag := pgPublicFixture(false)
	r.reconcilePGPublicAccess(context.Background())
	if n := countMethod(ag, "db.postgres.revoke_public_access"); n != 0 {
		t.Fatalf("calls = %d, want 0", n)
	}
}

// A failed run is retried on the next tick (the ledger is not stamped).
func TestReconcilePGPublicAccess_FailedRunIsRetried(t *testing.T) {
	r, ag := pgPublicFixture(true)
	ag.failMethod = "db.postgres.revoke_public_access"
	r.reconcilePGPublicAccess(context.Background())
	ag.failMethod = ""
	r.reconcilePGPublicAccess(context.Background())
	if n := countMethod(ag, "db.postgres.revoke_public_access"); n != 2 {
		t.Fatalf("calls = %d, want the failed run retried", n)
	}
}

// ReconcileAll runs the pass when it is enabled.
func TestReconcileAll_RunsThePGPublicAccessPass(t *testing.T) {
	r, ag := pgPublicFixture(true)
	r.ReconcileAll(context.Background())
	if n := countMethod(ag, "db.postgres.revoke_public_access"); n != 1 {
		t.Fatalf("calls = %d, want 1", n)
	}
}

func pgPublicCalls(ag *fakeAgent) []string {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	var out []string
	for _, c := range ag.calls {
		if c.method == "db.postgres.revoke_public_access" {
			raw, _ := json.Marshal(c.params)
			out = append(out, string(raw))
		}
	}
	return out
}

// The panel's grants go with the call, so the Agent re-grants each role
// before it revokes PUBLIC. No grants is sent as {} (never null, which the
// Agent refuses).
func TestReconcilePGPublicAccess_SendsThePanelGrants(t *testing.T) {
	r, ag := pgPublicFixtureWith(true, &fakePGGrantRepo{grants: map[string][]string{"alice_shop": {"alice_app"}}})
	r.reconcilePGPublicAccess(context.Background())
	if calls := pgPublicCalls(ag); len(calls) != 1 || calls[0] != `{"grants":{"alice_shop":["alice_app"]}}` {
		t.Fatalf("calls = %v", calls)
	}
	r2, ag2 := pgPublicFixtureWith(true, &fakePGGrantRepo{})
	r2.reconcilePGPublicAccess(context.Background())
	if calls := pgPublicCalls(ag2); len(calls) != 1 || calls[0] != `{"grants":{}}` {
		t.Fatalf("calls without grants = %v", calls)
	}
}

// A failed read sends nothing: revoking PUBLIC without the re-grant list
// could lock a tenant out of a restored database.
func TestReconcilePGPublicAccess_ListErrorSendsNothing(t *testing.T) {
	r, ag := pgPublicFixtureWith(true, &fakePGGrantRepo{err: errors.New("db down")})
	r.reconcilePGPublicAccess(context.Background())
	if calls := pgPublicCalls(ag); len(calls) != 0 {
		t.Fatalf("calls = %v, want none", calls)
	}
}
