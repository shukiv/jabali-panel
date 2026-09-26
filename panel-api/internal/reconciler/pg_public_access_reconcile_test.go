package reconciler

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func pgPublicFixture(pgEnabled bool) (*Reconciler, *fakeAgent) {
	ag := &fakeAgent{}
	r := New(&fakeDomainRepo{}, &fakeUserRepo{}, ag, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Interval: time.Second}).
		WithDNSRepos(&fakeDNSZoneRepo{zones: map[string]*models.DNSZone{}}, &fakeDNSRecordRepo{records: map[string]*models.DNSRecord{}},
			&fakeServerSettingsRepo{settings: &models.ServerSettings{PostgresEnabled: pgEnabled}}).
		WithPGPublicAccess()
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
