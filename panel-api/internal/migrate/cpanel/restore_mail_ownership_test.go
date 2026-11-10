package cpanel

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// The mail tree a migration imports comes from the SOURCE account, so its
// <domain> directory names are source-controlled. Only domains the panel holds
// for the migrated user may get mailbox rows or receive pushed mail; a dir
// naming another tenant's domain (or a domain not on this panel) is refused.

// recordingMailAgent records the import_mailboxes params.
type recordingMailAgent struct {
	calls  int
	params map[string]any
}

func (r *recordingMailAgent) Call(_ context.Context, _ string, params any) (json.RawMessage, error) {
	r.calls++
	r.params, _ = params.(map[string]any)
	return json.RawMessage(`{"messages_imported":0,"bytes_imported":0}`), nil
}

// notFoundDomainRepo knows only the domains in owners; FindByName fails for
// any other name, like the real repository for a domain not on this panel.
type notFoundDomainRepo struct {
	fakeDomainRepoMB
}

func (f *notFoundDomainRepo) FindByName(ctx context.Context, name string) (*models.Domain, error) {
	if _, ok := f.owners[name]; !ok {
		return nil, errors.New("record not found")
	}
	return f.fakeDomainRepoMB.FindByName(ctx, name)
}

func mkMaildir(t *testing.T, dir string) {
	t.Helper()
	for _, d := range []string{"cur", "new", "tmp"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
}

// cPanel-shaped mail root: owner Maildir at the root, own.com/box, and a
// planted victim.com/admin.
func ownershipMailTree(t *testing.T) *ParsedTarball {
	t.Helper()
	mailRoot := filepath.Join(t.TempDir(), "mail")
	mkMaildir(t, mailRoot)
	mkMaildir(t, filepath.Join(mailRoot, "own.com", "box"))
	mkMaildir(t, filepath.Join(mailRoot, "victim.com", "admin"))
	return &ParsedTarball{MailRoot: mailRoot, OwnerEmail: "webup@own.com"}
}

func createdAddrs(mb *fakeMailboxRepoMB) []string {
	var out []string
	for _, m := range mb.created {
		out = append(out, m.LocalPart)
	}
	return out
}

func TestImportMailboxes_RefusesAnotherTenantsDomain(t *testing.T) {
	parsed := ownershipMailTree(t)
	mb := &fakeMailboxRepoMB{}
	dom := &fakeDomainRepoMB{id: "01DOMAINID0000000000000000", owners: map[string]string{
		"victim.com": "01OTHERTENANT0000000000000",
	}}
	ag := &recordingMailAgent{}

	res, err := ImportMailboxes(context.Background(), parsed, ag, "job1", mb, dom, true, "", fakeTargetUserID)
	if err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(createdAddrs(mb), ","); got != "webup,box" {
		t.Fatalf("created rows = %q, want webup,box (own.com only)", got)
	}
	if !containsSubstr(res.Skipped, "mail/victim.com is not a domain of the migrated account — refused") {
		t.Errorf("want a refusal line for victim.com, got %v", res.Skipped)
	}
	if ag.calls != 1 {
		t.Fatalf("agent calls = %d, want 1", ag.calls)
	}
	allowed, _ := ag.params["allowed_domains"].([]string)
	if strings.Join(allowed, ",") != "own.com" {
		t.Fatalf("allowed_domains = %v, want [own.com]", ag.params["allowed_domains"])
	}
}

// Even with --preserve-source-state, an existing mailbox on another tenant's
// domain must not get the source password retrofitted onto it.
func TestImportMailboxes_NoPasswordRetrofitOnForeignDomain(t *testing.T) {
	mailRoot := filepath.Join(t.TempDir(), "mail")
	mkMaildir(t, filepath.Join(mailRoot, "victim.com", "admin"))
	parsed := &ParsedTarball{MailRoot: mailRoot}
	mb := &fakeExistingMailboxRepo{mb: &models.Mailbox{ID: "01MB", PasswordHash: "$2a$10$victimsownpassword"}}
	dom := &fakeDomainRepoMB{id: "01DOMAINID0000000000000000", owners: map[string]string{
		"victim.com": "01OTHERTENANT0000000000000",
	}}
	ag := &recordingMailAgent{}

	if _, err := ImportMailboxes(context.Background(), parsed, ag, "job1", mb, dom, true, "", fakeTargetUserID); err != nil {
		t.Fatal(err)
	}
	if mb.updateCalls != 0 || mb.createCalls != 0 {
		t.Fatalf("foreign mailbox touched: updates=%d creates=%d", mb.updateCalls, mb.createCalls)
	}
	if ag.calls != 0 {
		t.Fatalf("agent called %d time(s) with no verified domain; want 0", ag.calls)
	}
}

func TestImportMailboxes_RefusesDomainNotOnPanel(t *testing.T) {
	parsed := ownershipMailTree(t)
	mb := &fakeMailboxRepoMB{}
	dom := &notFoundDomainRepo{fakeDomainRepoMB{id: "01DOMAINID0000000000000000", owners: map[string]string{
		"own.com": fakeTargetUserID,
	}}}
	ag := &recordingMailAgent{}

	res, err := ImportMailboxes(context.Background(), parsed, ag, "job1", mb, dom, false, "", fakeTargetUserID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubstr(res.Skipped, "mail/victim.com is not a domain on this panel — refused") {
		t.Errorf("want a not-on-panel refusal for victim.com, got %v", res.Skipped)
	}
	allowed, _ := ag.params["allowed_domains"].([]string)
	if strings.Join(allowed, ",") != "own.com" {
		t.Fatalf("allowed_domains = %v, want [own.com]", ag.params["allowed_domains"])
	}
}

// No target user verifies nothing: no rows, no push.
func TestImportMailboxes_EmptyTargetUserRefusesEverything(t *testing.T) {
	parsed := ownershipMailTree(t)
	mb := &fakeMailboxRepoMB{}
	dom := &fakeDomainRepoMB{id: "01DOMAINID0000000000000000"}
	ag := &recordingMailAgent{}

	if _, err := ImportMailboxes(context.Background(), parsed, ag, "job1", mb, dom, false, "", ""); err != nil {
		t.Fatal(err)
	}
	if len(mb.created) != 0 || ag.calls != 0 {
		t.Fatalf("want no rows and no push, got rows=%v agent calls=%d", createdAddrs(mb), ag.calls)
	}
}

// Without repositories ownership cannot be verified: fail closed, no push.
func TestImportMailboxes_NoReposNoPush(t *testing.T) {
	parsed := ownershipMailTree(t)
	ag := &recordingMailAgent{}

	res, err := ImportMailboxes(context.Background(), parsed, ag, "job1", nil, nil, false, "", fakeTargetUserID)
	if err != nil {
		t.Fatal(err)
	}
	if ag.calls != 0 {
		t.Fatalf("agent called %d time(s) without ownership verification; want 0", ag.calls)
	}
	if !containsSubstr(res.Skipped, "cannot verify mail domain ownership") {
		t.Errorf("want a fail-closed skip line, got %v", res.Skipped)
	}
}
