package commands

import (
	"os"
	"path/filepath"
	"testing"
)

// GH #357: fileSHA256 backs the requirements-hash marker that stops
// app.python.apply re-running `pip install -r requirements.txt` on every ~60s
// reconcile tick. It must be stable for identical content and differ when the
// file changes — that difference is exactly what re-triggers pip.
func TestFileSHA256(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "requirements.txt")

	if err := os.WriteFile(p, []byte("Django==5.0\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	a, err := fileSHA256(p)
	if err != nil {
		t.Fatalf("fileSHA256: %v", err)
	}
	if a == "" {
		t.Fatal("empty hash")
	}

	// Same content → same hash (a converged app skips pip).
	b, err := fileSHA256(p)
	if err != nil {
		t.Fatalf("fileSHA256 (2): %v", err)
	}
	if a != b {
		t.Fatalf("hash not stable for identical content: %s != %s", a, b)
	}

	// Changed content → different hash (tenant edited requirements → re-pip).
	if err := os.WriteFile(p, []byte("Django==5.0\nwagtail==6.0\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	c, err := fileSHA256(p)
	if err != nil {
		t.Fatalf("fileSHA256 (3): %v", err)
	}
	if c == a {
		t.Fatal("hash unchanged after content change — marker would wrongly skip pip")
	}

	// Missing file → error (caller stats first; this guards the contract).
	if _, err := fileSHA256(filepath.Join(dir, "nope.txt")); err == nil {
		t.Fatal("expected error hashing a missing file")
	}
}
