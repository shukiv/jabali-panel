package cpanel

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// writeMsg drops a fake Maildir message file of the given size.
func writeMsg(t *testing.T, dir, name string, size int) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestImportMailboxes_CountsOwnerTopLevelMaildir is the bug-1 regression
// guard. The cPanel default/system account's mail lives in the
// TOP-LEVEL Maildir (mail/cur, mail/new), not a per-domain subdir.
// Before the fix the count loop skipped mail/{cur,new,tmp} and reported
// messages_found=0 even when the owner Maildir held real mail — silent
// data loss masked by a clean-looking manifest.
func TestImportMailboxes_CountsOwnerTopLevelMaildir(t *testing.T) {
	home := t.TempDir()
	mailRoot := filepath.Join(home, "mail")

	// Owner/default account: 3 messages in the top-level Maildir.
	writeMsg(t, filepath.Join(mailRoot, "cur"), "1700000000.M1.host:2,S", 100)
	writeMsg(t, filepath.Join(mailRoot, "cur"), "1700000001.M2.host:2,S", 150)
	writeMsg(t, filepath.Join(mailRoot, "new"), "1700000002.M3.host", 200)

	// A per-domain mailbox alongside it: 1 message.
	writeMsg(t, filepath.Join(mailRoot, "example.com", "alice", "cur"), "1700000003.M4.host:2,S", 50)

	parsed := &ParsedTarball{
		SourceUser: "miguser",
		HomeDir:    home,
		MailRoot:   mailRoot,
		OwnerEmail: "miguser@example.com",
	}

	// agentCli nil → observation-only: counts then returns, no JMAP.
	res, err := ImportMailboxes(context.Background(), parsed, nil, "", nil, nil, false, "")
	if err != nil {
		t.Fatalf("ImportMailboxes: %v", err)
	}

	// 2 maildirs: the owner top-level + example.com/alice.
	if res.MaildirsFound != 2 {
		t.Errorf("MaildirsFound = %d, want 2 (owner + per-domain)", res.MaildirsFound)
	}
	// 4 messages: 3 owner + 1 per-domain.
	if res.MessagesFound != 4 {
		t.Errorf("MessagesFound = %d, want 4 (3 owner + 1 per-domain)", res.MessagesFound)
	}
	if res.BytesFound != 100+150+200+50 {
		t.Errorf("BytesFound = %d, want 500", res.BytesFound)
	}
}

// TestImportMailboxes_NoOwnerEmailSkipsTopLevel proves the owner count
// is gated on OwnerEmail — without it (older tarballs where the primary
// domain couldn't be resolved) the top-level Maildir is not counted, so
// we never invent an owner mailbox we can't address.
func TestImportMailboxes_NoOwnerEmailSkipsTopLevel(t *testing.T) {
	home := t.TempDir()
	mailRoot := filepath.Join(home, "mail")
	writeMsg(t, filepath.Join(mailRoot, "cur"), "1700000000.M1.host:2,S", 100)
	writeMsg(t, filepath.Join(mailRoot, "example.com", "alice", "cur"), "1700000003.M4.host:2,S", 50)

	parsed := &ParsedTarball{
		SourceUser: "miguser",
		HomeDir:    home,
		MailRoot:   mailRoot,
		// OwnerEmail intentionally empty.
	}
	res, err := ImportMailboxes(context.Background(), parsed, nil, "", nil, nil, false, "")
	if err != nil {
		t.Fatalf("ImportMailboxes: %v", err)
	}
	if res.MaildirsFound != 1 {
		t.Errorf("MaildirsFound = %d, want 1 (per-domain only)", res.MaildirsFound)
	}
	if res.MessagesFound != 1 {
		t.Errorf("MessagesFound = %d, want 1 (per-domain only)", res.MessagesFound)
	}
}
