package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #2004: superuser-owned objects in the panel's Postgres databases go to
// each database's user.

type fakePGOwnerRepo struct {
	owners map[string]string
	err    error
}

func (f *fakePGOwnerRepo) ListPGDatabaseOwners(context.Context) (map[string]string, error) {
	return f.owners, f.err
}

func pgReownFixture(pgEnabled bool, repo *fakePGOwnerRepo) (*Reconciler, *fakeAgent) {
	ag := &fakeAgent{}
	r := New(&fakeDomainRepo{}, &fakeUserRepo{}, ag, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Interval: time.Second}).
		WithDNSRepos(&fakeDNSZoneRepo{zones: map[string]*models.DNSZone{}}, &fakeDNSRecordRepo{records: map[string]*models.DNSRecord{}},
			&fakeServerSettingsRepo{settings: &models.ServerSettings{PostgresEnabled: pgEnabled}}).
		WithPGReown(repo)
	return r, ag
}

func pgReownCalls(ag *fakeAgent) []string {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	var out []string
	for _, c := range ag.calls {
		if c.method == "db.postgres.reown_superuser_objects" {
			raw, _ := json.Marshal(c.params)
			out = append(out, string(raw))
		}
	}
	return out
}

// The panel's databases go with the call, each with its user or "".
func TestReconcilePGReown_SendsEachDatabaseWithItsUser(t *testing.T) {
	r, ag := pgReownFixture(true, &fakePGOwnerRepo{owners: map[string]string{"alice_shop": "alice_app", "bob_blog": ""}})
	r.reconcilePGReown(context.Background())
	if calls := pgReownCalls(ag); len(calls) != 1 || calls[0] != `{"databases":{"alice_shop":"alice_app","bob_blog":""}}` {
		t.Fatalf("calls = %v", calls)
	}
}

// It runs on the first tick, and a steady tick inside the interval sends
// nothing; a new database or user sends it again.
func TestReconcilePGReown_RunsOnceThenOnChange(t *testing.T) {
	repo := &fakePGOwnerRepo{owners: map[string]string{"alice_shop": "alice_app"}}
	r, ag := pgReownFixture(true, repo)
	r.reconcilePGReown(context.Background())
	r.reconcilePGReown(context.Background())
	if n := len(pgReownCalls(ag)); n != 1 {
		t.Fatalf("calls = %d, want 1", n)
	}
	repo.owners = map[string]string{"alice_shop": "alice_app", "bob_blog": ""}
	r.reconcilePGReown(context.Background())
	if n := len(pgReownCalls(ag)); n != 2 {
		t.Fatalf("calls after a new database = %d, want 2", n)
	}
}

// With the engine off there is nothing to call (psql may not even exist).
func TestReconcilePGReown_SkipsWhenPostgresIsDisabled(t *testing.T) {
	r, ag := pgReownFixture(false, &fakePGOwnerRepo{owners: map[string]string{"alice_shop": "alice_app"}})
	r.reconcilePGReown(context.Background())
	if calls := pgReownCalls(ag); len(calls) != 0 {
		t.Fatalf("calls = %v, want none", calls)
	}
}

// No databases: nothing to send.
func TestReconcilePGReown_NoDatabasesSendsNothing(t *testing.T) {
	r, ag := pgReownFixture(true, &fakePGOwnerRepo{owners: map[string]string{}})
	r.reconcilePGReown(context.Background())
	if calls := pgReownCalls(ag); len(calls) != 0 {
		t.Fatalf("calls = %v, want none", calls)
	}
}

// A failed read sends nothing: a database sent without its user would go
// to its holder instead.
func TestReconcilePGReown_ListErrorSendsNothing(t *testing.T) {
	r, ag := pgReownFixture(true, &fakePGOwnerRepo{err: errors.New("db down")})
	r.reconcilePGReown(context.Background())
	if calls := pgReownCalls(ag); len(calls) != 0 {
		t.Fatalf("calls = %v, want none", calls)
	}
}

// A failed call is retried on the next tick.
func TestReconcilePGReown_FailedRunIsRetried(t *testing.T) {
	r, ag := pgReownFixture(true, &fakePGOwnerRepo{owners: map[string]string{"alice_shop": "alice_app"}})
	ag.failMethod = "db.postgres.reown_superuser_objects"
	r.reconcilePGReown(context.Background())
	ag.failMethod = ""
	r.reconcilePGReown(context.Background())
	if n := len(pgReownCalls(ag)); n != 2 {
		t.Fatalf("calls = %d, want the failed run retried", n)
	}
}

// An Agent from before the verb (mid-update) isn't asked again every tick.
func TestReconcilePGReown_OldAgentIsNotRetriedEveryTick(t *testing.T) {
	r, ag := pgReownFixture(true, &fakePGOwnerRepo{owners: map[string]string{"alice_shop": "alice_app"}})
	ag.errByMethod = map[string]error{"db.postgres.reown_superuser_objects": &agent.AgentError{Code: agent.CodeUnknownCommand, Message: "unknown command"}}
	r.reconcilePGReown(context.Background())
	r.reconcilePGReown(context.Background())
	if n := len(pgReownCalls(ag)); n != 1 {
		t.Fatalf("calls = %d, want 1", n)
	}
}

// ReconcileAll runs the pass.
func TestReconcileAll_RunsThePGReownPass(t *testing.T) {
	r, ag := pgReownFixture(true, &fakePGOwnerRepo{owners: map[string]string{"alice_shop": "alice_app"}})
	r.ReconcileAll(context.Background())
	if n := len(pgReownCalls(ag)); n != 1 {
		t.Fatalf("calls = %d, want 1", n)
	}
}
