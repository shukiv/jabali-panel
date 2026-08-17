package commands

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDirSizeBytes_SumsTree(t *testing.T) {
	withRealExec(t) // GH #994: needs the real read-only command (stub returns empty output)
	dir := t.TempDir()
	// 3 KiB across two files in a subdir — du counts file data, so assert
	// a lower bound rather than an exact figure (block rounding + dir
	// overhead vary by filesystem).
	sub := filepath.Join(dir, "vol")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "a.bin"), make([]byte, 2048), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "b.bin"), make([]byte, 1024), 0o640); err != nil {
		t.Fatal(err)
	}
	n, err := dirSizeBytes(context.Background(), dir)
	if err != nil {
		t.Fatalf("dirSizeBytes: %v", err)
	}
	if n < 3072 {
		t.Errorf("size = %d, want >= 3072 (2K + 1K of file data)", n)
	}
}

func TestDirSizeBytes_InstallDirIsPositive(t *testing.T) {
	withRealExec(t) // GH #994: needs the real read-only command (stub returns empty output)
	// du -sb reports apparent bytes: a truly-empty dir is 0. A real
	// install dir is never empty though — it always holds compose.yml
	// (+ .env), so its du is > 0. The reconciler's >0 guard relies on
	// that: 0 means du failed (best-effort), not "an empty install".
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte("services: {}"), 0o640); err != nil {
		t.Fatal(err)
	}
	n, err := dirSizeBytes(context.Background(), dir)
	if err != nil {
		t.Fatalf("dirSizeBytes: %v", err)
	}
	if n <= 0 {
		t.Errorf("install dir size = %d, want > 0", n)
	}
}

func TestDirSizeBytes_MissingDirErrors(t *testing.T) {
	_, err := dirSizeBytes(context.Background(), filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Error("expected error for missing dir, got nil")
	}
}
