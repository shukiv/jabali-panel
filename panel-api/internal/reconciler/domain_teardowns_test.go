package reconciler

// JAB-236 sweep properties:
//  1. A pending tombstone is driven through the shared executor and
//     cleared on success.
//  2. Live-row guard: a tombstone whose domain name has a LIVE row is
//     dropped WITHOUT any agent call — it is stale (crash in the
//     create-tombstone→delete-row gap, refused delete, or the name was
//     re-registered before a failed teardown retried). Acting on it would
//     tear down a serving site.
//  3. A failed teardown keeps the tombstone and reports it as pending so
//     the orphan sweep stays quiet about it.
//  4. Backoff: a recently-attempted tombstone is skipped this tick.
//  5. The orphan report fires ONCE per set change, not per site per tick.

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// memTeardowns is a race-safe in-memory DomainTeardownRepository.
type memTeardowns struct {
	mu   sync.Mutex
	rows map[string]*models.DomainTeardown
}

func newMemTeardowns(names ...string) *memTeardowns {
	m := &memTeardowns{rows: map[string]*models.DomainTeardown{}}
	for _, n := range names {
		m.rows[n] = &models.DomainTeardown{DomainName: n, CreatedAt: time.Now().UTC()}
	}
	return m
}

func (m *memTeardowns) Ensure(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.rows[name]; !ok {
		m.rows[name] = &models.DomainTeardown{DomainName: name, CreatedAt: time.Now().UTC()}
	}
	return nil
}

func (m *memTeardowns) List(_ context.Context) ([]models.DomainTeardown, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]models.DomainTeardown, 0, len(m.rows))
	for _, r := range m.rows {
		out = append(out, *r)
	}
	return out, nil
}

func (m *memTeardowns) Delete(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, name)
	return nil
}

func (m *memTeardowns) MarkAttempt(_ context.Context, name, lastError string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.rows[name]; ok {
		r.Attempts++
		r.LastError = lastError
		now := time.Now().UTC()
		r.LastAttemptAt = &now
	}
	return nil
}

func (m *memTeardowns) has(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.rows[name]
	return ok
}

func teardownSweepFixture(t *testing.T, tombNames ...string) (*Reconciler, *fakeAgent, *memTeardowns, *fakeDomainRepo) {
	t.Helper()
	dr := newFakeDomainRepo()
	ag := &fakeAgent{}
	tombs := newMemTeardowns(tombNames...)
	r := New(dr, nil, ag, slog.Default(), Config{}).WithDomainTeardowns(tombs)
	return r, ag, tombs, dr
}

func TestTeardownSweep_ClearsOnSuccess(t *testing.T) {
	r, ag, tombs, _ := teardownSweepFixture(t, "gone.example")

	pending := r.processDomainTeardowns(context.Background())

	if !agentCalled(ag, "domain.delete") || !agentCalled(ag, "dns.zone.delete") {
		t.Fatal("sweep must drive the full teardown for a pending tombstone")
	}
	if tombs.has("gone.example") {
		t.Fatal("tombstone must be cleared after a successful teardown")
	}
	if len(pending) != 0 {
		t.Fatalf("nothing should remain pending: %v", pending)
	}
}

func TestTeardownSweep_LiveRowGuard(t *testing.T) {
	r, ag, tombs, dr := teardownSweepFixture(t, "alive.example")
	dr.domains["d1"] = &models.Domain{ID: "d1", Name: "alive.example", IsEnabled: true}

	r.processDomainTeardowns(context.Background())

	if len(ag.calls) != 0 {
		t.Fatal("a tombstone with a LIVE row must trigger NO agent call — it is stale, and executing it would tear down a serving site (worst case the panel's own primary)")
	}
	if tombs.has("alive.example") {
		t.Fatal("stale tombstone must be dropped")
	}
}

func TestTeardownSweep_FailureKeepsPending(t *testing.T) {
	r, ag, tombs, _ := teardownSweepFixture(t, "gone.example")
	ag.failMethod = "domain.delete"

	pending := r.processDomainTeardowns(context.Background())

	if !tombs.has("gone.example") {
		t.Fatal("tombstone must survive a failed retry")
	}
	if !pending["gone.example"] {
		t.Fatal("a failed teardown must be reported pending so the orphan sweep skips it")
	}
	tombs.mu.Lock()
	attempts := tombs.rows["gone.example"].Attempts
	tombs.mu.Unlock()
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestTeardownSweep_BacksOffRecentAttempts(t *testing.T) {
	r, ag, tombs, _ := teardownSweepFixture(t, "gone.example")
	recent := time.Now().UTC().Add(-time.Minute)
	tombs.rows["gone.example"].LastAttemptAt = &recent

	pending := r.processDomainTeardowns(context.Background())

	if len(ag.calls) != 0 {
		t.Fatal("a recently-attempted tombstone must be skipped this tick")
	}
	if !pending["gone.example"] {
		t.Fatal("a backed-off tombstone is still pending (the orphan sweep must skip it)")
	}
}

// countingHandler counts slog records at Warn level.
type countingHandler struct {
	mu    sync.Mutex
	warns int
}

func (h *countingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *countingHandler) Handle(_ context.Context, rec slog.Record) error {
	if rec.Level == slog.LevelWarn {
		h.mu.Lock()
		h.warns++
		h.mu.Unlock()
	}
	return nil
}
func (h *countingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *countingHandler) WithGroup(string) slog.Handler      { return h }

func TestReportOrphanSites_WarnsOncePerSetChange(t *testing.T) {
	h := &countingHandler{}
	r := New(newFakeDomainRepo(), nil, &fakeAgent{}, slog.New(h), Config{})

	r.reportOrphanSites([]string{"b.example", "a.example"})
	r.reportOrphanSites([]string{"a.example", "b.example"}) // same set, different order
	r.reportOrphanSites([]string{"a.example", "b.example"})
	if h.warns != 1 {
		t.Fatalf("warns = %d, want 1 — the per-tick warning storm is the bug this fixes", h.warns)
	}

	r.reportOrphanSites([]string{"a.example"}) // set changed
	if h.warns != 2 {
		t.Fatalf("warns = %d, want 2 after a set change", h.warns)
	}

	r.reportOrphanSites(nil) // cleared — informational, not a warning
	if h.warns != 2 {
		t.Fatalf("warns = %d, want 2 after clearing", h.warns)
	}
}
