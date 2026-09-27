package reconciler

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

func dbGrantEscapeCalls(ag *fakeAgent) int {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	n := 0
	for _, c := range ag.calls {
		if c.method == "db_user.escape_legacy_grants" {
			n++
		}
	}
	return n
}

func dbGrantEscapeFixture() (*Reconciler, *fakeAgent) {
	ag := &fakeAgent{}
	r := New(&fakeDomainRepo{}, &fakeUserRepo{}, ag, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Interval: time.Second}).
		WithDBGrantEscape()
	return r, ag
}

// The conversion runs on the first tick, and a steady tick inside the audit
// interval sends nothing.
func TestReconcileDBGrantEscape_RunsOnceThenWaitsForTheInterval(t *testing.T) {
	r, ag := dbGrantEscapeFixture()
	r.reconcileDBGrantEscape(context.Background())
	r.reconcileDBGrantEscape(context.Background())
	if n := dbGrantEscapeCalls(ag); n != 1 {
		t.Fatalf("calls = %d, want 1", n)
	}
}

// A failed conversion is retried on the next tick (the ledger is not stamped).
func TestReconcileDBGrantEscape_FailedRunIsRetried(t *testing.T) {
	r, ag := dbGrantEscapeFixture()
	ag.failMethod = "db_user.escape_legacy_grants"
	r.reconcileDBGrantEscape(context.Background())
	ag.failMethod = ""
	r.reconcileDBGrantEscape(context.Background())
	if n := dbGrantEscapeCalls(ag); n != 2 {
		t.Fatalf("calls = %d, want the failed run retried", n)
	}
}

// ReconcileAll runs the pass when it is enabled, and not otherwise.
func TestReconcileAll_RunsTheDBGrantEscapePass(t *testing.T) {
	r, ag, _ := plannerFixture(t)
	r.ReconcileAll(context.Background())
	if n := dbGrantEscapeCalls(ag); n != 0 {
		t.Fatalf("calls without WithDBGrantEscape = %d, want 0", n)
	}
	r2, ag2, _ := plannerFixture(t)
	r2.WithDBGrantEscape()
	r2.ReconcileAll(context.Background())
	if n := dbGrantEscapeCalls(ag2); n != 1 {
		t.Fatalf("calls = %d, want 1", n)
	}
}
