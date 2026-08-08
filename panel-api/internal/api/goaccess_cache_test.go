package api

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// statOf writes a file with the given content and returns its FileInfo.
func statOf(t *testing.T, path, content string) os.FileInfo {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// resetGoaccessCache isolates each test from the package-level cache.
func resetGoaccessCache() {
	goaccessCache.mu.Lock()
	defer goaccessCache.mu.Unlock()
	goaccessCache.entries = map[string]*goaccessCacheEntry{}
	goaccessCache.inflight = map[string]*sync.Mutex{}
}

// TestGoAccessCacheServesWithinTTL pins the core win: a poller hitting the
// endpoint repeatedly inside the TTL causes exactly one goaccess run.
func TestGoAccessCacheServesWithinTTL(t *testing.T) {
	resetGoaccessCache()
	logPath := filepath.Join(t.TempDir(), "access.log")
	st := statOf(t, logPath, "line one\n")

	base := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	calls := 0
	render := func(context.Context, string) ([]byte, error) {
		calls++
		return []byte("<html>render</html>"), nil
	}

	// The modal polls every 10s; three polls inside the 30s TTL.
	for i, offset := range []time.Duration{0, 10 * time.Second, 20 * time.Second} {
		now := func() time.Time { return base.Add(offset) }
		out, err := renderGoAccessCached(context.Background(), logPath, st, now, render)
		if err != nil {
			t.Fatalf("poll %d: %v", i, err)
		}
		if string(out) != "<html>render</html>" {
			t.Fatalf("poll %d: unexpected body %q", i, out)
		}
	}
	if calls != 1 {
		t.Fatalf("goaccess ran %d times across 3 polls in one TTL, want 1", calls)
	}
}

// TestGoAccessCacheRerendersWhenLogChangedAfterTTL: past the TTL, a log that
// actually grew must be re-parsed (the dashboard has to show new traffic).
func TestGoAccessCacheRerendersWhenLogChangedAfterTTL(t *testing.T) {
	resetGoaccessCache()
	logPath := filepath.Join(t.TempDir(), "access.log")
	st1 := statOf(t, logPath, "line one\n")

	base := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	calls := 0
	render := func(context.Context, string) ([]byte, error) {
		calls++
		return []byte("<html>render</html>"), nil
	}

	if _, err := renderGoAccessCached(context.Background(), logPath, st1,
		func() time.Time { return base }, render); err != nil {
		t.Fatal(err)
	}
	// Log grows; poll after the TTL.
	st2 := statOf(t, logPath, "line one\nline two\n")
	if _, err := renderGoAccessCached(context.Background(), logPath, st2,
		func() time.Time { return base.Add(goaccessCacheTTL + time.Second) }, render); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("goaccess ran %d times, want 2 (changed log past TTL must re-render)", calls)
	}
}

// TestGoAccessCacheSkipsRerenderForIdleLog: past the TTL but the log is
// byte-identical — re-parsing would produce the same HTML, so don't.
func TestGoAccessCacheSkipsRerenderForIdleLog(t *testing.T) {
	resetGoaccessCache()
	logPath := filepath.Join(t.TempDir(), "access.log")
	st := statOf(t, logPath, "line one\n")

	base := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	calls := 0
	render := func(context.Context, string) ([]byte, error) {
		calls++
		return []byte("<html>render</html>"), nil
	}

	if _, err := renderGoAccessCached(context.Background(), logPath, st,
		func() time.Time { return base }, render); err != nil {
		t.Fatal(err)
	}
	if _, err := renderGoAccessCached(context.Background(), logPath, st,
		func() time.Time { return base.Add(10 * goaccessCacheTTL) }, render); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("goaccess ran %d times on an unchanged log, want 1", calls)
	}
}

// TestGoAccessCacheSingleFlight: concurrent viewers landing on a cold entry
// must produce one goaccess process, not one per viewer.
func TestGoAccessCacheSingleFlight(t *testing.T) {
	resetGoaccessCache()
	logPath := filepath.Join(t.TempDir(), "access.log")
	st := statOf(t, logPath, "line one\n")

	now := func() time.Time { return time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC) }
	var mu sync.Mutex
	calls := 0
	render := func(context.Context, string) ([]byte, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		time.Sleep(20 * time.Millisecond) // hold the slot so the others pile up
		return []byte("<html>render</html>"), nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := renderGoAccessCached(context.Background(), logPath, st, now, render); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("goaccess ran %d times for 8 concurrent viewers, want 1", calls)
	}
}

// TestGoAccessCacheDoesNotCacheErrors: a failed render must not be stored,
// or one transient failure would be served for a whole TTL.
func TestGoAccessCacheDoesNotCacheErrors(t *testing.T) {
	resetGoaccessCache()
	logPath := filepath.Join(t.TempDir(), "access.log")
	st := statOf(t, logPath, "line one\n")

	now := func() time.Time { return time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC) }
	calls := 0
	render := func(context.Context, string) ([]byte, error) {
		calls++
		if calls == 1 {
			return nil, context.DeadlineExceeded
		}
		return []byte("<html>ok</html>"), nil
	}

	if _, err := renderGoAccessCached(context.Background(), logPath, st, now, render); err == nil {
		t.Fatal("first render should have returned the error")
	}
	out, err := renderGoAccessCached(context.Background(), logPath, st, now, render)
	if err != nil {
		t.Fatalf("second render: %v", err)
	}
	if string(out) != "<html>ok</html>" {
		t.Fatalf("got %q — a failed render must not be cached", out)
	}
}
