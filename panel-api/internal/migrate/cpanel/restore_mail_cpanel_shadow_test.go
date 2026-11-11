package cpanel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testSrcSHA512 is a SHA-512-crypt string of the shape cPanel writes into
// <home>/etc/<domain>/shadow (low-entropy filler; never a real password).
var testSrcSHA512 = "$6$saltsalt$" + strings.Repeat("a", 86)

// buildCpanelOfflineTree lays out an extracted cpmove: homedir/mail with one
// Maildir for box@main.example, and homedir/etc/main.example/shadow holding
// shadowBody (no file when shadowBody is "").
func buildCpanelOfflineTree(t *testing.T, shadowBody string) *ParsedTarball {
	t.Helper()
	extract := t.TempDir()
	home := filepath.Join(extract, "cpmove-acct", "homedir")
	mailRoot := filepath.Join(home, "mail")
	for _, d := range []string{"cur", "new", "tmp"} {
		if err := os.MkdirAll(filepath.Join(mailRoot, "main.example", "box", d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	etc := filepath.Join(home, "etc", "main.example")
	if err := os.MkdirAll(etc, 0o750); err != nil {
		t.Fatal(err)
	}
	if shadowBody != "" {
		if err := os.WriteFile(filepath.Join(etc, "shadow"), []byte(shadowBody), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	return &ParsedTarball{ExtractDir: extract, HomeDir: home, MailRoot: mailRoot, SourceUser: "acct"}
}

func importOneCpanelMailbox(t *testing.T, parsed *ParsedTarball, preserve bool) (*fakeMailboxRepoMB, []string) {
	t.Helper()
	mb := &fakeMailboxRepoMB{}
	dom := &fakeDomainRepoMB{id: "01DOMAINID0000000000000000"}
	res, err := ImportMailboxes(context.Background(), parsed, stubMailAgent{}, "job1", mb, dom, preserve, "", fakeTargetUserID)
	if err != nil {
		t.Fatalf("ImportMailboxes: %v", err)
	}
	// box@ (the Maildir) comes first; any other shadow entry gets a row of
	// its own after it.
	if len(mb.created) == 0 || mb.created[0].LocalPart != "box" {
		t.Fatalf("want mailbox box@ first, got %+v", mb.created)
	}
	return mb, res.Skipped
}

func skippedContains(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

// The bug: cPanel's per-domain shadow was never read, so every cPanel mailbox
// got a fresh password even with --preserve-source-state. The source hash
// must land on the row, and never in the run summary.
func TestImportMailboxes_CpanelShadowHashPreserved(t *testing.T) {
	parsed := buildCpanelOfflineTree(t, "other:"+testSrcSHA512+":19000::::::\nbox:"+testSrcSHA512+":19000::::::\n")
	mb, skipped := importOneCpanelMailbox(t, parsed, true)
	if mb.created[0].PasswordHash != testSrcSHA512 {
		t.Fatalf("PasswordHash = %q, want the source SHA-512-crypt hash", mb.created[0].PasswordHash)
	}
	if !skippedContains(skipped, "SOURCE password preserved") {
		t.Fatalf("no 'SOURCE password preserved' line: %v", skipped)
	}
	if skippedContains(skipped, testSrcSHA512) || skippedContains(skipped, "$6$") {
		t.Fatalf("a hash reached the run summary: %v", skipped)
	}
}

// Preserve off: the source hash is never used.
func TestImportMailboxes_CpanelShadowIgnoredWhenPreserveOff(t *testing.T) {
	parsed := buildCpanelOfflineTree(t, "box:"+testSrcSHA512+":19000::::::\n")
	mb, skipped := importOneCpanelMailbox(t, parsed, false)
	if mb.created[0].PasswordHash == testSrcSHA512 {
		t.Fatal("source hash used with preserve off")
	}
	if !skippedContains(skipped, "use --preserve-source-state") {
		t.Fatalf("preserve-off notice missing: %v", skipped)
	}
}

// A shadow file that is a symlink is refused, not followed: a planted
// `shadow -> /etc/shadow` must never put a host hash on a mailbox.
func TestImportMailboxes_CpanelShadowSymlinkRefused(t *testing.T) {
	parsed := buildCpanelOfflineTree(t, "")
	outside := filepath.Join(t.TempDir(), "shadow")
	if err := os.WriteFile(outside, []byte("box:"+testSrcSHA512+":19000::::::\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(parsed.HomeDir, "etc", "main.example", "shadow")); err != nil {
		t.Fatal(err)
	}
	mb, skipped := importOneCpanelMailbox(t, parsed, true)
	if mb.created[0].PasswordHash == testSrcSHA512 {
		t.Fatal("hash read through a symlinked shadow file")
	}
	if !skippedContains(skipped, "mail_passwords: main.example: password file not read") {
		t.Fatalf("symlink refusal not reported: %v", skipped)
	}
}

// Same for a symlinked domain dir.
func TestImportMailboxes_CpanelShadowSymlinkedDomainDirRefused(t *testing.T) {
	parsed := buildCpanelOfflineTree(t, "")
	etcDom := filepath.Join(parsed.HomeDir, "etc", "main.example")
	if err := os.Remove(etcDom); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "shadow"), []byte("box:"+testSrcSHA512+":19000::::::\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, etcDom); err != nil {
		t.Fatal(err)
	}
	mb, skipped := importOneCpanelMailbox(t, parsed, true)
	if mb.created[0].PasswordHash == testSrcSHA512 {
		t.Fatal("hash read through a symlinked domain dir")
	}
	if !skippedContains(skipped, "mail_passwords: main.example: password file not read") {
		t.Fatalf("symlink refusal not reported: %v", skipped)
	}
}

// A symlink that stays inside the tree is refused too: one domain's shadow
// must not stand in for another's.
func TestImportMailboxes_CpanelShadowInTreeSymlinkRefused(t *testing.T) {
	parsed := buildCpanelOfflineTree(t, "")
	other := filepath.Join(parsed.HomeDir, "etc", "other.example")
	if err := os.MkdirAll(other, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "shadow"), []byte("box:"+testSrcSHA512+":19000::::::\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	etcDom := filepath.Join(parsed.HomeDir, "etc", "main.example")
	if err := os.Remove(etcDom); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("other.example", etcDom); err != nil {
		t.Fatal(err)
	}
	mb, skipped := importOneCpanelMailbox(t, parsed, true)
	if mb.created[0].PasswordHash == testSrcSHA512 {
		t.Fatal("hash read through an in-tree symlink")
	}
	if !skippedContains(skipped, "mail_passwords: main.example: password file not read") {
		t.Fatalf("symlink refusal not reported: %v", skipped)
	}
}

// An entry in a scheme Stalwart cannot verify is dropped, and the notice says
// so (not "no entry", not "use the flag").
func TestImportMailboxes_CpanelShadowUnverifiableSchemeNamed(t *testing.T) {
	parsed := buildCpanelOfflineTree(t, "box:{PLAIN}notahash:19000::::::\n")
	mb, skipped := importOneCpanelMailbox(t, parsed, true)
	if strings.Contains(mb.created[0].PasswordHash, "notahash") {
		t.Fatal("unverifiable value stored as the hash")
	}
	if !skippedContains(skipped, "scheme the mail server cannot verify") {
		t.Fatalf("unverifiable cause not named: %v", skipped)
	}
}

// An entry locked/suspended on the source (`!` or `*`) is its own cause, so
// the operator knows nothing failed.
func TestImportMailboxes_CpanelShadowLockedOnSourceNamed(t *testing.T) {
	for _, entry := range []string{"!" + testSrcSHA512, "*", "!!"} {
		parsed := buildCpanelOfflineTree(t, "box:"+entry+":19000::::::\n")
		mb, skipped := importOneCpanelMailbox(t, parsed, true)
		if mb.created[0].PasswordHash == testSrcSHA512 || strings.HasPrefix(mb.created[0].PasswordHash, "!") {
			t.Fatalf("%q: locked entry stored as the hash", entry)
		}
		if !skippedContains(skipped, "locked or suspended on the source") {
			t.Fatalf("%q: locked cause not named: %v", entry, skipped)
		}
	}
}

// The pull could not read a domain's shadow on the source: that cause is
// named, with the fix, instead of "no entry".
func TestImportMailboxes_CpanelShadowUnreadableOnSourceNamed(t *testing.T) {
	parsed := buildCpanelOfflineTree(t, "")
	parsed.MailShadowUnreadable = []string{"MAIN.example"}
	mb, skipped := importOneCpanelMailbox(t, parsed, true)
	if mb.created[0].PasswordHash == "" {
		t.Fatal("no fallback password set")
	}
	if !skippedContains(skipped, "was not readable by the source account") {
		t.Fatalf("unreadable cause not named: %v", skipped)
	}
}

// A local part that is not a valid mailbox name is skipped, never used as a key.
func TestCpanelMailShadow_BadLocalPartSkipped(t *testing.T) {
	parsed := buildCpanelOfflineTree(t, "../x:"+testSrcSHA512+"::\nBox:"+testSrcSHA512+"::\nok:"+testSrcSHA512+"::\n")
	m := mailShadowSourceFor(parsed).read("main.example")
	if !m.present {
		t.Fatal("shadow not read")
	}
	if _, bad := m.hashes["../x"]; bad {
		t.Fatal("path-like local part kept")
	}
	if m.hashes["ok"] != testSrcSHA512 {
		t.Fatalf("valid entry lost: %v", m.hashes)
	}
}

// An invalid domain name is refused before any path is built from it.
func TestCpanelMailShadow_InvalidDomainRefused(t *testing.T) {
	parsed := buildCpanelOfflineTree(t, "box:"+testSrcSHA512+"::\n")
	for _, d := range []string{"../etc", "a/b", "", "-x.example"} {
		m := mailShadowSourceFor(parsed).read(d)
		if m.present || len(m.hashes) != 0 {
			t.Fatalf("%q: read a file", d)
		}
		if m.refused == "" {
			t.Fatalf("%q: not refused", d)
		}
	}
}

// A homedir that is not inside the extract dir is never read.
func TestMailShadowSourceFor_HomeOutsideExtractNotRead(t *testing.T) {
	parsed := buildCpanelOfflineTree(t, "box:"+testSrcSHA512+"::\n")
	parsed.ExtractDir = t.TempDir()
	if src := mailShadowSourceFor(parsed); src.base != "" {
		t.Fatalf("base = %q, want none", src.base)
	}
}

// A staged pull dir wins over the extract tree.
func TestMailShadowSourceFor_StagedPullDir(t *testing.T) {
	staged := t.TempDir()
	if err := os.MkdirAll(filepath.Join(staged, "main.example"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staged, "main.example", "shadow"), []byte("box:"+testSrcSHA512+"::\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := mailShadowSourceFor(&ParsedTarball{MailShadowDir: staged}).read("main.example")
	if m.hashes["box"] != testSrcSHA512 {
		t.Fatalf("staged hash not read: %v (refused %q)", m.hashes, m.refused)
	}
}

// An SSH pull whose password-file copy failed must say so, not "no entry …
// re-running will not change this": the tarball's homedir is empty by design
// (--skiphomedir), so falling back to it reads nothing. Here the extract does
// hold a shadow file, and it must not be read in place of the failed copy.
func TestImportMailboxes_CpanelShadowCopyFailedNamed(t *testing.T) {
	parsed := buildCpanelOfflineTree(t, "box:"+testSrcSHA512+":19000::::::\n")
	parsed.MailShadowNotCopied = true
	mb, skipped := importOneCpanelMailbox(t, parsed, true)
	if mb.created[0].PasswordHash == testSrcSHA512 {
		t.Fatal("read the extract's homedir in place of the failed copy")
	}
	if !skippedContains(skipped, "mail password files were not copied") {
		t.Fatalf("copy-failed cause not named: %v", skipped)
	}
	if skippedContains(skipped, "will not change this") {
		t.Fatalf("told the operator a re-run is pointless: %v", skipped)
	}
}
