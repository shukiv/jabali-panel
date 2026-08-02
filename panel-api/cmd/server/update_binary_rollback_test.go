package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeBin(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// The core promise: after a failed migration the bytes on disk are the bytes
// that were there before the update swapped them.
func TestBinarySnapshotRestoresPreviousBytes(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	panelBin := filepath.Join(dir, "jabali-panel")
	agentBin := filepath.Join(dir, "jabali-agent")
	writeBin(t, panelBin, "OLD-PANEL", 0o755)
	writeBin(t, agentBin, "OLD-AGENT", 0o755)

	snap, err := snapshotBinaries([]string{panelBin, agentBin})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	defer snap.cleanup()

	// The update swaps in new builds...
	writeBin(t, panelBin, "NEW-PANEL", 0o755)
	writeBin(t, agentBin, "NEW-AGENT", 0o755)

	// ...then `migrate up` fails.
	if err := snap.restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}

	for path, want := range map[string]string{panelBin: "OLD-PANEL", agentBin: "OLD-AGENT"} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if string(got) != want {
			t.Errorf("%s = %q, want %q — a failed migration must leave the host on "+
				"the build it was running, not new code in front of an old schema",
				filepath.Base(path), got, want)
		}
	}
}

// The restored file has to still be executable, or the rollback trades a schema
// mismatch for a host that cannot run the panel at all.
func TestBinarySnapshotRestoresExecutableMode(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bin := filepath.Join(dir, "jabali-panel")
	writeBin(t, bin, "OLD", 0o755)

	snap, err := snapshotBinaries([]string{bin})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	defer snap.cleanup()

	writeBin(t, bin, "NEW", 0o755)
	if err := snap.restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}

	info, err := os.Stat(bin)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("restored binary mode %v is not executable", info.Mode().Perm())
	}
}

// jabali-mailhook is absent wherever the mail module was never provisioned, so
// an uninstalled path is an ordinary state and must not abort the snapshot —
// otherwise every no-mail host loses rollback coverage entirely.
func TestSnapshotSkipsBinariesNotInstalled(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	present := filepath.Join(dir, "jabali-panel")
	writeBin(t, present, "OLD", 0o755)
	absent := filepath.Join(dir, "jabali-mailhook")

	snap, err := snapshotBinaries([]string{present, absent})
	if err != nil {
		t.Fatalf("an absent binary must not fail the snapshot: %v", err)
	}
	defer snap.cleanup()

	if _, ok := snap.files[absent]; ok {
		t.Error("a binary that was never installed must not be snapshotted")
	}
	if _, ok := snap.files[present]; !ok {
		t.Error("the installed binary must be snapshotted")
	}

	// Restoring must not resurrect a file that was never there.
	if err := snap.restore(); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(absent); !errors.Is(err, os.ErrNotExist) {
		t.Error("restore recreated a binary that was not installed before the update")
	}
}

func TestBinarySnapshotCleanupIsIdempotent(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bin := filepath.Join(dir, "jabali-panel")
	writeBin(t, bin, "OLD", 0o755)

	snap, err := snapshotBinaries([]string{bin})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	snapDir := snap.dir
	snap.cleanup()
	snap.cleanup() // deferred cleanup + the migrate step's cleanup both run

	if _, err := os.Stat(snapDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("snapshot dir %s survived cleanup", snapDir)
	}
	// A nil snapshot is the "could not snapshot" path; it must not panic.
	var nilSnap *binarySnapshot
	nilSnap.cleanup()
}

// When the snapshot was never taken, the rollback cannot happen — and that has
// to read as the more serious state, because it is the one that needs hands on
// the box.
func TestRollbackWithoutSnapshotIsLouderThanTheMigrationFailure(t *testing.T) {
	t.Parallel()

	var nilSnap *binarySnapshot
	err := rollbackAfterFailedMigrate(nilSnap, errors.New("dirty database version 243"), func(string, ...any) {})
	if err == nil {
		t.Fatal("want an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "could not be restored") {
		t.Errorf("must say the restore did not happen, got: %v", err)
	}
	if !strings.Contains(msg, "Do not") || !strings.Contains(msg, "restart") {
		t.Errorf("must warn against restarting into the mismatch, got: %v", err)
	}
	if !strings.Contains(msg, "dirty database version 243") {
		t.Errorf("must carry the original migrate error, got: %v", err)
	}
}

// On the good path the operator should be told the host is consistent — the
// difference between "your update failed" and "your update failed and your
// server is now in an unknown state" is the whole point of this code.
func TestRollbackAfterFailedMigrateReportsConsistentHost(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	bin := filepath.Join(dir, "jabali-panel")
	writeBin(t, bin, "OLD", 0o755)
	snap, err := snapshotBinaries([]string{bin})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	defer snap.cleanup()
	writeBin(t, bin, "NEW", 0o755)

	var logged []string
	err = rollbackAfterFailedMigrate(snap, errors.New("no migration found for version 242"),
		func(format string, args ...any) { logged = append(logged, format) })
	if err == nil {
		t.Fatal("the update still failed, so it must still return an error")
	}
	if !strings.Contains(err.Error(), "rolled back") {
		t.Errorf("must say the swap was undone, got: %v", err)
	}
	if !strings.Contains(err.Error(), "no migration found for version 242") {
		t.Errorf("must carry the original migrate error, got: %v", err)
	}
	if len(logged) == 0 {
		t.Error("the rollback should be visible in the update output, not silent")
	}
	if got, _ := os.ReadFile(bin); string(got) != "OLD" {
		t.Errorf("binary = %q, want the pre-update bytes", got)
	}
}

// managedBinaries is what the rollback covers; it has to stay in step with
// what the release installer actually swaps, or an update replaces a binary
// that no rollback puts back.
func TestManagedBinariesCoverEveryInstalledBinary(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("update_release.go")
	if err != nil {
		t.Fatalf("read update_release.go: %v", err)
	}
	for _, name := range []string{"jabali-panel", "jabali-agent", "jabali-ssh-shell", "jabali-mailhook"} {
		if !strings.Contains(string(body), `"`+name+`"`) {
			t.Errorf("%s is in managedBinaries but the release installer no longer "+
				"mentions it — the two lists have drifted", name)
		}
		found := false
		for _, p := range managedBinaries {
			if filepath.Base(p) == name {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is installed by the release path but is not in managedBinaries, "+
				"so a failed migration would not roll it back", name)
		}
	}
}
