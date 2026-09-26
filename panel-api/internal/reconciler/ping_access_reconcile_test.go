package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

type fakePingAccessRepo struct {
	names []string
	err   error
}

func (f *fakePingAccessRepo) ListPingAllowedUsernames(context.Context) ([]string, error) {
	return f.names, f.err
}

func pingFixture(repo *fakePingAccessRepo) (*Reconciler, *fakeAgent) {
	ag := &fakeAgent{}
	r := New(&fakeDomainRepo{}, &fakeUserRepo{}, ag, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Interval: time.Second}).
		WithPingAccess(repo)
	return r, ag
}

func pingCalls(ag *fakeAgent) []json.RawMessage {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	var out []json.RawMessage
	for _, c := range ag.calls {
		if c.method == "user.ping_access.apply" {
			raw, _ := json.Marshal(c.params)
			out = append(out, raw)
		}
	}
	return out
}

// The group's members are the users the repo lists, sorted, so the ledger
// fingerprint is the set and not the order the rows came back in.
func TestReconcilePingAccess_SendsTheSortedList(t *testing.T) {
	r, ag := pingFixture(&fakePingAccessRepo{names: []string{"bob", "alice"}})
	r.reconcilePingAccess(context.Background())
	calls := pingCalls(ag)
	if len(calls) != 1 || string(calls[0]) != `{"members":["alice","bob"]}` {
		t.Fatalf("calls = %s, want one call with alice,bob", calls)
	}
}

// No package allows ping: the list is sent as [] so the Agent clears the
// group. A JSON null would be refused by the Agent and leave old members in.
func TestReconcilePingAccess_NoUsersSendsAnEmptyList(t *testing.T) {
	r, ag := pingFixture(&fakePingAccessRepo{names: nil})
	r.reconcilePingAccess(context.Background())
	calls := pingCalls(ag)
	if len(calls) != 1 || string(calls[0]) != `{"members":[]}` {
		t.Fatalf("calls = %s, want one call with an empty list", calls)
	}
}

// A failed read sends nothing: sending an empty list would take ping away from
// every tenant that has it.
func TestReconcilePingAccess_ListErrorSendsNothing(t *testing.T) {
	r, ag := pingFixture(&fakePingAccessRepo{err: errors.New("db down")})
	r.reconcilePingAccess(context.Background())
	if calls := pingCalls(ag); len(calls) != 0 {
		t.Fatalf("calls = %s, want none", calls)
	}
}

// A steady tick sends nothing; a changed list is sent at once.
func TestReconcilePingAccess_SteadyTickSendsNothing(t *testing.T) {
	repo := &fakePingAccessRepo{names: []string{"alice"}}
	r, ag := pingFixture(repo)
	r.reconcilePingAccess(context.Background())
	r.reconcilePingAccess(context.Background())
	if calls := pingCalls(ag); len(calls) != 1 {
		t.Fatalf("calls after a steady tick = %d, want 1", len(calls))
	}
	repo.names = []string{"alice", "carol"}
	r.reconcilePingAccess(context.Background())
	if calls := pingCalls(ag); len(calls) != 2 {
		t.Fatalf("calls after a change = %d, want 2", len(calls))
	}
}

// A failed apply is retried on the next tick (the ledger is not stamped).
func TestReconcilePingAccess_FailedApplyIsRetried(t *testing.T) {
	r, ag := pingFixture(&fakePingAccessRepo{names: []string{"alice"}})
	ag.failMethod = "user.ping_access.apply"
	r.reconcilePingAccess(context.Background())
	ag.failMethod = ""
	r.reconcilePingAccess(context.Background())
	if calls := pingCalls(ag); len(calls) != 2 {
		t.Fatalf("calls = %d, want the failed apply retried", len(calls))
	}
}

// ReconcileAll runs the pass.
func TestReconcileAll_RunsThePingAccessPass(t *testing.T) {
	r, ag, _ := plannerFixture(t)
	r.WithPingAccess(&fakePingAccessRepo{names: []string{"alice"}})
	r.ReconcileAll(context.Background())
	if calls := pingCalls(ag); len(calls) != 1 {
		t.Fatalf("ping-access calls = %d, want 1", len(calls))
	}
}
