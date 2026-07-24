package commands

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestDuSubdirSizes_Recursive guards GH #657: du must follow the
// /proc/self/fd/3 magic symlink and report each child's RECURSIVE size.
// The regression it catches — running du without -D — returned the symlink
// node's own size (~tens of bytes) and no children, so every folder read 0.
func TestDuSubdirSizes_Recursive(t *testing.T) {
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

	sizes := duSubdirSizes(context.Background(), f, root)

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
