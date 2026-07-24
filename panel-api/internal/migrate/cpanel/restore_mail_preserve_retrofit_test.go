package cpanel

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeExistingMailboxRepo simulates a mailbox row a PRIOR migration already
// created with a FRESH (non-source) password. GH #634: re-running the
// migration with "carry over source passwords" checked must UPDATE that row to
// the source hash — the old exists-guard skipped it, so the source password was
// never applied and the tenant stayed locked out of their original password.
type fakeExistingMailboxRepo struct {
	repository.MailboxRepository
	mb          *models.Mailbox
	updatedTo   string
	updateCalls int
	createCalls int
}

func (f *fakeExistingMailboxRepo) ExistsByDomainAndLocalPart(_ context.Context, _, _ string) (bool, error) {
	return true, nil
}
func (f *fakeExistingMailboxRepo) FindByEmail(_ context.Context, _ string) (*models.Mailbox, error) {
	return f.mb, nil
}
func (f *fakeExistingMailboxRepo) UpdatePasswordHash(_ context.Context, id, hash string) error {
	f.updateCalls++
	f.updatedTo = hash
	if f.mb != nil && f.mb.ID == id {
		f.mb.PasswordHash = hash
	}
	return nil
}
func (f *fakeExistingMailboxRepo) Create(_ context.Context, _ *models.Mailbox) error {
	f.createCalls++
	return nil
}

const freshFirstMigrationHash = "$2a$10$freshtemppasswordfromfirstmigrationaaaaaaaaaaaaaaaaaa"

// GH #634 reproduction (johnnyq): first migration created the row with a fresh
// password; the second run with --preserve-source-state must retrofit the
// source hash onto the existing row instead of skipping it.
func TestImportMailboxes_RetrofitsExistingRowOnPreserve(t *testing.T) {
	parsed := buildHestiaMailTree(t, "itflow.app", "info", "info:{BLF-CRYPT}"+testSrcBcrypt)
	mb := &fakeExistingMailboxRepo{mb: &models.Mailbox{
		ID:           "01MAILBOXID000000000000000",
		LocalPart:    "info",
		PasswordHash: freshFirstMigrationHash,
	}}
	dom := &fakeDomainRepoMB{id: "01DOMAINID0000000000000000"}

	res, err := ImportMailboxes(context.Background(), parsed, stubMailAgent{}, "job1", mb, dom, true /*preserveCreds*/, "")
	if err != nil {
		t.Fatalf("ImportMailboxes: %v", err)
	}
	if mb.createCalls != 0 {
		t.Errorf("existing row must not be re-created; Create called %d time(s)", mb.createCalls)
	}
	if mb.updateCalls != 1 {
		t.Fatalf("want exactly 1 UpdatePasswordHash call, got %d", mb.updateCalls)
	}
	if mb.updatedTo != testSrcBcrypt {
		t.Errorf("retrofit hash = %q, want tag-stripped source %q", mb.updatedTo, testSrcBcrypt)
	}
	if !containsSubstr(res.Skipped, "SOURCE password RE-APPLIED") {
		t.Errorf("expected a 'SOURCE password RE-APPLIED' notice, got %v", res.Skipped)
	}
}

// Idempotency: a second preserve run over a row that ALREADY has the source
// hash must not churn the password (no redundant update).
func TestImportMailboxes_ExistingRowAlreadySourceNoChurn(t *testing.T) {
	parsed := buildHestiaMailTree(t, "itflow.app", "info", "info:{BLF-CRYPT}"+testSrcBcrypt)
	mb := &fakeExistingMailboxRepo{mb: &models.Mailbox{
		ID:           "01MAILBOXID000000000000000",
		LocalPart:    "info",
		PasswordHash: testSrcBcrypt, // already retrofitted on a prior run
	}}
	dom := &fakeDomainRepoMB{id: "01DOMAINID0000000000000000"}

	res, err := ImportMailboxes(context.Background(), parsed, stubMailAgent{}, "job1", mb, dom, true /*preserveCreds*/, "")
	if err != nil {
		t.Fatalf("ImportMailboxes: %v", err)
	}
	if mb.updateCalls != 0 {
		t.Errorf("row already carries the source hash; want 0 updates, got %d", mb.updateCalls)
	}
	if !containsSubstr(res.Skipped, "already exists with the SOURCE password") {
		t.Errorf("expected the 'already exists with the SOURCE password' notice, got %v", res.Skipped)
	}
}

// Without preserve, an existing row is left untouched — no password churn on a
// plain re-run.
func TestImportMailboxes_ExistingRowUntouchedWithoutPreserve(t *testing.T) {
	parsed := buildHestiaMailTree(t, "itflow.app", "info", "info:{BLF-CRYPT}"+testSrcBcrypt)
	mb := &fakeExistingMailboxRepo{mb: &models.Mailbox{
		ID:           "01MAILBOXID000000000000000",
		LocalPart:    "info",
		PasswordHash: freshFirstMigrationHash,
	}}
	dom := &fakeDomainRepoMB{id: "01DOMAINID0000000000000000"}

	res, err := ImportMailboxes(context.Background(), parsed, stubMailAgent{}, "job1", mb, dom, false /*preserveCreds*/, "")
	if err != nil {
		t.Fatalf("ImportMailboxes: %v", err)
	}
	if mb.updateCalls != 0 {
		t.Errorf("no-preserve must not touch an existing password; got %d update(s)", mb.updateCalls)
	}
	if !containsSubstr(res.Skipped, "already exists") {
		t.Errorf("expected an 'already exists' skip, got %v", res.Skipped)
	}
}
