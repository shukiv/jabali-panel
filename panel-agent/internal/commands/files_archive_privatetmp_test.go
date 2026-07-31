package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// GH #756: the download archive must be staged OUTSIDE /tmp. jabali-agent and
// panel-api both run PrivateTmp=yes, so a /tmp path the agent writes is
// invisible to panel-api (it opens its own private /tmp) → os.Open returns
// "no such file or directory" and the folder/file download 500s. Guard the
// location so a future refactor can't silently move it back under /tmp.
func TestArchiveStagingRoot_NotUnderTmp(t *testing.T) {
	if strings.HasPrefix(filepath.Clean(archiveStagingRoot)+"/", "/tmp/") || archiveStagingRoot == "/tmp" {
		t.Fatalf("archiveStagingRoot must be outside /tmp (PrivateTmp boundary, GH #756); got %q", archiveStagingRoot)
	}
	if !filepath.IsAbs(archiveStagingRoot) {
		t.Fatalf("archiveStagingRoot must be absolute; got %q", archiveStagingRoot)
	}
}

// reapStaleArchives + archiveStagingBytesInUse must both operate on
// archiveStagingRoot — if one still globbed /tmp while writes went elsewhere,
// the budget accounting and orphan reaping would silently no-op.
func TestArchiveReapAndAccountingUseStagingRoot(t *testing.T) {
	orig := archiveStagingRoot
	t.Cleanup(func() { archiveStagingRoot = orig })
	archiveStagingRoot = t.TempDir()

	fresh := filepath.Join(archiveStagingRoot, "jabali-archive-fresh.tar.gz")
	stale := filepath.Join(archiveStagingRoot, "jabali-archive-stale.tar.gz")
	if err := os.WriteFile(fresh, []byte("1234"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("5678"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Age the stale one well past the 15m reap cutoff.
	old := time.Now().Add(-30 * time.Minute)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	if got := archiveStagingBytesInUse(); got != 8 {
		t.Fatalf("archiveStagingBytesInUse: want 8 bytes across both files, got %d", got)
	}

	reapStaleArchives()

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale archive should have been reaped, stat err=%v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh archive must survive the reap, got %v", err)
	}
}

// ensureArchiveStagingRoot must create the dir when missing (best-effort chown
// is ignored on hosts without the jabali user, e.g. CI).
func TestEnsureArchiveStagingRoot_CreatesDir(t *testing.T) {
	orig := archiveStagingRoot
	t.Cleanup(func() { archiveStagingRoot = orig })
	archiveStagingRoot = filepath.Join(t.TempDir(), "nested", "uploads")

	if err := ensureArchiveStagingRoot(); err != nil {
		t.Fatalf("ensureArchiveStagingRoot: %v", err)
	}
	fi, err := os.Stat(archiveStagingRoot)
	if err != nil || !fi.IsDir() {
		t.Fatalf("staging root not created: err=%v", err)
	}
}
