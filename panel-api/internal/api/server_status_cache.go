package api

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// statusCache is the JAB-373 Host Observation Snapshot cache: one
// process-wide, per-slice TTL cache with singleflight in front of the
// server-status aggregator's agent fan-out. It exists because the admin
// dashboard polls GET /admin/server-status every ~5s and the header mounts
// it on every admin page at 30s — each poll fanned out 9 agent subprocess
// calls, so N tabs / N operators multiplied identical host work (≈960–5,760
// agent calls/hour per viewer). The cache collapses that two ways:
//
//   - per-slice TTL: a request inside a slice's freshness window reuses the
//     last snapshot and makes zero agent calls (AC #2/#3);
//   - singleflight: N concurrent requests that all find a slice expired
//     trigger at most one refresh for it, not N (AC #1/#8).
//
// AC #5 (stale-serve): a successful fetch is retained even after its TTL
// expires. If a later refresh fails while the last-good value is still within
// ttl+maxStale, get serves that value flagged stale (with its observed_at)
// instead of dropping the slice — so a transient agent hiccup does not blank
// the dashboard. Beyond ttl+maxStale the stale value is abandoned and the
// error surfaces as before, so a dead agent can never present a healthy
// snapshot indefinitely. Stale data is display-only: the aggregator keeps it
// out of alert synthesis (see server_status.go).
//
// AC #7 (metrics): per-slice counters (hit, miss, refresh, stale-serve, and
// refresh latency) are recorded on every access and exposed read-only via
// snapshot(). Counter semantics are caller-facing and pinned here:
//   - hit    — this get served a fresh cached slice with no fetch;
//   - miss   — this get did not find a fresh slice (it fetched, waited on a
//     singleflight refresh, served stale, or errored);
//   - refresh — an agent fetch actually ran (leader only, once per batch);
//   - stale_serve — a last-good value was served on a failed refresh (once
//     per batch);
//   - refresh latency — wall time of each fetch (count/total/last/max ms).
//
// hit + miss == total gets; refresh ≤ miss.
type statusCache struct {
	mu   sync.Mutex
	data map[string]statusEntry
	sf   singleflight.Group
	now  func() time.Time // injectable clock for tests

	mmu     sync.Mutex
	metrics map[string]*sliceMetrics
}

type statusEntry struct {
	raw json.RawMessage
	at  time.Time
}

// sliceResult is the outcome of one cache get. raw is the slice body (fresh or
// last-good); observedAt is when that body was fetched; stale marks a last-good
// value served because the refresh failed; fromCache marks a fresh value served
// without a fetch; err is non-nil only when no value could be served at all.
type sliceResult struct {
	raw        json.RawMessage
	observedAt time.Time
	stale      bool
	fromCache  bool
	err        error
}

// sliceMetrics is the per-slice counter set exposed by AC #7. Latency is kept
// as count/total/last/max in whole milliseconds rather than a histogram — the
// tuning this feeds (migration step 5) needs central tendency and worst case,
// not distribution shape.
type sliceMetrics struct {
	Hit                   uint64 `json:"hit"`
	Miss                  uint64 `json:"miss"`
	Refresh               uint64 `json:"refresh"`
	StaleServe            uint64 `json:"stale_serve"`
	RefreshLatencyCount   uint64 `json:"refresh_latency_count"`
	RefreshLatencyTotalMs uint64 `json:"refresh_latency_total_ms"`
	RefreshLatencyLastMs  uint64 `json:"refresh_latency_last_ms"`
	RefreshLatencyMaxMs   uint64 `json:"refresh_latency_max_ms"`
}

// maxStale bounds how long past a slice's TTL a last-good value may still be
// served on a failed refresh (AC #5). It is a deliberate knob: large enough to
// ride out a brief agent restart or a slow systemctl, small enough that a
// genuinely dead agent stops presenting an ageing snapshot. The stale flag and
// observed_at travel with the value so a consumer always knows how old it is.
const maxStale = 2 * time.Minute

func newStatusCache() *statusCache {
	return &statusCache{
		data:    make(map[string]statusEntry),
		now:     time.Now,
		metrics: make(map[string]*sliceMetrics),
	}
}

