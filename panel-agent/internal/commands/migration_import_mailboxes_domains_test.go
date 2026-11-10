package commands

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A migrated mail tree comes from the SOURCE account, so its <domain> dir
// names are source-controlled. With allowed_domains set, the walk must push
// only into the verified domains: a planted mail/<other-tenant-domain>/<box>
// or an owner address on a foreign domain must never reach Stalwart.

func writeMaildirMsg(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "new"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new", "1.host"), []byte("Subject: x\r\n\r\nbody\r\n"), 0o640); err != nil {
		t.Fatal(err)
	}
}

// stageMailTree lays down a cPanel-shaped ~/mail: an owner Maildir at the
// root (with a .Drafts subfolder), plus ex.com/box and foreign.com/admin.
func stageMailTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeMaildirMsg(t, root)
	if err := os.MkdirAll(filepath.Join(root, ".Drafts", "cur"), 0o750); err != nil {
		t.Fatal(err)
	}
	writeMaildirMsg(t, filepath.Join(root, "ex.com", "box"))
	writeMaildirMsg(t, filepath.Join(root, "foreign.com", "admin"))
	return root
}

// stubImportOne replaces importOneMailboxFn for the test and returns the
// addresses the walk tried to push into.
func stubImportOne(t *testing.T) *[]string {
	t.Helper()
	var pushed []string
	orig := importOneMailboxFn
	importOneMailboxFn = func(_ context.Context, email, _ string, _ []string) (int64, int64, []string, error) {
		pushed = append(pushed, email)
		return 1, 10, nil, nil
	}
	t.Cleanup(func() { importOneMailboxFn = orig })
	return &pushed
}

func TestImportMaildirTree_AllowedDomainsRefuseForeignDirsAndOwner(t *testing.T) {
	root := stageMailTree(t)
	pushed := stubImportOne(t)

	res, err := importMaildirTree(context.Background(), root, "owner@foreign.com", []string{root},
		map[string]bool{"ex.com": true})
	if err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(*pushed, ","); got != "box@ex.com" {
		t.Fatalf("pushed = %q, want only box@ex.com", got)
	}
	joined := strings.Join(res.Skipped, "\n")
	for _, want := range []string{
		"owner_mailbox owner@foreign.com: foreign.com is not a domain of the migrated account — refused",
		"mail/foreign.com: not a domain of the migrated account — refused",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("skipped lines missing %q; got:\n%s", want, joined)
		}
	}
	// The owner Maildir's own slots/subfolders are not domains: no refusal noise.
	for _, noise := range []string{"mail/.Drafts", "mail/new", "mail/cur"} {
		if strings.Contains(joined, noise) {
			t.Errorf("unexpected refusal line for %s; got:\n%s", noise, joined)
		}
	}
}

func TestImportMaildirTree_AllowedDomainsCaseInsensitive(t *testing.T) {
	root := t.TempDir()
	writeMaildirMsg(t, filepath.Join(root, "Ex.COM", "box"))
	pushed := stubImportOne(t)

	if _, err := importMaildirTree(context.Background(), root, "", []string{root},
		map[string]bool{"ex.com": true}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(*pushed, ","); got != "box@Ex.COM" {
		t.Fatalf("pushed = %q, want box@Ex.COM", got)
	}
}

// nil = unrestricted: the account-restore and IMAP callers walk an internally
// derived tree and keep the old behaviour.
func TestImportMaildirTree_NilAllowedDomainsWalksEverything(t *testing.T) {
	root := stageMailTree(t)
	pushed := stubImportOne(t)

	if _, err := importMaildirTree(context.Background(), root, "owner@foreign.com", []string{root}, nil); err != nil {
		t.Fatal(err)
	}
	got := append([]string(nil), *pushed...)
	sort.Strings(got)
	if strings.Join(got, ",") != "admin@foreign.com,box@ex.com,owner@foreign.com" {
		t.Fatalf("pushed = %v, want every mailbox", got)
	}
}
