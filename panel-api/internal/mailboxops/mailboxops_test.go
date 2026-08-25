package mailboxops

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

type fakeMBRepo struct {
	repository.MailboxRepository
	existing  map[string]*models.Mailbox
	exists    bool
	created   *models.Mailbox
	lastEnc   []byte
	encCalled bool
	quotaSet  uint64
	deleted   string
	createErr error
}

func (f *fakeMBRepo) ExistsByDomainAndLocalPart(_ context.Context, _, _ string) (bool, error) {
	return f.exists, nil
}
func (f *fakeMBRepo) Create(_ context.Context, mb *models.Mailbox) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.created = mb
	return nil
}
func (f *fakeMBRepo) FindByEmail(_ context.Context, email string) (*models.Mailbox, error) {
	if mb, ok := f.existing[email]; ok {
		return mb, nil
	}
	return nil, repository.ErrNotFound
}
func (f *fakeMBRepo) UpdatePasswordHashAndEnc(_ context.Context, _ string, _ string, enc []byte) error {
	f.lastEnc = enc
	f.encCalled = true
	return nil
}
func (f *fakeMBRepo) UpdateQuota(_ context.Context, _ string, q uint64) error { f.quotaSet = q; return nil }
func (f *fakeMBRepo) Delete(_ context.Context, id string) error              { f.deleted = id; return nil }

func enabledDomain() *models.Domain {
	return &models.Domain{ID: "d1", Name: "example.com", EmailEnabled: true}
}

func TestCreate_FieldParityAndDefaults(t *testing.T) {
	repo := &fakeMBRepo{}
	key := ssokey.Key{}
	mb, gen, err := Create(context.Background(), Deps{Mailboxes: repo, SSOKey: &key},
		CreateInput{Domain: enabledDomain(), LocalPart: "alice", DisplayName: "  Alice A  ", SendOnly: true}, nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if gen == "" {
		t.Fatal("a generated password must be revealed once")
	}
	if repo.created == nil || repo.created.DisplayName != "Alice A" || !repo.created.SendOnly {
		t.Fatalf("display_name/send_only not persisted (field parity): %+v", repo.created)
	}
	if repo.created.QuotaBytes != DefaultQuotaBytes {
		t.Errorf("quota default not applied: %d", repo.created.QuotaBytes)
	}
	if len(repo.created.PasswordEnc) == 0 {
		t.Errorf("password_enc must be sealed when an SSO key is present")
	}
	if mb.EmailCached != "alice@example.com" {
		t.Errorf("email_cached not reflected: %q", mb.EmailCached)
	}
}

func TestCreate_Gates(t *testing.T) {
	key := ssokey.Key{}
	base := func(repo *fakeMBRepo, dom *models.Domain, q uint64) (*models.Mailbox, string, error) {
		return Create(context.Background(), Deps{Mailboxes: repo, SSOKey: &key},
			CreateInput{Domain: dom, LocalPart: "a", QuotaBytes: q}, nil)
	}
	if _, _, err := base(&fakeMBRepo{}, &models.Domain{Name: "x.com"}, 0); !errors.Is(err, ErrEmailNotEnabled) {
		t.Errorf("email-disabled domain must be rejected, got %v", err)
	}
	if _, _, err := base(&fakeMBRepo{exists: true}, enabledDomain(), 0); !errors.Is(err, ErrMailboxExists) {
		t.Errorf("duplicate must be rejected, got %v", err)
	}
	if _, _, err := base(&fakeMBRepo{}, enabledDomain(), 1); !errors.Is(err, ErrQuotaTooSmall) {
		t.Errorf("below-floor quota must be rejected, got %v", err)
	}
}

// The #5 fix: rotating WITHOUT an SSO key must CLEAR the envelope (enc = nil),
// never leave the old sealed password behind. Both adapters previously called
// the hash-only UpdatePasswordHash, leaving a stale envelope.
func TestRotate_NilKey_ClearsEnvelope(t *testing.T) {
	repo := &fakeMBRepo{existing: map[string]*models.Mailbox{"a@example.com": {ID: "m1"}}}
	if _, err := RotatePassword(context.Background(), Deps{Mailboxes: repo, SSOKey: nil}, "a@example.com", "", nil); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if !repo.encCalled {
		t.Fatal("rotate must persist hash + envelope atomically via UpdatePasswordHashAndEnc")
	}
	if repo.lastEnc != nil {
		t.Fatalf("nil SSO key must CLEAR the envelope (enc=nil), got %d bytes", len(repo.lastEnc))
	}
}

func TestRotate_WithKey_Seals(t *testing.T) {
	repo := &fakeMBRepo{existing: map[string]*models.Mailbox{"a@example.com": {ID: "m1"}}}
	key := ssokey.Key{}
	if _, err := RotatePassword(context.Background(), Deps{Mailboxes: repo, SSOKey: &key}, "a@example.com", "newpassw0rd", nil); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if len(repo.lastEnc) == 0 {
		t.Fatal("a live SSO key must re-seal the envelope to the new password")
	}
}

// Delete destroys the Stalwart side first: an agent failure aborts BEFORE the
// row is removed.
func TestDelete_HostFailureKeepsRow(t *testing.T) {
	repo := &fakeMBRepo{existing: map[string]*models.Mailbox{"a@example.com": {ID: "m1"}}}
	failCall := func(context.Context, string, any) (json.RawMessage, error) {
		return nil, errors.New("stalwart down")
	}
	if err := Delete(context.Background(), repo, failCall, "a@example.com"); err == nil {
		t.Fatal("a failed Stalwart destroy must abort the delete")
	}
	if repo.deleted != "" {
		t.Fatalf("the row must NOT be deleted when the host destroy failed, got %q", repo.deleted)
	}
}