// get returns the named slice. It serves a fresh cached copy (no fetch) when
// one is within ttl; otherwise it refreshes via fetch under singleflight, so
// concurrent misses for the same name collapse to one fetch. On a failed
// refresh it serves the last-good value flagged stale when that value is still
// within ttl+maxStale, else it returns the error. Every access is metered.
//
// fetch MUST use a request-independent context (e.g. context.Background with
// its own timeout): the result is shared across every concurrent caller, so
// binding it to one caller's request ctx would let that caller's cancellation
// fail the others.
func (sc *statusCache) get(name string, ttl time.Duration, fetch func() (json.RawMessage, error)) sliceResult {
	sc.mu.Lock()
	if e, ok := sc.data[name]; ok && sc.now().Sub(e.at) < ttl {
		r := sliceResult{raw: e.raw, observedAt: e.at, fromCache: true}
		sc.mu.Unlock()
		sc.recordAccess(name, true)
		return r
	}
	sc.mu.Unlock()

	v, _, _ := sc.sf.Do(name, func() (interface{}, error) {
		// A leader may have refreshed this slice while we were queued behind
		// its singleflight call; re-check before spending another fetch.
		sc.mu.Lock()
		if e, ok := sc.data[name]; ok && sc.now().Sub(e.at) < ttl {
			r := sliceResult{raw: e.raw, observedAt: e.at, fromCache: true}
			sc.mu.Unlock()
			return r, nil
		}
		sc.mu.Unlock()

		start := sc.now()
		raw, ferr := fetch()
		sc.recordRefresh(name, sc.now().Sub(start))
		if ferr != nil {
			// AC #5: serve the last-good value if it is still within the stale
			// window; otherwise abandon it and surface the error as before.
			sc.mu.Lock()
			e, ok := sc.data[name]
			within := ok && sc.now().Sub(e.at) <= ttl+maxStale
			raw, at := e.raw, e.at
			sc.mu.Unlock()
			if within {
				sc.recordStaleServe(name)
				return sliceResult{raw: raw, observedAt: at, stale: true, err: ferr}, nil
			}
			return sliceResult{err: ferr}, nil
		}
		at := sc.now()
		sc.mu.Lock()
		sc.data[name] = statusEntry{raw: raw, at: at}
		sc.mu.Unlock()
		return sliceResult{raw: raw, observedAt: at}, nil
	})

	r := v.(sliceResult)
	sc.recordAccess(name, r.fromCache)
	return r
}

// metricFor returns the counter row for a slice, creating it on first use.
// Caller holds mmu.
func (sc *statusCache) metricFor(name string) *sliceMetrics {
	m := sc.metrics[name]
	if m == nil {
		m = &sliceMetrics{}
		sc.metrics[name] = m
	}
	return m
}

// recordAccess counts one caller-facing hit or miss.
func (sc *statusCache) recordAccess(name string, hit bool) {
	sc.mmu.Lock()
	defer sc.mmu.Unlock()
	m := sc.metricFor(name)
	if hit {
		m.Hit++
	} else {
		m.Miss++
	}
}

// recordRefresh counts one actual fetch and folds in its latency.
func (sc *statusCache) recordRefresh(name string, d time.Duration) {
	ms := uint64(d.Milliseconds())
	sc.mmu.Lock()
	defer sc.mmu.Unlock()
	m := sc.metricFor(name)
	m.Refresh++
	m.RefreshLatencyCount++
	m.RefreshLatencyTotalMs += ms
	m.RefreshLatencyLastMs = ms
	if ms > m.RefreshLatencyMaxMs {
		m.RefreshLatencyMaxMs = ms
	}
}

// recordStaleServe counts one last-good-on-failure serve.
func (sc *statusCache) recordStaleServe(name string) {
	sc.mmu.Lock()
	defer sc.mmu.Unlock()
	sc.metricFor(name).StaleServe++
}

// snapshot returns a copy of the per-slice counters for the metrics endpoint.
func (sc *statusCache) snapshot() map[string]sliceMetrics {
	sc.mmu.Lock()
	defer sc.mmu.Unlock()
	out := make(map[string]sliceMetrics, len(sc.metrics))
	for k, m := range sc.metrics {
		out[k] = *m
	}
	return out
}

// detachedTimeout is the per-slice agent-call deadline used inside a fetch.
// Kept equal to the old per-sub-call timeout; lives on a background context so
// a cancelled poller cannot abort a refresh other pollers are waiting on.
func detachedTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// Per-slice TTLs (JAB-373). Volatile slices stay short; host/unit state can be
// longer; software keeps its existing ~5-minute cadence. Even a short TTL
// collapses the concurrent multi-tab / multi-operator polls that singleflight
// targets, and the 30s header cadence reuses whatever the 5s dashboard poll
// just refreshed.
const (
	ttlHost       = 15 * time.Second
	ttlCPU        = 3 * time.Second
	ttlNetwork    = 3 * time.Second
	ttlProcesses  = 5 * time.Second
	ttlServices   = 15 * time.Second
	ttlUserSlices = 15 * time.Second
	ttlSoftware   = 5 * time.Minute
	ttlNginx      = 15 * time.Second
	ttlAppArmor   = 30 * time.Second
)
