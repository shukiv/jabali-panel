package directadmin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// When the backup's primary domain has no mail on the source, the account's
// own Maildir (from home.tar.zst) is not carried. It must not be left at the
// top of MailRoot either: that is where the cPanel-shaped mail import reads
// the owner's INBOX, and where its cur/new/tmp folders would sit next to the
// per-domain mail folders.
func TestNativeBackup_OwnerMaildirNotCarried_NothingStagedAtMailRoot(t *testing.T) {
	raw := daArchiveWithout(t, func(name string) bool {
		return strings.HasSuffix(name, "/email/passwd") || strings.HasSuffix(name, "/email/aliases") ||
			strings.HasPrefix(name, "imap/")
	})
	path := writeArchive(t, zstdBytes(t, raw))
	x := filepath.Join(t.TempDir(), "extract")
	nb, err := ParseNativeBackup(path, x)
	if err != nil {
		t.Fatal(err)
	}
	if nb.OwnerMaildir {
		t.Fatal("the owner Maildir is carried for a domain without mail")
	}
	for _, n := range []string{"cur", "new", "tmp", ".INBOX.spam"} {
		if exists(filepath.Join(x, "imap", n)) {
			t.Errorf("imap/%s is staged although the owner mailbox is not carried", n)
		}
	}
	if exists(filepath.Join(x, ownerMailStage)) {
		t.Errorf("the staged owner Maildir (%s/) was left in the extract dir", ownerMailStage)
	}
	if p := nb.ToCpanelParsed("newuser"); p.OwnerEmail != "" {
		t.Errorf("owner email %q for a mailbox that is not carried", p.OwnerEmail)
	}
}

// When the primary domain does have mail, the owner Maildir lands at the top
// of MailRoot, exactly where the mail import reads the owner's INBOX, and the
// staging folder is gone.
func TestNativeBackup_OwnerMaildirCarried_PlacedAtMailRootTop(t *testing.T) {
	x, nb := parseFixture(t, zstdBytes)
	if !nb.OwnerMaildir {
		t.Fatal("the owner Maildir is not carried for a mail domain")
	}
	for _, n := range []string{"new", ".INBOX.spam/cur"} {
		ents, err := os.ReadDir(filepath.Join(x, "imap", n))
		if err != nil || len(ents) == 0 {
			t.Errorf("imap/%s: %d message(s), err %v", n, len(ents), err)
		}
	}
	if exists(filepath.Join(x, ownerMailStage)) {
		t.Errorf("the staging folder %s/ was left behind", ownerMailStage)
	}
	if p := nb.ToCpanelParsed("newuser"); p.OwnerEmail == "" {
		t.Error("no owner email for a carried owner mailbox")
	}
}
