package hostedsvc

import (
	"context"
	"database/sql"
	"log/slog"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newReapStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1) // in-memory sqlite: one conn = one database
	store, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// flakyDNS reuses fakeDNS for every method but lets a test force RemoveLabel
// to fail, so the reaper's "DNS must succeed before revoke" contract is
// exercised directly.
type flakyDNS struct {
	*fakeDNS
	failRemove bool
	removed    []string
}

func (f *flakyDNS) RemoveLabel(ctx context.Context, label string) error {
	if f.failRemove {
		return context.DeadlineExceeded
	}
	f.removed = append(f.removed, label)
	return f.fakeDNS.RemoveLabel(ctx, label)
}

// movedAt reads the raw moved_at for a label (NULL -> zero time).
func movedAt(t *testing.T, s *Store, label string) time.Time {
	t.Helper()
	var v sql.NullInt64
	if err := s.db.QueryRow(`SELECT moved_at FROM labels WHERE label = ?`, label).Scan(&v); err != nil {
		t.Fatalf("read moved_at %s: %v", label, err)
	}
	if !v.Valid {
		return time.Time{}
	}
	return time.Unix(v.Int64, 0)
}

func revoked(t *testing.T, s *Store, label string) bool {
	t.Helper()
	var v sql.NullInt64
	if err := s.db.QueryRow(`SELECT revoked_at FROM labels WHERE label = ?`, label).Scan(&v); err != nil {
		t.Fatalf("read revoked_at %s: %v", label, err)
	}
	return v.Valid
}

func seedLabel(t *testing.T, s *Store, label, ip string, at time.Time) {
	t.Helper()
	s.now = func() time.Time { return at }
	if err := s.CreateLabel(label, ip, "op@example.com", "hash-"+label); err != nil {
		t.Fatalf("seed %s: %v", label, err)
	}
}

// TestReclaimMovedMatrix covers the skip/reap decision surface: a label moved
// long ago is reaped; a fresh move, an active (never-moved) label, and an
// already-revoked label are all left alone.
func TestReclaimMovedMatrix(t *testing.T) {
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	s := newReapStore(t)

	run := base.Add(reclaimAfterMove + 24*time.Hour) // reap runs 8 days after base

	// eligible: moved at base -> moved_at is (reclaimAfterMove + 1d) old at run
	seedLabel(t, s, "10-0-0-1", "10.0.0.1", base)
	s.now = func() time.Time { return base }
	if err := s.MarkMoved("10-0-0-1"); err != nil {
		t.Fatal(err)
	}

	// fresh move: moved 1 day before the run (well inside reclaimAfterMove)
	seedLabel(t, s, "10-0-0-2", "10.0.0.2", base)
	s.now = func() time.Time { return run.Add(-24 * time.Hour) }
	if err := s.MarkMoved("10-0-0-2"); err != nil {
		t.Fatal(err)
	}

	// active: never moved
	seedLabel(t, s, "10-0-0-3", "10.0.0.3", base)

	// already revoked but also moved long ago
	seedLabel(t, s, "10-0-0-4", "10.0.0.4", base)
	s.now = func() time.Time { return base }
	if err := s.MarkMoved("10-0-0-4"); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeLabel("10-0-0-4", "admin"); err != nil {
		t.Fatal(err)
	}

	dns := &flakyDNS{fakeDNS: newFakeDNS()}
	dns.a["10-0-0-1"] = "10.0.0.1" // pretend the record exists so removal is observable
	n, err := ReclaimMoved(context.Background(), s, dns, slog.New(slog.DiscardHandler), run, false)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reaped %d, want 1 (only the long-moved live label)", n)
	}
	if !revoked(t, s, "10-0-0-1") {
		t.Error("10-0-0-1 should be reclaimed (revoked)")
	}
	if len(dns.removed) != 1 || dns.removed[0] != "10-0-0-1" {
		t.Errorf("DNS removed = %v, want [10-0-0-1]", dns.removed)
	}
	if revoked(t, s, "10-0-0-2") {
		t.Error("10-0-0-2 (fresh move) must not be reaped")
	}
	if revoked(t, s, "10-0-0-3") {
		t.Error("10-0-0-3 (active) must not be reaped")
	}
}

