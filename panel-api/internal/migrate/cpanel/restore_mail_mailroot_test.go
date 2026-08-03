package cpanel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// JAB-152: ImportMailboxes used to return early whenever HomeDir was empty,
// which made mail import IMPOSSIBLE for any source whose tarball is
// metadata-only. Plesk's is exactly that by design — plesk/backup.go states
// "the homedir (docroots) and Maildirs are NOT bundled" — so a live Plesk
// migration moved 85 MB of files and 2 databases correctly and silently moved
// ZERO of 3 mailboxes, while reporting the job done.
//
// An explicit MailRoot is a complete answer on its own; the HomeDir check only
// ever existed to derive HomeDir/mail.
func TestImportMailboxesAcceptsExplicitMailRootWithoutHomeDir(t *testing.T) {
	t.Parallel()

	// A Maildir tree rsynced in from outside the tarball, exactly as the Plesk
	// and CyberPanel paths produce: <mailroot>/<domain>/<local>/{cur,new,tmp}.
	root := t.TempDir()
	box := filepath.Join(root, "example.com", "info")
	for _, sub := range []string{"cur", "new", "tmp"} {
		if err := os.MkdirAll(filepath.Join(box, sub), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(box, "cur", "1.msg"), []byte("From: a@b\n\nhi\n"), 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}

	parsed := &ParsedTarball{MailRoot: root} // HomeDir deliberately empty

	// agentCli nil = observation-only; enough to prove we get PAST the gate.
	res, err := ImportMailboxes(context.Background(), parsed, nil, "job1", nil, nil, false, "target")
	if err != nil {
		t.Fatalf("ImportMailboxes: %v", err)
	}
	for _, s := range res.Skipped {
		if strings.Contains(s, "no_homedir_in_tarball") {
			t.Fatalf("an explicit MailRoot must satisfy the gate, but import skipped with %q — "+
				"this is the bug that made Plesk mail migration impossible", s)
		}
	}
}

// With neither a MailRoot nor a HomeDir there is genuinely nothing to walk, and
// the skip must still be recorded rather than silently passing.
func TestImportMailboxesStillSkipsWithNeitherRoot(t *testing.T) {
	t.Parallel()

	res, err := ImportMailboxes(context.Background(), &ParsedTarball{}, nil, "job1", nil, nil, false, "target")
	if err != nil {
		t.Fatalf("ImportMailboxes: %v", err)
	}
	found := false
	for _, s := range res.Skipped {
		if strings.Contains(s, "no_homedir_in_tarball") {
			found = true
		}
	}
	if !found {
		t.Error("with no MailRoot and no HomeDir the skip must be recorded, not silent")
	}
}
