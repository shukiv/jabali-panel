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
// Only successful fetches are cached. On a fetch error the caller keeps the
// existing best-effort contract (slice absent + an `errors` entry) and the
// next request retries — so an erroring slice never serves a poisoned cache,
// at the cost of no singleflight saving while it is failing (the rare case).
type statusCache struct {
	mu   sync.Mutex
	data map[string]statusEntry
	sf   singleflight.Group
	now  func() time.Time // injectable clock for tests
}

type statusEntry struct {
	raw json.RawMessage
	at  time.Time
}

func newStatusCache() *statusCache {
	return &statusCache{data: make(map[string]statusEntry), now: time.Now}
}

// get returns the named slice, refreshing it via fetch only when the cached
// copy is older than ttl. Concurrent misses for the same name collapse to one
// fetch (singleflight). The bool is true when the value was served from cache
// (no fetch ran) — used by tests/metrics, ignored by the aggregator.
//
// fetch MUST use a request-independent context (e.g. context.Background with
// its own timeout): the result is shared across every concurrent caller, so
// binding it to one caller's request ctx would let that caller's cancellation
// fail the others.
func (sc *statusCache) get(name string, ttl time.Duration, fetch func() (json.RawMessage, error)) (json.RawMessage, bool, error) {
	sc.mu.Lock()
	if e, ok := sc.data[name]; ok && sc.now().Sub(e.at) < ttl {
		raw := e.raw
		sc.mu.Unlock()
		return raw, true, nil
	}
	sc.mu.Unlock()

	v, err, _ := sc.sf.Do(name, func() (interface{}, error) {
		// A leader may have refreshed this slice while we were queued behind
		// its singleflight call; re-check before spending another fetch.
		sc.mu.Lock()
		if e, ok := sc.data[name]; ok && sc.now().Sub(e.at) < ttl {
			raw := e.raw
			sc.mu.Unlock()
			return raw, nil
		}
		sc.mu.Unlock()

		raw, ferr := fetch()
		if ferr != nil {
			return json.RawMessage(nil), ferr
		}
		sc.mu.Lock()
		sc.data[name] = statusEntry{raw: raw, at: sc.now()}
		sc.mu.Unlock()
		return raw, nil
	})
	if err != nil {
		return nil, false, err
	}
	return v.(json.RawMessage), false, nil
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