// TestReclaimMovedDNSFailureRetries is the contract that matters most: when
// DNS removal fails, the label is NOT revoked (it stays on the worklist) and a
// later run with working DNS reclaims it. Revoking on DNS failure would strand
// the record forever.
func TestReclaimMovedDNSFailureRetries(t *testing.T) {
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	s := newReapStore(t)
	seedLabel(t, s, "10-0-0-1", "10.0.0.1", base)
	s.now = func() time.Time { return base }
	if err := s.MarkMoved("10-0-0-1"); err != nil {
		t.Fatal(err)
	}
	run := base.Add(reclaimAfterMove + 24*time.Hour)
	dns := &flakyDNS{fakeDNS: newFakeDNS(), failRemove: true}

	n, err := ReclaimMoved(context.Background(), s, dns, slog.New(slog.DiscardHandler), run, false)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("reaped %d on DNS failure, want 0", n)
	}
	if revoked(t, s, "10-0-0-1") {
		t.Fatal("label revoked despite DNS removal failing — record would be stranded")
	}
	if movedAt(t, s, "10-0-0-1").IsZero() {
		t.Fatal("moved_at cleared despite DNS failure — clock lost, never retried")
	}

	// DNS recovers; next tick reclaims it.
	dns.failRemove = false
	n, err = ReclaimMoved(context.Background(), s, dns, slog.New(slog.DiscardHandler), run.Add(24*time.Hour), false)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || !revoked(t, s, "10-0-0-1") {
		t.Fatalf("second run: reaped %d revoked=%v, want 1/true", n, revoked(t, s, "10-0-0-1"))
	}
}

// TestReclaimMovedDryRun reports the worklist without touching DNS or the store.
func TestReclaimMovedDryRun(t *testing.T) {
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	s := newReapStore(t)
	seedLabel(t, s, "10-0-0-1", "10.0.0.1", base)
	s.now = func() time.Time { return base }
	if err := s.MarkMoved("10-0-0-1"); err != nil {
		t.Fatal(err)
	}
	dns := &flakyDNS{fakeDNS: newFakeDNS()}
	n, err := ReclaimMoved(context.Background(), s, dns, slog.New(slog.DiscardHandler),
		base.Add(reclaimAfterMove+24*time.Hour), true)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("dry-run counted %d, want 1", n)
	}
	if revoked(t, s, "10-0-0-1") {
		t.Error("dry-run must not revoke")
	}
	if len(dns.removed) != 0 {
		t.Error("dry-run must not touch DNS")
	}
}

// TestMarkMovedIdempotent: a second MarkMoved must not reset the reclaim clock,
// or a box that keeps heartbeating the old token through the move would push
// the deadline out forever.
func TestMarkMovedIdempotent(t *testing.T) {
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	s := newReapStore(t)
	seedLabel(t, s, "10-0-0-1", "10.0.0.1", base)

	s.now = func() time.Time { return base }
	if err := s.MarkMoved("10-0-0-1"); err != nil {
		t.Fatal(err)
	}
	first := movedAt(t, s, "10-0-0-1")

	s.now = func() time.Time { return base.Add(6 * 24 * time.Hour) } // 6 days later, still moved
	if err := s.MarkMoved("10-0-0-1"); err != nil {
		t.Fatal(err)
	}
	if got := movedAt(t, s, "10-0-0-1"); !got.Equal(first) {
		t.Fatalf("moved_at moved from %v to %v — clock reset", first, got)
	}
}

// TestClearMovedAfterFlap: the box's IP matches again, so a pending reclaim is
// cancelled and the label is no longer on the worklist.
func TestClearMovedAfterFlap(t *testing.T) {
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	s := newReapStore(t)
	seedLabel(t, s, "10-0-0-1", "10.0.0.1", base)
	s.now = func() time.Time { return base }
	if err := s.MarkMoved("10-0-0-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearMoved("10-0-0-1"); err != nil {
		t.Fatal(err)
	}
	if !movedAt(t, s, "10-0-0-1").IsZero() {
		t.Fatal("moved_at not cleared after flap")
	}
	got, err := s.MovedLabelsBefore(base.Add(100 * 24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("worklist = %v, want empty after clear", got)
	}
}

// TestHeartbeatStampsAndClearsMoved exercises the api.go wiring: a heartbeat
// from a different source IP stamps moved_at (and reports ip_moved); a
// heartbeat from the original IP clears it again.
func TestHeartbeatStampsAndClearsMoved(t *testing.T) {
	a, _, mailer := newTestAPI(t)
	h := a.Routes()
	label, token := registerAndClaim(t, a, mailer, "op@example.com", "45.79.1.9")

	// Heartbeat from a NEW address (public, non-bogon so LabelFromIP accepts
	// it): ip_moved + moved_at stamped.
	_, out := call(t, h, "/v1/heartbeat", "45.79.1.10", TokenRequest{Token: token})
	if out["ip_moved"] != true {
		t.Fatalf("expected ip_moved=true, got %v", out["ip_moved"])
	}
	work, err := a.Store.MovedLabelsBefore(time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 1 || work[0] != label {
		t.Fatalf("worklist after move = %v, want [%s]", work, label)
	}

	// Heartbeat from the ORIGINAL address: moved_at cleared.
	_, out = call(t, h, "/v1/heartbeat", "45.79.1.9", TokenRequest{Token: token})
	if out["ip_moved"] == true {
		t.Fatal("ip_moved should be false from the original address")
	}
	work, err = a.Store.MovedLabelsBefore(time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 0 {
		t.Fatalf("worklist after return = %v, want empty", work)
	}
}
