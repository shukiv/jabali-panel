package commands

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDuSubdirSizes_Recursive guards GH #657: du must follow the
// /proc/self/fd/3 magic symlink and report each child's RECURSIVE size.
// The regression it catches — running du without -D — returned the symlink
// node's own size (~tens of bytes) and no children, so every folder read 0.
func TestDuSubdirSizes_Recursive(t *testing.T) {
	withRealExec(t) // GH #994: needs the real read-only command (stub returns empty output)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "beta", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(p string, n int) {
		if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "alpha", "a.bin"), 2_000_000)
	write(filepath.Join(root, "beta", "b.bin"), 5_000_000)
	write(filepath.Join(root, "beta", "nested", "c.bin"), 1_000_000) // recursion check

	f, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	sizes, timedOut := duSubdirSizes(context.Background(), f, root)
	if timedOut {
		t.Fatal("unexpected timeout on a small tree")
	}

	// >= (not ==) tolerates the directory nodes' own apparent size, which
	// varies by filesystem. The point is that they are NOT ~0 (the bug).
	if got := sizes[filepath.Join(root, "alpha")]; got < 2_000_000 {
		t.Errorf("alpha size = %d, want >= 2000000 (0-ish means the du-fd deref bug is back)", got)
	}
	if got := sizes[filepath.Join(root, "beta")]; got < 6_000_000 {
		t.Errorf("beta size = %d, want >= 6000000 (5MB + 1MB nested — recursion broken?)", got)
	}
	if got := sizes[root]; got < 8_000_000 {
		t.Errorf("root total = %d, want >= 8000000", got)
	}
}

// TestDuSubdirSizes_Timeout guards GH #657: when du is killed by a fired
// deadline (directory too large), duSubdirSizes must report timedOut=true so
// the handler returns a clean error rather than silently-partial sizes.
func TestDuSubdirSizes_Timeout(t *testing.T) {
	root := t.TempDir()
	f, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, timedOut := duSubdirSizes(ctx, f, root); !timedOut {
		t.Fatal("expected timedOut=true for an already-expired context")
	}
}
