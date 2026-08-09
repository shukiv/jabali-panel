package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// ---------- fake MailboxRepository ----------

// fakeMailboxRepo is an in-memory MailboxRepository for the CLI tests.
// Scoping-correctness on ListByDomainID is the main guarantee we test —
// see TestListMailboxes_DisjointByDomain below.
type fakeMailboxRepo struct {
	rows map[string]*models.Mailbox // id → row
	// createErr / deleteErr / updateErr / findErr / existsErr force the
	// corresponding write to fail, exercising the CLI's error paths.
	createErr error
	deleteErr error
	updateErr error
	findErr   error
	existsErr error
}

func newFakeMailboxRepo() *fakeMailboxRepo {
	return &fakeMailboxRepo{rows: map[string]*models.Mailbox{}}
}

func (f *fakeMailboxRepo) FindByIDs(_ context.Context, ids []string) ([]models.Mailbox, error) {
	var out []models.Mailbox
	for _, id := range ids {
		if mb, ok := f.rows[id]; ok {
			out = append(out, *mb)
		}
	}
	return out, nil
}

func (f *fakeMailboxRepo) FindByID(_ context.Context, id string) (*models.Mailbox, error) {
	if f.findErr != nil {
		return nil, f.findErr
	}
	if mb, ok := f.rows[id]; ok {
		cp := *mb
		return &cp, nil
	}
	return nil, repository.ErrNotFound
}

func (f *fakeMailboxRepo) FindByEmail(_ context.Context, email string) (*models.Mailbox, error) {
	if f.findErr != nil {
		return nil, f.findErr
	}
	for _, mb := range f.rows {
		if mb.EmailCached == email {
			cp := *mb
			return &cp, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (f *fakeMailboxRepo) ListByDomainID(_ context.Context, domainID string, _ repository.ListOptions) ([]models.Mailbox, int64, error) {
	var out []models.Mailbox
	for _, mb := range f.rows {
		if mb.DomainID == domainID {
			out = append(out, *mb)
		}
	}
	return out, int64(len(out)), nil
}

func (f *fakeMailboxRepo) CountByDomainID(_ context.Context, domainID string) (int64, error) {
	var n int64
	for _, mb := range f.rows {
		if mb.DomainID == domainID {
			n++
		}
	}
	return n, nil
}

func (f *fakeMailboxRepo) Create(_ context.Context, mb *models.Mailbox) error {
	if f.createErr != nil {
		return f.createErr
	}
	// Simulate the BEFORE INSERT trigger that the repo test relies on in
	// prod — set EmailCached so downstream assertions see the full form.
	mb.EmailCached = mb.LocalPart + "@" + domainNameForID(f.rows, mb.DomainID)
	cp := *mb
	f.rows[mb.ID] = &cp
	return nil
}

func (f *fakeMailboxRepo) Delete(_ context.Context, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	delete(f.rows, id)
	return nil
}

func (f *fakeMailboxRepo) UpdatePasswordHash(_ context.Context, id string, hash string) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	if mb, ok := f.rows[id]; ok {
		mb.PasswordHash = hash
		mb.UpdatedAt = time.Now().UTC()
		return nil
	}
	return repository.ErrNotFound
}

func (f *fakeMailboxRepo) UpdatePasswordHashAndEnc(_ context.Context, id string, hash string, enc []byte) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	if mb, ok := f.rows[id]; ok {
		mb.PasswordHash = hash
		mb.PasswordEnc = enc
		mb.UpdatedAt = time.Now().UTC()
		return nil
	}
	return repository.ErrNotFound
}

func (f *fakeMailboxRepo) UpdateQuota(_ context.Context, id string, quotaBytes uint64) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	if mb, ok := f.rows[id]; ok {
		mb.QuotaBytes = quotaBytes
		mb.UpdatedAt = time.Now().UTC()
		return nil
	}
	return repository.ErrNotFound
}

