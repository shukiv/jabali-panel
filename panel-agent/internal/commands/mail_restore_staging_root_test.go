package commands

// GH #954 regression: account-restore materializes the mail Maildir under
// /var/lib/jabali-backups/restore-staging/<job>/mail, but importMaildirTree was
// called with migrationStagingRoots (only the /var/lib/…/migrations paths).
// filesafe.OpenInScope then refused every message-file open, so every
// account-restore + Jabali→Jabali migration silently imported 0 message bodies.
// These tests pin the fix: the staged message opens (and its Message-ID reads)
// ONLY when the staging root is in allowedRoots, and NOT under migrationStagingRoots.

import (
	"os"
	"path/filepath"
	"testing"
)

// stageAMessage lays down restore-staging-shaped Maildir with one message and
// returns (stagingRoot, messageFilePath).
func stageAMessage(t *testing.T) (string, string) {
	t.Helper()
	stagingRoot := t.TempDir()
	newDir := filepath.Join(stagingRoot, "mail", "run", "jabali-backup", "JOB", "mail", "ex.com", "box", "new")
	if err := os.MkdirAll(newDir, 0o750); err != nil {
		t.Fatal(err)
	}
	msg := filepath.Join(newDir, "eaaaaab.jabali,620")
	if err := os.WriteFile(msg, []byte("Message-ID: <mark@ex.com>\r\nSubject: hi\r\n\r\nbody\r\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	return stagingRoot, msg
}

func TestMailRestore_OpensOnlyUnderStagingRoot(t *testing.T) {
	stagingRoot, msg := stageAMessage(t)

	// Correct wiring (the fix): the job's own staging root is allowed → open works.
	f, err := openMaildirFileInStaging(msg, []string{stagingRoot})
	if err != nil {
		t.Fatalf("with stagingRoot allowed, open must succeed, got %v", err)
	}
	_ = f.Close()

	// The bug: migrationStagingRoots does NOT contain restore-staging → refused.
	if _, err := openMaildirFileInStaging(msg, migrationStagingRoots); err == nil {
		t.Error("with migrationStagingRoots (the old wiring), the staged message must be refused — that was the 0-messages bug")
	}
}

func TestMailRestore_MessageIDReadsOnlyInScope(t *testing.T) {
	stagingRoot, msg := stageAMessage(t)

	if mid := messageIDFromFile(msg, []string{stagingRoot}); mid != "mark@ex.com" {
		t.Errorf("in-scope Message-ID must read, got %q", mid)
	}
	// Out of scope → open fails → "" (dedup silently disabled, and — the real
	// bug — uploadBlob would fail too, dropping the message).
	if mid := messageIDFromFile(msg, migrationStagingRoots); mid != "" {
		t.Errorf("out-of-scope must yield empty Message-ID, got %q", mid)
	}
}
