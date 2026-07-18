package backup

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// JAB-98: old job logs are pruned; a fresh (active-job) log and non-.log files
// are left alone.
func TestPruneJobLogsIn(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-120 * 24 * time.Hour)
	fresh := time.Now()

	mk := func(name string, mtime time.Time) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		return p
	}

	oldLog := mk("01OLDJOB.log", old)     // aged out -> removed
	activeLog := mk("01ACTIVE.log", fresh) // in-flight -> kept
	notLog := mk("keep.txt", old)          // not a .log -> kept

	n, err := pruneJobLogsIn(dir, DefaultJobLogRetention)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 pruned, got %d", n)
	}
	if _, err := os.Stat(oldLog); !os.IsNotExist(err) {
		t.Errorf("old log should be removed, stat err=%v", err)
	}
	if _, err := os.Stat(activeLog); err != nil {
		t.Errorf("active job log must be kept: %v", err)
	}
	if _, err := os.Stat(notLog); err != nil {
		t.Errorf("non-.log file must be kept: %v", err)
	}
}

// A missing logs dir is a no-op, not an error (no backups have run yet).
func TestPruneJobLogsIn_MissingDir(t *testing.T) {
	n, err := pruneJobLogsIn(filepath.Join(t.TempDir(), "nope"), time.Hour)
	if err != nil || n != 0 {
		t.Fatalf("missing dir should be (0, nil), got (%d, %v)", n, err)
	}
}
