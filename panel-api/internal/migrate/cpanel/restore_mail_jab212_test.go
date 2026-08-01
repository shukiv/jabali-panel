package cpanel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// JAB-212. Account soomcoil restored with
//
//	mailboxes: count=2 messages_found=760 messages_pushed=787
//
// — 27 MORE messages pushed than were found, with no explanatory line, right
// after JAB-209 established that found==pushed is the attestation operators
// rely on. A counter that can exceed its own denominator is worth no more than
// no counter at all.
//
// The issue guessed cross-mailbox duplicate delivery. It is simpler and worse
// than that: the two sides walk different trees.
//
//   - The agent (importMaildirTree → importOneMailbox) imports the INBOX
//     *and every Maildir++ subfolder* — ".Sent", ".Trash", ".INBOX.Archive" —
//     each a sibling of cur/new holding its own cur/new.
//   - The panel-side counter walked only the root cur/ and new/.
//
// So every message in a Sent or Trash folder was pushed but never found. It
// takes a mailbox with subfolders to show up, which is why single-mailbox
// accounts in the same batch reconciled exactly.
//
// The same divergence has a second face: the agent's looksLikeMailMaildir
// treats a mailbox whose root cur/new are absent but which has a populated
// ".Sub" as a real maildir; the panel's looksLikeMaildir did not, so such a
// mailbox was missing from BOTH count= and messages_found= while its mail was
// imported regardless.

// writeSubfolderMsg creates a Maildir message file under maildir/sub.
func writeSubfolderMsg(t *testing.T, maildir, sub, name string) {
	t.Helper()
	dir := filepath.Join(maildir, sub)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("From: a@b\r\n\r\nbody"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCountMaildirMessages_CountsMaildirPlusPlusSubfolders(t *testing.T) {
	t.Parallel()

	md := t.TempDir()
	writeSubfolderMsg(t, md, "cur", "1700000000.M1:2,S")
	writeSubfolderMsg(t, md, "new", "1700000001.M2")
	// Subfolders the agent imports and the counter used to ignore.
	writeSubfolderMsg(t, md, ".Sent/cur", "1700000002.M3:2,S")
	writeSubfolderMsg(t, md, ".Sent/new", "1700000003.M4")
	writeSubfolderMsg(t, md, ".Trash/cur", "1700000004.M5:2,S")
	// Nested Maildir++ hierarchy — ".Parent.Child" is one flat directory.
	writeSubfolderMsg(t, md, ".INBOX.Archive.2024/cur", "1700000005.M6:2,S")

	count, bytes, _ := countMaildirMessagesDetailed(md)

	if count != 6 {
		t.Errorf("count = %d, want 6 (2 root + 4 in subfolders) — subfolder "+
			"messages are pushed by the agent, so not counting them is exactly "+
			"what made messages_pushed exceed messages_found", count)
	}
	if bytes == 0 {
		t.Error("bytes must include subfolder messages too")
	}
}

func TestCountMaildirMessages_SubfolderTmpStillExcluded(t *testing.T) {
	t.Parallel()

	// The JAB-209 rule has to hold inside subfolders as well, or the fix for
	// one direction re-creates the gap in the other.
	md := t.TempDir()
	writeSubfolderMsg(t, md, "cur", "1700000000.M1:2,S")
	writeSubfolderMsg(t, md, ".Sent/cur", "1700000001.M2:2,S")
	writeSubfolderMsg(t, md, ".Sent/tmp", "1700000002.M3") // interrupted delivery

	count, _, tmpFound := countMaildirMessagesDetailed(md)

	if count != 2 {
		t.Errorf("count = %d, want 2 — tmp/ inside a subfolder is still an "+
			"interrupted delivery, not mail the user has", count)
	}
	if tmpFound != 1 {
		t.Errorf("tmpFound = %d, want 1 — subfolder tmp/ must stay visible to "+
			"the caller", tmpFound)
	}
}

func TestCountMaildirMessages_IgnoresNonMaildirDotDirs(t *testing.T) {
	t.Parallel()

	// A dot-dir with no cur/new is not a Maildir++ folder; counting stray
	// files in it would push found above pushed in the other direction.
	md := t.TempDir()
	writeSubfolderMsg(t, md, "cur", "1700000000.M1:2,S")
	if err := os.MkdirAll(filepath.Join(md, ".nfs-junk"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(md, ".nfs-junk", "stray"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if count, _, _ := countMaildirMessagesDetailed(md); count != 1 {
		t.Errorf("count = %d, want 1 — a dot-dir without cur/new is not a "+
			"subfolder and the agent never visits it", count)
	}
}

func TestLooksLikeMaildir_SubfolderOnlyMailbox(t *testing.T) {
	t.Parallel()

	// Empty INBOX, mail only in .Archive. The agent imports this; the panel
	// used to not even count it, so the mailbox was absent from count= and
	// messages_found= while its messages landed in messages_pushed=.
	md := t.TempDir()
	writeSubfolderMsg(t, md, ".Archive/cur", "1700000000.M1:2,S")

	got, ok := looksLikeMaildir(md)
	if !ok {
		t.Fatal("subfolder-only mailbox not recognised — its mail is imported " +
			"but never counted, which reads as pushed > found")
	}
	if got != md {
		t.Errorf("maildir path = %q, want %q", got, md)
	}
	if count, _, _ := countMaildirMessagesDetailed(md); count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
}

func TestLooksLikeMaildir_StillRejectsNonMaildir(t *testing.T) {
	t.Parallel()

	// Guard against the subfolder probe turning every directory into a
	// mailbox — that would invent maildirs out of ordinary home folders.
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, ok := looksLikeMaildir(dir); ok {
		t.Error("a plain directory with a dotfile dir must not look like a maildir")
	}
}

func TestReconcileMailCounts_PushedExceedsFound(t *testing.T) {
	t.Parallel()

	// The reported shape: 760 found, 787 pushed. Before this it returned nil —
	// gap <= 0 was treated as "nothing to say" — so the operator saw two
	// numbers that could not both be right and no explanation at all.
	msgs := reconcileMailCounts(760, 787, nil)

	if len(msgs) != 1 {
		t.Fatalf("an inequality in EITHER direction needs a line, got %v", msgs)
	}
	got := msgs[0]
	if !strings.Contains(got, "27") {
		t.Errorf("must state the size of the discrepancy: %s", got)
	}
	// Not mail loss — saying WARNING here would train operators to ignore the
	// word on the runs where it does mean loss.
	if strings.Contains(got, "do not report this mailbox as fully migrated") {
		t.Errorf("pushed>found is an accounting mismatch, not loss: %s", got)
	}
	if !strings.Contains(got, "more message(s) were imported than") {
		t.Errorf("should say plainly that more was imported than staged: %s", got)
	}
}

func TestReconcileMailCounts_EqualStaysSilent(t *testing.T) {
	t.Parallel()

	if got := reconcileMailCounts(500, 500, nil); got != nil {
		t.Errorf("equality is the normal case and must stay silent, got %v", got)
	}
}
