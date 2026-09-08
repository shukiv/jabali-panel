package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1439: `jabali disk-usage refresh-all` is the daily snapshot sweep. Its
// safety rests on being SERIAL and on the fresh-skip gate (JAB-273 — a
// concurrent nightly du fan-out wedged a swapless box). These pin that the sweep
// skips fresh snapshots, tolerates a single failing tenant, skips accounts with
// no Linux username, and calls each tenant exactly once in order.

type raSnapshotRepo struct {
	repository.DiskUsageSnapshotRepository
	byUser map[string]*models.DiskUsageSnapshot
}

func (r *raSnapshotRepo) Get(_ context.Context, userID string) (*models.DiskUsageSnapshot, error) {
	if s, ok := r.byUser[userID]; ok && s != nil {
		return s, nil
	}
	return nil, repository.ErrNotFound
}

func uname(s string) *string { return &s }

func tenants(names ...*string) []models.User {
	out := make([]models.User, len(names))
	for i, n := range names {
		id := "u" + string(rune('0'+i))
		out[i] = models.User{ID: id, Username: n}
	}
	return out
}

func TestRefreshAll_SkipsFreshTolerantSerial(t *testing.T) {
	fresh := &models.DiskUsageSnapshot{ComputedAt: time.Now().Add(-1 * time.Hour)}  // within max-age
	stale := &models.DiskUsageSnapshot{ComputedAt: time.Now().Add(-48 * time.Hour)} // older than max-age
	snaps := &raSnapshotRepo{byUser: map[string]*models.DiskUsageSnapshot{
		"u0": fresh, // skip (fresh)
		"u2": stale, // refresh (stale)
		// u1 has no snapshot → refresh; u3 fails; u4 has no username → skipped silently
	}}
	users := tenants(uname("alice"), uname("bob"), uname("carol"), uname("dave"), nil)

	var calls []string
	refreshOne := func(_ context.Context, userID string) error {
		calls = append(calls, userID)
		if userID == "u3" { // dave: one bad home must not abort the sweep
			return errors.New("du timed out")
		}
		return nil
	}

	r, err := runDiskUsageRefreshAll(context.Background(), users, snaps,
		refreshAllOpts{maxAge: 20 * time.Hour, betweenUsers: 0}, refreshOne)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// bob (no snap) + carol (stale) refreshed; dave failed; alice fresh-skipped;
	// the username-less tenant is not counted as a skip (it has nothing to du).
	if r.refreshed != 2 || r.skipped != 1 || r.failed != 1 {
		t.Fatalf("got refreshed=%d skipped=%d failed=%d; want 2/1/1", r.refreshed, r.skipped, r.failed)
	}
	// refreshOne is called only for the tenants that need it, in list order, and
	// NEVER for the fresh one — the fresh-skip gate must short-circuit before it.
	want := []string{"u1", "u2", "u3"}
	if len(calls) != len(want) {
		t.Fatalf("refreshOne calls = %v; want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("refreshOne calls = %v; want %v (order matters — serial)", calls, want)
		}
	}
}

// maxAge=0 disables the fresh-skip gate: every tenant with a home is refreshed,
// even one measured seconds ago.
func TestRefreshAll_MaxAgeZeroAlwaysRefreshes(t *testing.T) {
	snaps := &raSnapshotRepo{byUser: map[string]*models.DiskUsageSnapshot{
		"u0": {ComputedAt: time.Now()}, // brand new — still refreshed when maxAge=0
	}}
	users := tenants(uname("alice"), uname("bob"))

	n := 0
	refreshOne := func(_ context.Context, _ string) error { n++; return nil }

	r, err := runDiskUsageRefreshAll(context.Background(), users, snaps,
		refreshAllOpts{maxAge: 0, betweenUsers: 0}, refreshOne)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.refreshed != 2 || r.skipped != 0 || n != 2 {
		t.Fatalf("got refreshed=%d skipped=%d calls=%d; want 2/0/2", r.refreshed, r.skipped, n)
	}
}

// A cancelled context stops the sweep promptly instead of grinding the whole
// fleet (operator Ctrl-C / shutdown).
func TestRefreshAll_ContextCancelStops(t *testing.T) {
	snaps := &raSnapshotRepo{byUser: map[string]*models.DiskUsageSnapshot{}}
	users := tenants(uname("alice"), uname("bob"), uname("carol"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	n := 0
	refreshOne := func(_ context.Context, _ string) error { n++; return nil }
	_, err := runDiskUsageRefreshAll(ctx, users, snaps,
		refreshAllOpts{maxAge: 20 * time.Hour, betweenUsers: 0}, refreshOne)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v; want context.Canceled", err)
	}
	if n != 0 {
		t.Fatalf("refreshOne called %d times after cancel; want 0", n)
	}
}
