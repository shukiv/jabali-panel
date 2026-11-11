package cpanel

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #327 Bug B: HestiaCP creates a mail account dir with `mkdir mail/<dom>/<acct>`,
// and dovecot only materializes cur/new/tmp on first delivery/login. A freshly
// created account is therefore a BARE dir that looksLikeMaildir rejects — before
// the fix only accounts that had received mail imported ("1 of 3"). The account
// list must come from conf/passwd, not the maildir walk.
func TestInsertMailboxPanelRows_HestiaFreshAccountsFromPasswd(t *testing.T) {
	home := t.TempDir()
	mailRoot := filepath.Join(home, "mail")
	dom := "testsite.local"
	confDir := filepath.Join(mailRoot, dom, "conf")
	if err := os.MkdirAll(confDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// conf/passwd lists 3 accounts (real Hestia dovecot passwd-file format).
	passwd := "sales:" + testSrcBcrypt + ":itflowtest:mail::/home/itflowtest:0:\n" +
		"billing:" + testSrcBcrypt + ":itflowtest:mail::/home/itflowtest:0:\n" +
		"support:" + testSrcBcrypt + ":itflowtest:mail::/home/itflowtest:0:\n"
	if err := os.WriteFile(filepath.Join(confDir, "passwd"), []byte(passwd), 0o640); err != nil {
		t.Fatal(err)
	}
	// Only `support` has a materialized Maildir (received mail). `sales` +
	// `billing` are fresh: bare dirs, no cur/new/tmp.
	for _, d := range []string{"cur", "new", "tmp"} {
		if err := os.MkdirAll(filepath.Join(mailRoot, dom, "support", d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(mailRoot, dom, "sales"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(mailRoot, dom, "billing"), 0o750); err != nil {
		t.Fatal(err)
	}

	mb := &fakeMailboxRepoMB{}
	dr := &fakeDomainRepoMB{id: "01DOMAIN0000000000000000TS"}
	res := &MailImportResult{}
	insertMailboxPanelRows(context.Background(), mailRoot, "", true, mailShadowSource{}, fakeTargetUserID, mb, dr, res)

	got := map[string]bool{}
	for _, m := range mb.created {
		got[m.LocalPart] = true
	}
	for _, want := range []string{"sales", "billing", "support"} {
		if !got[want] {
			t.Errorf("mailbox %q@%s not created — fresh (maildir-less) accounts must import from conf/passwd", want, dom)
		}
	}
	if len(mb.created) != 3 {
		t.Fatalf("created %d mailboxes, want 3 (sales, billing, support)", len(mb.created))
	}
}

// listSourceMailAccounts returns every account in conf/passwd regardless of hash
// scheme (unlike loadSourceMailPasswords, which drops non-bcrypt). Missing file
// (cPanel) → nil so the Maildir walk stays authoritative there.
func TestListSourceMailAccounts(t *testing.T) {
	dir := t.TempDir()
	confDir := filepath.Join(dir, "conf")
	if err := os.MkdirAll(confDir, 0o750); err != nil {
		t.Fatal(err)
	}
	body := "sales:{BLF-CRYPT}$2y$05$x:u:mail::/h:0:\n" +
		"# a comment\n" +
		"\n" +
		"legacy:{SHA512-CRYPT}$6$notbcrypt:u:mail::/h:0:\n" // non-bcrypt still listed
	if err := os.WriteFile(filepath.Join(confDir, "passwd"), []byte(body), 0o640); err != nil {
		t.Fatal(err)
	}
	got := listSourceMailAccounts(dir)
	sort.Strings(got)
	if len(got) != 2 || got[0] != "legacy" || got[1] != "sales" {
		t.Fatalf("got %v, want [legacy sales]", got)
	}
	if n := listSourceMailAccounts(t.TempDir()); n != nil {
		t.Errorf("no conf/passwd → nil, got %v", n)
	}
}

// --- Bug A: aliases ---

type fakeForwarderRepoGH327 struct {
	repository.EmailForwarderRepository
	created []*models.EmailForwarder
}

func (f *fakeForwarderRepoGH327) Create(_ context.Context, fwd *models.EmailForwarder) error {
	f.created = append(f.created, fwd)
	return nil
}

type fakeMailboxRepoAlias struct {
	repository.MailboxRepository
	byEmail map[string]*models.Mailbox
}

func (f *fakeMailboxRepoAlias) FindByEmail(_ context.Context, email string) (*models.Mailbox, error) {
	if mb, ok := f.byEmail[email]; ok {
		return mb, nil
	}
	return nil, repository.ErrNotFound
}

type fakeDomainRepoAlias struct {
	repository.DomainRepository
	id string
}

func (f *fakeDomainRepoAlias) FindByName(_ context.Context, name string) (*models.Domain, error) {
	return &models.Domain{ID: f.id, Name: name}, nil
}

// GH #327 Bug A: HestiaCP aliases at mail/<dom>/conf/aliases (alias@dom:acct@dom)
// were never scanned (the importer only knew cpanel's <home>/etc/<dom>/aliases).
// Each alias must attach to its TARGET mailbox as a type='alias' forwarder, with
// Target = the alias's OWN address (GH #280) so many aliases coexist on one box.
func TestScanHestiaAliases(t *testing.T) {
	extract := t.TempDir()
	dom := "testsite.local"
	confDir := filepath.Join(extract, "mail", dom, "conf")
	if err := os.MkdirAll(confDir, 0o750); err != nil {
		t.Fatal(err)
	}
	aliases := "info@" + dom + ":support@" + dom + "\n" +
		"help@" + dom + ":support@" + dom + "\n"
	if err := os.WriteFile(filepath.Join(confDir, "aliases"), []byte(aliases), 0o640); err != nil {
		t.Fatal(err)
	}

	support := &models.Mailbox{ID: "01MBSUPPORT000000000000000", LocalPart: "support", DomainID: "01DOMAIN0000000000000000TS"}
	fwd := &fakeForwarderRepoGH327{}
	mbx := &fakeMailboxRepoAlias{byEmail: map[string]*models.Mailbox{"support@" + dom: support}}
	dr := &fakeDomainRepoAlias{id: "01DOMAIN0000000000000000TS"}
	res := &ExtrasResult{}

	scanHestiaAliases(context.Background(), fwd, mbx, dr, &ParsedTarball{ExtractDir: extract}, true, res)

	if res.ForwardersCreated != 2 {
		t.Fatalf("ForwardersCreated = %d, want 2", res.ForwardersCreated)
	}
	if len(fwd.created) != 2 {
		t.Fatalf("created %d forwarders, want 2", len(fwd.created))
	}
	byLocal := map[string]*models.EmailForwarder{}
	for _, f := range fwd.created {
		if f.LocalPart == nil {
			t.Fatalf("alias row has nil LocalPart")
		}
		byLocal[*f.LocalPart] = f
	}
	for _, alias := range []string{"info", "help"} {
		f, ok := byLocal[alias]
		if !ok {
			t.Fatalf("alias %q not created", alias)
		}
		if f.Type != "alias" {
			t.Errorf("%s: Type = %q, want alias", alias, f.Type)
		}
		if f.MailboxID == nil || *f.MailboxID != support.ID {
			t.Errorf("%s: MailboxID = %v, want target mailbox %s", alias, f.MailboxID, support.ID)
		}
		if want := alias + "@" + dom; f.Target != want {
			t.Errorf("%s: Target = %q, want %q (alias's own address for GH #280 uniqueness)", alias, f.Target, want)
		}
		if !f.Enabled {
			t.Errorf("%s: Enabled = false, want true (preserveMailRouting)", alias)
		}
	}
}

// An alias whose target account has no panel mailbox is orphaned, not fatal.
func TestScanHestiaAliases_Orphan(t *testing.T) {
	extract := t.TempDir()
	dom := "testsite.local"
	confDir := filepath.Join(extract, "mail", dom, "conf")
	if err := os.MkdirAll(confDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(confDir, "aliases"),
		[]byte("ghost@"+dom+":missing@"+dom+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	fwd := &fakeForwarderRepoGH327{}
	mbx := &fakeMailboxRepoAlias{byEmail: map[string]*models.Mailbox{}}
	dr := &fakeDomainRepoAlias{id: "01DOMAIN0000000000000000TS"}
	res := &ExtrasResult{}
	scanHestiaAliases(context.Background(), fwd, mbx, dr, &ParsedTarball{ExtractDir: extract}, true, res)
	if res.ForwardersCreated != 0 || res.ForwardersOrphaned != 1 || len(fwd.created) != 0 {
		t.Fatalf("orphan: created=%d orphaned=%d rows=%d, want 0/1/0",
			res.ForwardersCreated, res.ForwardersOrphaned, len(fwd.created))
	}
}

// No mail/ dir (cPanel/DA) → no-op.
func TestScanHestiaAliases_NoMailDir(t *testing.T) {
	fwd := &fakeForwarderRepoGH327{}
	mbx := &fakeMailboxRepoAlias{byEmail: map[string]*models.Mailbox{}}
	dr := &fakeDomainRepoAlias{id: "x"}
	res := &ExtrasResult{}
	scanHestiaAliases(context.Background(), fwd, mbx, dr, &ParsedTarball{ExtractDir: t.TempDir()}, false, res)
	if res.ForwardersCreated != 0 || len(fwd.created) != 0 {
		t.Errorf("expected no-op, got created=%d rows=%d", res.ForwardersCreated, len(fwd.created))
	}
}