func (f *fakeMailboxRepo) UpdateDisplayName(_ context.Context, _ string, _ string) error { return nil }
func (f *fakeMailboxRepo) SetDisabled(_ context.Context, _ string, _ bool) error         { return nil }
func (f *fakeMailboxRepo) SetSendOnly(_ context.Context, _ string, _ bool) error         { return nil }
func (f *fakeMailboxRepo) CountAll(context.Context) (int64, error) { return 0, nil }
func (f *fakeMailboxRepo) ListAllWithDomain(_ context.Context) ([]repository.MailboxWithDomain, error) {
	return nil, nil
}
func (f *fakeMailboxRepo) ListByOwnerWithDomain(_ context.Context, _ string) ([]repository.MailboxWithDomain, error) {
	return nil, nil
}

func (f *fakeMailboxRepo) UpdateUsage(_ context.Context, _ string, _ uint64, _ time.Time) error {
	return nil
}

func (f *fakeMailboxRepo) ExistsByDomainAndLocalPart(_ context.Context, domainID, localPart string) (bool, error) {
	if f.existsErr != nil {
		return false, f.existsErr
	}
	for _, mb := range f.rows {
		if mb.DomainID == domainID && mb.LocalPart == localPart {
			return true, nil
		}
	}
	return false, nil
}

// domainNameForID reverses-lookups the domain name for a mailbox row so
// the fake can fill EmailCached the way the DB trigger would. In these
// tests we preseed by inserting with a known domain name anyway, so the
// test helper just returns the first seen row's domain — good enough
// for single-domain insertion tests; callers of multi-domain tests set
// EmailCached explicitly before Create, bypassing this branch.
func domainNameForID(existing map[string]*models.Mailbox, _ string) string {
	for _, mb := range existing {
		if mb.EmailCached != "" {
			if at := strings.Index(mb.EmailCached, "@"); at >= 0 {
				return mb.EmailCached[at+1:]
			}
		}
	}
	return "unknown.test"
}

// ---------- helper: domain fixtures ----------

func testDomain(id, name string, emailEnabled bool) *models.Domain {
	return &models.Domain{
		ID:           id,
		Name:         name,
		UserID:       "u-test",
		EmailEnabled: emailEnabled,
	}
}

