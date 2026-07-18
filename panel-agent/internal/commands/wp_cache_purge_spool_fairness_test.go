package commands

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withSpool points the watcher at a temp dir and stubs the per-file handler with
// a counter, restoring both after the test.
func withSpool(t *testing.T) (string, *int) {
	t.Helper()
	dir := t.TempDir()
	origDir, origCollect := wpPurgeSpoolDir, collectWpPurgeFile
	wpPurgeSpoolDir = dir
	var processed int
	// Stub the per-file collector so the fairness caps (applied before
	// coalescing) are what these tests measure.
	collectWpPurgeFile = func(_ string, _ *slog.Logger) *wpPurgeItem { processed++; return nil }
	t.Cleanup(func() { wpPurgeSpoolDir = origDir; collectWpPurgeFile = origCollect })
	return dir, &processed
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// JAB-63: one tenant flooding the shared spool can consume at most its per-UID
// fair share of a tick; the rest are deferred (not processed) — proving another
// tenant's single purge always has budget.
func TestWpPurgeTick_PerUIDFairShare(t *testing.T) {
	dir, processed := withSpool(t)
	// All files are owned by the test user (one UID), so the per-UID cap applies.
	for i := 0; i < 200; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("r%d.json", i)), []byte(`{"host":"x"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runWpPurgeTick(context.Background(), quietLog())
	if *processed != wpPurgeMaxPerUIDTick {
		t.Fatalf("processed=%d, want per-UID cap %d (one tenant must not exceed its share)", *processed, wpPurgeMaxPerUIDTick)
	}
}

// JAB-63: non-regular files are removed, never processed.
func TestWpPurgeTick_RejectsNonRegular(t *testing.T) {
	dir, processed := withSpool(t)
	link := filepath.Join(dir, "evil.json")
	if err := os.Symlink("/etc/shadow", link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	runWpPurgeTick(context.Background(), quietLog())
	if *processed != 0 {
		t.Fatalf("processed=%d, want 0 (symlink must not be handled)", *processed)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Fatalf("symlink should have been removed, lstat err=%v", err)
	}
}

// JAB-63: stale files are reaped so a flood can't fill /run.
func TestWpPurgeTick_ReapsStale(t *testing.T) {
	dir, processed := withSpool(t)
	stale := filepath.Join(dir, "old.json")
	if err := os.WriteFile(stale, []byte(`{"host":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-wpPurgeStaleAfter - time.Minute)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	runWpPurgeTick(context.Background(), quietLog())
	if *processed != 0 {
		t.Fatalf("processed=%d, want 0 (stale file must not be handled)", *processed)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale file should have been reaped, stat err=%v", err)
	}
}
