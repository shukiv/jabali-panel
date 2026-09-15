package api

import (
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// clock is a manually-advanced time source so TTL expiry is deterministic.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestCache(cl *clock) *statusCache {
	sc := newStatusCache()
	sc.now = cl.now
	return sc
}

// AC #1/#8: N concurrent misses for the same slice trigger AT MOST one fetch.
func TestStatusCache_SingleflightCollapsesConcurrent(t *testing.T) {
	cl := &clock{t: time.Unix(1_000_000, 0)}
	sc := newTestCache(cl)

	var fetches int32
	release := make(chan struct{})
	var entered sync.WaitGroup
	const N = 24
	entered.Add(N)

	fetch := func() (json.RawMessage, error) {
		atomic.AddInt32(&fetches, 1)
		<-release // hold the leader in-flight so followers queue behind it
		return json.RawMessage(`{"v":1}`), nil
	}

	results := make([]json.RawMessage, N)
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			entered.Done()
			r := sc.get("cpu", 30*time.Second, fetch)
			if r.err != nil {
				t.Errorf("get: %v", r.err)
			}
			results[i] = r.raw
		}(i)
	}
	entered.Wait()
	time.Sleep(30 * time.Millisecond) // let the goroutines reach sf.Do
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&fetches); got != 1 {
		t.Fatalf("expected exactly 1 fetch for %d concurrent callers, got %d", N, got)
	}
	for i, r := range results {
		if string(r) != `{"v":1}` {
			t.Fatalf("caller %d got %q, want the shared snapshot", i, string(r))
		}
	}
}

// AC #2/#3: a request inside the TTL reuses the cached slice — zero fetches.
func TestStatusCache_WithinTTLServesCached(t *testing.T) {
	cl := &clock{t: time.Unix(1_000_000, 0)}
	sc := newTestCache(cl)
	var fetches int32
	fetch := func() (json.RawMessage, error) {
		atomic.AddInt32(&fetches, 1)
		return json.RawMessage(`{"n":1}`), nil
	}

	// First call: miss → fetch.
	if sc.get("host", 10*time.Second, fetch).fromCache {
		t.Fatal("first call must be a miss")
	}
	// Within TTL (advance < ttl): hit → no fetch.
	cl.advance(9 * time.Second)
	r := sc.get("host", 10*time.Second, fetch)
	if !r.fromCache {
		t.Fatal("call within TTL must be served from cache")
	}
	if string(r.raw) != `{"n":1}` {
		t.Fatalf("cached value wrong: %q", r.raw)
	}
	if got := atomic.LoadInt32(&fetches); got != 1 {
		t.Fatalf("within-TTL call must not fetch; fetches=%d", got)
	}
}

// AC #3: once the TTL expires the slice refreshes exactly once more.
func TestStatusCache_ExpiredRefetches(t *testing.T) {
	cl := &clock{t: time.Unix(1_000_000, 0)}
	sc := newTestCache(cl)
	var fetches int32
	fetch := func() (json.RawMessage, error) {
		atomic.AddInt32(&fetches, 1)
		return json.RawMessage(`{}`), nil
	}
	sc.get("net", 5*time.Second, fetch) // miss → 1
	cl.advance(6 * time.Second)         // past TTL
	sc.get("net", 5*time.Second, fetch) // miss → 2
	if got := atomic.LoadInt32(&fetches); got != 2 {
		t.Fatalf("expired slice must refetch; fetches=%d want 2", got)
	}
}

// A failed fetch is NOT cached: the best-effort contract is preserved and the
// next request retries (no poisoned cache, no stale-error serve).
func TestStatusCache_ErrorNotCached(t *testing.T) {
	cl := &clock{t: time.Unix(1_000_000, 0)}
	sc := newTestCache(cl)
	var fetches int32
	boom := errors.New("agent down")
	fetch := func() (json.RawMessage, error) {
		n := atomic.AddInt32(&fetches, 1)
		if n == 1 {
			return nil, boom // first call fails
		}
		return json.RawMessage(`{"ok":1}`), nil
	}
	if sc.get("svc", time.Minute, fetch).err == nil {
		t.Fatal("first get must surface the fetch error")
	}
	// Same TTL window, but the error was not cached → second get retries and
	// succeeds. (No prior good value existed, so there is nothing to stale-serve.)
	r := sc.get("svc", time.Minute, fetch)
	if r.err != nil || r.fromCache || string(r.raw) != `{"ok":1}` {
		t.Fatalf("error must not be cached: raw=%q fromCache=%v err=%v", r.raw, r.fromCache, r.err)
	}
	if got := atomic.LoadInt32(&fetches); got != 2 {
		t.Fatalf("expected retry after error; fetches=%d want 2", got)
	}
}

