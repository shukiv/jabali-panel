package cpanel

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Fakes implement only the methods insertOneMailboxRow touches (interface-
// embedded so the rest is nil — never called here).
type fakeDomainRepoMB struct {
	repository.DomainRepository
	id string
}

func (f *fakeDomainRepoMB) FindByName(_ context.Context, name string) (*models.Domain, error) {
	return &models.Domain{ID: f.id, Name: name}, nil
}

type fakeMailboxRepoMB struct {
	repository.MailboxRepository
	created []*models.Mailbox
}

func (f *fakeMailboxRepoMB) ExistsByDomainAndLocalPart(_ context.Context, _, _ string) (bool, error) {
	return false, nil
}
func (f *fakeMailboxRepoMB) Create(_ context.Context, mb *models.Mailbox) error {
	f.created = append(f.created, mb)
	return nil
}

// stubMailAgent satisfies AgentInterface so ImportMailboxes takes the agent path
// (nil agent short-circuits before the panel-row insert). Returns an empty
// import result — we exercise the row-insert + password logic, not JMAP.
type stubMailAgent struct{}

func (stubMailAgent) Call(_ context.Context, _ string, _ any) (json.RawMessage, error) {
	return json.RawMessage(`{"messages_imported":0,"bytes_imported":0}`), nil
}

var _ agent.AgentInterface = stubMailAgent{}

// buildHestiaMailTree lays out mail/<domain>/conf/passwd (real format
// "info:{BLF-CRYPT}$2y$05$…") + a Maildir for the mailbox, matching what the
// hestia extractor produces.
func buildHestiaMailTree(t *testing.T, domain, local, passwdLine string) *ParsedTarball {
	t.Helper()
	home := t.TempDir()
	mailRoot := filepath.Join(home, "mail")
	confDir := filepath.Join(mailRoot, domain, "conf")
	if err := os.MkdirAll(confDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(confDir, "passwd"), []byte(passwdLine+"\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	// Maildir for the account so insertMailboxPanelRows treats it as a mailbox.
	for _, d := range []string{"cur", "new", "tmp"} {
		if err := os.MkdirAll(filepath.Join(mailRoot, domain, local, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	return &ParsedTarball{HomeDir: home, MailRoot: mailRoot}
}

const testSrcBcrypt = "$2y$05$abcdefghijklmnopqrstuuKe3l9x4Qm2Zr8s6t0v2w4y6a8c0e2g"

// End-to-end wiring: read conf/passwd → map by localpart → store the (tag-
// stripped) source hash on the created mailbox — but ONLY when preserving.
func TestImportMailboxes_PreservesSourcePassword(t *testing.T) {
	parsed := buildHestiaMailTree(t, "itflow.app", "info", "info:{BLF-CRYPT}"+testSrcBcrypt)
	mb := &fakeMailboxRepoMB{}
	dom := &fakeDomainRepoMB{id: "01DOMAINID0000000000000000"}

	res, err := ImportMailboxes(context.Background(), parsed, stubMailAgent{}, "job1", mb, dom, true /*preserveCreds*/, "")
	if err != nil {
		t.Fatalf("ImportMailboxes: %v", err)
	}
	if len(mb.created) != 1 {
		t.Fatalf("want 1 mailbox created, got %d", len(mb.created))
	}
	if mb.created[0].LocalPart != "info" {
		t.Errorf("local_part = %q", mb.created[0].LocalPart)
	}
	if mb.created[0].PasswordHash != testSrcBcrypt {
		t.Errorf("PasswordHash = %q, want the tag-stripped source hash %q", mb.created[0].PasswordHash, testSrcBcrypt)
	}
	if !containsSubstr(res.Skipped, "SOURCE password preserved") {
		t.Errorf("expected a 'SOURCE password preserved' notice, got %v", res.Skipped)
	}
}

// Default (no preserve): a FRESH random bcrypt is set (never the source hash),
// and the loud lockout notice fires.
func TestImportMailboxes_DefaultSetsFreshPasswordAndWarns(t *testing.T) {
	parsed := buildHestiaMailTree(t, "itflow.app", "info", "info:{BLF-CRYPT}"+testSrcBcrypt)
	mb := &fakeMailboxRepoMB{}
	dom := &fakeDomainRepoMB{id: "01DOMAINID0000000000000000"}

	res, err := ImportMailboxes(context.Background(), parsed, stubMailAgent{}, "job1", mb, dom, false /*preserveCreds*/, "")
	if err != nil {
		t.Fatalf("ImportMailboxes: %v", err)
	}
	if len(mb.created) != 1 {
		t.Fatalf("want 1 mailbox, got %d", len(mb.created))
	}
	if mb.created[0].PasswordHash == testSrcBcrypt {
		t.Error("default must NOT carry the source password")
	}
	if !strings.HasPrefix(mb.created[0].PasswordHash, "$2") {
		t.Errorf("expected a fresh bcrypt, got %q", mb.created[0].PasswordHash)
	}
	if !containsSubstr(res.Skipped, "ORIGINAL mail password was NOT migrated") {
		t.Errorf("expected the loud lockout notice, got %v", res.Skipped)
	}
}

func containsSubstr(ss []string, sub string) bool {
	for _, s := range ss {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