// preseedMailbox inserts a fully-formed mailbox row directly, bypassing
// the CLI helpers. Used when a test wants to exercise list/read paths
// without first running Create.
func preseedMailbox(repo *fakeMailboxRepo, id, domainID, domainName, local string) *models.Mailbox {
	mb := &models.Mailbox{
		ID:           id,
		DomainID:     domainID,
		LocalPart:    local,
		EmailCached:  local + "@" + domainName,
		PasswordHash: "$2b$12$stub",
		QuotaBytes:   1 << 30,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	cp := *mb
	repo.rows[id] = &cp
	return mb
}

// ---------- tests ----------

// TestListMailboxes_DisjointByDomain is the plan's key scoping
// regression guard: listing mailboxes for d1 must never include a row
// from d2, and vice versa.
func TestListMailboxes_DisjointByDomain(t *testing.T) {
	t.Parallel()
	repo := newFakeMailboxRepo()
	preseedMailbox(repo, "mb_a1", "dom1", "alpha.test", "alice")
	preseedMailbox(repo, "mb_a2", "dom1", "alpha.test", "bob")
	preseedMailbox(repo, "mb_b1", "dom2", "beta.test", "carol")

	ctx := context.Background()
	got1, err := listMailboxesDirect(ctx, repo, "dom1")
	if err != nil {
		t.Fatalf("list dom1: %v", err)
	}
	got2, err := listMailboxesDirect(ctx, repo, "dom2")
	if err != nil {
		t.Fatalf("list dom2: %v", err)
	}

	if len(got1) != 2 {
		t.Fatalf("dom1 should have 2 mailboxes, got %d", len(got1))
	}
	if len(got2) != 1 {
		t.Fatalf("dom2 should have 1 mailbox, got %d", len(got2))
	}
	for _, mb := range got1 {
		if mb.DomainID != "dom1" {
			t.Errorf("dom1 list leaked a %s mailbox: %+v", mb.DomainID, mb)
		}
	}
	for _, mb := range got2 {
		if mb.DomainID != "dom2" {
			t.Errorf("dom2 list leaked a %s mailbox: %+v", mb.DomainID, mb)
		}
	}
	// Emails must be disjoint — no overlap in result sets.
	inDom1 := map[string]bool{}
	for _, mb := range got1 {
		inDom1[mb.EmailCached] = true
	}
	for _, mb := range got2 {
		if inDom1[mb.EmailCached] {
			t.Errorf("dom1 and dom2 lists share email %q — scoping regression", mb.EmailCached)
		}
	}
}

// TestCreateMailbox_HappyPath checks the canonical flow: mailbox is
// inserted with a bcrypt hash the password verifies against, and a
// generated password is returned reveal-once.
func TestCreateMailbox_HappyPath(t *testing.T) {
	t.Parallel()
	repo := newFakeMailboxRepo()
	dom := testDomain("dom1", "example.com", true)

	var notifyCmd string
	notify := func(_ context.Context, cmd string, _ any) {
		notifyCmd = cmd
	}

	mb, generated, err := createMailboxDirect(
		context.Background(), repo, notify, nil,
		dom, "Alice", "", 0,
	)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if mb.LocalPart != "alice" {
		t.Errorf("LocalPart should be canonicalised to lowercase, got %q", mb.LocalPart)
	}
	if mb.EmailCached != "alice@example.com" {
		t.Errorf("EmailCached = %q, want alice@example.com", mb.EmailCached)
	}
	if generated == "" {
		t.Error("generated password must be returned reveal-once when caller omitted --password")
	}
	if bcrypt.CompareHashAndPassword([]byte(mb.PasswordHash), []byte(generated)) != nil {
		t.Error("stored bcrypt hash must verify against generated password")
	}
	if mb.QuotaBytes != cliMailboxDefaultQuotaBytes {
		t.Errorf("default quota should be %d, got %d", cliMailboxDefaultQuotaBytes, mb.QuotaBytes)
	}
	if notifyCmd != "mailbox.create" {
		t.Errorf("agent notify should fire mailbox.create, got %q", notifyCmd)
	}
	if _, ok := repo.rows[mb.ID]; !ok {
		t.Error("mailbox row should be in the repo")
	}
}

// TestCreateMailbox_RefusesOnDisabledEmail: the handler also blocks
// this with email_not_enabled; the CLI must mirror that guard so
// operators don't silently create orphans.
func TestCreateMailbox_RefusesOnDisabledEmail(t *testing.T) {
	t.Parallel()
	repo := newFakeMailboxRepo()
	dom := testDomain("dom1", "example.com", false) // email disabled

	_, _, err := createMailboxDirect(
		context.Background(), repo, nil, nil,
		dom, "alice", "", 0,
	)
	if err == nil {
		t.Fatal("expected error when email is not enabled on the domain")
	}
	if !strings.Contains(err.Error(), "email is not enabled") {
		t.Errorf("error should mention email disabled, got: %v", err)
	}
	if len(repo.rows) != 0 {
		t.Errorf("no mailbox row should be inserted on a disabled domain, got %d rows", len(repo.rows))
	}
}

// TestCreateMailbox_UniquenessConflict — the UNIQUE index on
// email_cached is enforced pre-INSERT; caller gets a typed error rather
// than a driver duplicate-entry string.
func TestCreateMailbox_UniquenessConflict(t *testing.T) {
	t.Parallel()
	repo := newFakeMailboxRepo()
	dom := testDomain("dom1", "example.com", true)

	// First create succeeds.
	_, _, err := createMailboxDirect(
		context.Background(), repo, nil, nil,
		dom, "alice", "", 0,
	)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	// Second create with the same local_part must fail.
	_, _, err = createMailboxDirect(
		context.Background(), repo, nil, nil,
		dom, "alice", "", 0,
	)
	if err == nil {
		t.Fatal("duplicate create should have failed")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error should say already exists, got: %v", err)
	}
}

// TestCreateMailbox_ExplicitPassword — a caller-supplied password must
// be stored as its bcrypt hash and NOT returned in the response (the
// response's Password field is only populated for generated passwords).
func TestCreateMailbox_ExplicitPassword(t *testing.T) {
	t.Parallel()
	repo := newFakeMailboxRepo()
	dom := testDomain("dom1", "example.com", true)

	mb, generated, err := createMailboxDirect(
		context.Background(), repo, nil, nil,
		dom, "alice", "super-secret-12345", 0,
	)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if generated != "" {
		t.Errorf("generated password must be empty when caller supplied one, got %q", generated)
	}
	if bcrypt.CompareHashAndPassword([]byte(mb.PasswordHash), []byte("super-secret-12345")) != nil {
		t.Error("stored hash must verify against caller-supplied password")
	}
}

// TestCreateMailbox_QuotaFloor enforces the 16 MiB floor.
func TestCreateMailbox_QuotaFloor(t *testing.T) {
	t.Parallel()
	repo := newFakeMailboxRepo()
	dom := testDomain("dom1", "example.com", true)

	_, _, err := createMailboxDirect(
		context.Background(), repo, nil, nil,
		dom, "alice", "pw12345678", 1*1024*1024, // 1 MiB — below floor
	)
	if err == nil || !strings.Contains(err.Error(), "at least 16") {
		t.Fatalf("expected quota-floor error, got %v", err)
	}
}

// TestDeleteMailbox_HappyPath — agent call fires, row is removed.
func TestDeleteMailbox_HappyPath(t *testing.T) {
	t.Parallel()
	repo := newFakeMailboxRepo()
	mb := preseedMailbox(repo, "mb1", "dom1", "example.com", "alice")

	var calledCmd string
	call := func(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
		calledCmd = cmd
		return json.RawMessage(`{"ok":true}`), nil
	}
	err := deleteMailboxDirect(context.Background(), repo, call, mb.EmailCached)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if calledCmd != "mailbox.delete" {
		t.Errorf("agent should fire mailbox.delete, got %q", calledCmd)
	}
	if _, ok := repo.rows[mb.ID]; ok {
		t.Error("row should have been removed after agent + repo delete")
	}
}

// TestDeleteMailbox_AgentFailureAbortsBeforeDB verifies the
// delete-ordering rule: if the agent call fails, the DB row must stay
// so Stalwart + panel stay in sync (ADR-0042).
func TestDeleteMailbox_AgentFailureAbortsBeforeDB(t *testing.T) {
	t.Parallel()
	repo := newFakeMailboxRepo()
	mb := preseedMailbox(repo, "mb1", "dom1", "example.com", "alice")

	call := func(_ context.Context, _ string, _ any) (json.RawMessage, error) {
		return nil, errors.New("stalwart down")
	}
	err := deleteMailboxDirect(context.Background(), repo, call, mb.EmailCached)
	if err == nil {
		t.Fatal("expected delete to fail when agent errors")
	}
	if _, ok := repo.rows[mb.ID]; !ok {
		t.Error("DB row must remain intact when agent delete fails")
	}
}

// TestDeleteMailbox_NotFound — mailbox not in the table.
func TestDeleteMailbox_NotFound(t *testing.T) {
	t.Parallel()
	repo := newFakeMailboxRepo()
	err := deleteMailboxDirect(context.Background(), repo, func(_ context.Context, _ string, _ any) (json.RawMessage, error) { return nil, nil }, "missing@example.com")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

// TestSetMailboxQuota_HappyPath — quota bytes updated + agent notified.
func TestSetMailboxQuota_HappyPath(t *testing.T) {
	t.Parallel()
	repo := newFakeMailboxRepo()
	mb := preseedMailbox(repo, "mb1", "dom1", "example.com", "alice")

	var notifyCmd string
	notify := func(_ context.Context, cmd string, _ any) {
		notifyCmd = cmd
	}
	row, err := setMailboxQuotaDirect(
		context.Background(), repo, notify,
		mb.EmailCached, 500*1024*1024,
	)
	if err != nil {
		t.Fatalf("set-quota: %v", err)
	}
	if row.QuotaBytes != 500*1024*1024 {
		t.Errorf("returned row quota = %d, want 500 MiB", row.QuotaBytes)
	}
	if repo.rows[mb.ID].QuotaBytes != 500*1024*1024 {
		t.Errorf("stored row quota not updated")
	}
	if notifyCmd != "mailbox.set_quota" {
		t.Errorf("agent should fire mailbox.set_quota, got %q", notifyCmd)
	}
}

// TestSetMailboxQuota_Floor enforces the 16 MiB floor.
func TestSetMailboxQuota_Floor(t *testing.T) {
	t.Parallel()
	repo := newFakeMailboxRepo()
	preseedMailbox(repo, "mb1", "dom1", "example.com", "alice")

	_, err := setMailboxQuotaDirect(
		context.Background(), repo, nil,
		"alice@example.com", 1*1024*1024,
	)
	if err == nil || !strings.Contains(err.Error(), "at least 16") {
		t.Fatalf("expected quota-floor error, got %v", err)
	}
}

// TestRotatePassword_HappyPath — new hash is stored, password returned
// once, and verifies against the stored hash.
func TestRotatePassword_HappyPath(t *testing.T) {
	t.Parallel()
	repo := newFakeMailboxRepo()
	mb := preseedMailbox(repo, "mb1", "dom1", "example.com", "alice")
	oldHash := mb.PasswordHash

	var notifyCmd string
	notify := func(_ context.Context, cmd string, _ any) {
		notifyCmd = cmd
	}
	generated, err := rotateMailboxPasswordDirect(
		context.Background(), repo, notify, nil,
		mb.EmailCached, "",
	)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if generated == "" {
		t.Error("generated password must be returned reveal-once when caller omits --password")
	}
	if repo.rows[mb.ID].PasswordHash == oldHash {
		t.Error("password hash must change after rotate")
	}
	if bcrypt.CompareHashAndPassword([]byte(repo.rows[mb.ID].PasswordHash), []byte(generated)) != nil {
		t.Error("new hash must verify against the generated password")
	}
	if notifyCmd != "mailbox.set_password" {
		t.Errorf("agent should fire mailbox.set_password, got %q", notifyCmd)
	}
}

// TestRotatePassword_ExplicitPassword — supplying --password returns
// empty and stores a hash that verifies against the supplied value.
func TestRotatePassword_ExplicitPassword(t *testing.T) {
	t.Parallel()
	repo := newFakeMailboxRepo()
	mb := preseedMailbox(repo, "mb1", "dom1", "example.com", "alice")

	generated, err := rotateMailboxPasswordDirect(
		context.Background(), repo, nil, nil,
		mb.EmailCached, "my-new-secret",
	)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if generated != "" {
		t.Errorf("generated password must be empty when caller supplied one, got %q", generated)
	}
	if bcrypt.CompareHashAndPassword([]byte(repo.rows[mb.ID].PasswordHash), []byte("my-new-secret")) != nil {
		t.Error("stored hash must verify against caller-supplied password")
	}
}

// TestRotatePassword_NotFound — mailbox doesn't exist.
func TestRotatePassword_NotFound(t *testing.T) {
	t.Parallel()
	repo := newFakeMailboxRepo()
	_, err := rotateMailboxPasswordDirect(
		context.Background(), repo, nil, nil,
		"missing@example.com", "",
	)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}