// AC #5: a refresh that fails while the last-good value is still within
// ttl+maxStale serves that value flagged stale, carrying its original
// observed_at and the refresh error — the dashboard rides out a transient agent
// hiccup instead of blanking the slice.
func TestStatusCache_StaleServeWithinWindow(t *testing.T) {
	cl := &clock{t: time.Unix(1_000_000, 0)}
	sc := newTestCache(cl)
	firstAt := cl.now()
	good := json.RawMessage(`{"v":"good"}`)
	fail := false
	fetch := func() (json.RawMessage, error) {
		if fail {
			return nil, errors.New("agent down")
		}
		return good, nil
	}
	if r := sc.get("host", 10*time.Second, fetch); r.err != nil || r.stale {
		t.Fatalf("prime must be a clean fresh fetch: %+v", r)
	}
	// Expire the slice, then fail the refresh — but stay within ttl+maxStale.
	cl.advance(10*time.Second + 30*time.Second)
	fail = true
	r := sc.get("host", 10*time.Second, fetch)
	if r.err == nil {
		t.Fatal("a stale serve must still carry the refresh error")
	}
	if !r.stale {
		t.Fatal("a within-window failed refresh must serve the stale last-good value")
	}
	if string(r.raw) != string(good) {
		t.Fatalf("stale serve must return the last-good bytes, got %q", r.raw)
	}
	if !r.observedAt.Equal(firstAt) {
		t.Fatalf("observed_at must be the last-good fetch time; got %v want %v", r.observedAt, firstAt)
	}
	if m := sc.snapshot()["host"]; m.StaleServe != 1 {
		t.Fatalf("stale_serve counter = %d, want 1", m.StaleServe)
	}
}

// AC #5 bound: past ttl+maxStale the last-good value is abandoned and the error
// surfaces — a dead agent can never keep presenting an ageing snapshot.
func TestStatusCache_StaleServeBeyondWindowDrops(t *testing.T) {
	cl := &clock{t: time.Unix(1_000_000, 0)}
	sc := newTestCache(cl)
	good := json.RawMessage(`{"v":"good"}`)
	fail := false
	fetch := func() (json.RawMessage, error) {
		if fail {
			return nil, errors.New("agent down")
		}
		return good, nil
	}
	sc.get("host", 10*time.Second, fetch) // prime
	cl.advance(10*time.Second + maxStale + time.Second)
	fail = true
	r := sc.get("host", 10*time.Second, fetch)
	if r.err == nil {
		t.Fatal("beyond the stale window the refresh error must surface")
	}
	if r.stale || r.raw != nil {
		t.Fatalf("beyond the stale window nothing may be served; got stale=%v raw=%q", r.stale, r.raw)
	}
}

// AC #7: hit / miss / refresh / stale-serve / latency counters track cache
// behaviour per slice. Latency is measured on the injected clock so the fetch
// stub can simulate a fixed elapsed time deterministically.
func TestStatusCache_MetricsCounters(t *testing.T) {
	cl := &clock{t: time.Unix(1_000_000, 0)}
	sc := newTestCache(cl)
	fetch := func() (json.RawMessage, error) {
		cl.advance(7 * time.Millisecond) // simulated fetch latency
		return json.RawMessage(`{}`), nil
	}
	sc.get("cpu", 10*time.Second, fetch) // miss → refresh 1
	sc.get("cpu", 10*time.Second, fetch) // hit (within ttl, no fetch)
	cl.advance(11 * time.Second)         // expire
	sc.get("cpu", 10*time.Second, fetch) // miss → refresh 2

	m := sc.snapshot()["cpu"]
	if m.Hit != 1 || m.Miss != 2 || m.Refresh != 2 {
		t.Fatalf("counters: hit=%d miss=%d refresh=%d want 1/2/2", m.Hit, m.Miss, m.Refresh)
	}
	if m.RefreshLatencyCount != 2 || m.RefreshLatencyLastMs != 7 ||
		m.RefreshLatencyMaxMs != 7 || m.RefreshLatencyTotalMs != 14 {
		t.Fatalf("latency: count=%d last=%d max=%d total=%d want 2/7/7/14",
			m.RefreshLatencyCount, m.RefreshLatencyLastMs, m.RefreshLatencyMaxMs, m.RefreshLatencyTotalMs)
	}
}
