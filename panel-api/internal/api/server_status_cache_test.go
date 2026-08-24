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
			raw, _, err := sc.get("cpu", 30*time.Second, fetch)
			if err != nil {
				t.Errorf("get: %v", err)
			}
			results[i] = raw
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
	_, cached, _ := sc.get("host", 10*time.Second, fetch)
	if cached {
		t.Fatal("first call must be a miss")
	}
	// Within TTL (advance < ttl): hit → no fetch.
	cl.advance(9 * time.Second)
	raw, cached, _ := sc.get("host", 10*time.Second, fetch)
	if !cached {
		t.Fatal("call within TTL must be served from cache")
	}
	if string(raw) != `{"n":1}` {
		t.Fatalf("cached value wrong: %q", raw)
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
	if _, _, err := sc.get("svc", time.Minute, fetch); err == nil {
		t.Fatal("first get must surface the fetch error")
	}
	// Same TTL window, but the error was not cached → second get retries and
	// succeeds.
	raw, cached, err := sc.get("svc", time.Minute, fetch)
	if err != nil || cached || string(raw) != `{"ok":1}` {
		t.Fatalf("error must not be cached: raw=%q cached=%v err=%v", raw, cached, err)
	}
	if got := atomic.LoadInt32(&fetches); got != 2 {
		t.Fatalf("expected retry after error; fetches=%d want 2", got)
	}
}
