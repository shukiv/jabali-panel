package mailboxops

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/mailaddr"
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
	mb, gen, err := Create(context.Background(), Deps{Mailboxes: repo, SSOKey: &key, Addresses: okReleaser{}},
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
		return Create(context.Background(), Deps{Mailboxes: repo, SSOKey: &key, Addresses: okReleaser{}},
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

// CreateSystem mints the GH #1056 relay: System=true, SendOnly=true, a sealed
// envelope, and the plaintext returned once. A nil SSO key is a hard error.
func TestCreateSystem_MintsSealedRelayPrincipal(t *testing.T) {
	repo := &fakeMBRepo{}
	key := ssokey.Key{}
	mb, pw, err := CreateSystem(context.Background(), Deps{Mailboxes: repo, SSOKey: &key, Addresses: okReleaser{}},
		SystemCreateInput{Domain: enabledDomain(), LocalPart: "sendmail", DisplayName: "example.com (system sender)", QuotaBytes: 16 * 1024 * 1024}, nil)
	if err != nil {
		t.Fatalf("CreateSystem: %v", err)
	}
	if pw == "" {
		t.Error("the generated relay password must be returned once")
	}
	if repo.created == nil || !repo.created.System || !repo.created.SendOnly {
		t.Fatalf("relay must be System+SendOnly: %+v", repo.created)
	}
	if len(repo.created.PasswordEnc) == 0 {
		t.Error("relay envelope must be sealed (webmail SSO depends on it)")
	}
	if repo.created.QuotaBytes != 16*1024*1024 {
		t.Errorf("relay quota = %d, want 16 MiB", repo.created.QuotaBytes)
	}
	if mb.EmailCached != "sendmail@example.com" {
		t.Errorf("email_cached = %q", mb.EmailCached)
	}
}

func TestCreateSystem_NilKeyRejected(t *testing.T) {
	if _, _, err := CreateSystem(context.Background(), Deps{Mailboxes: &fakeMBRepo{}, SSOKey: nil, Addresses: okReleaser{}},
		SystemCreateInput{Domain: enabledDomain(), LocalPart: "sendmail"}, nil); !errors.Is(err, ErrDeps) {
		t.Errorf("a nil SSO key must be rejected (relay must be sealed), got %v", err)
	}
}

// CreateForRestore persists a pre-computed hash and leaves password_enc NULL —
// there is no plaintext to seal.
func TestCreateForRestore_NoSealNoGate(t *testing.T) {
	repo := &fakeMBRepo{}
	mb, err := CreateForRestore(context.Background(), Deps{Mailboxes: repo, Addresses: okReleaser{}},
		RestoreCreateInput{DomainID: "d1", DomainName: "example.com", LocalPart: "info", PasswordHash: "$2b$12$sourcehash", QuotaBytes: 0})
	if err != nil {
		t.Fatalf("CreateForRestore: %v", err)
	}
	if repo.created.PasswordHash != "$2b$12$sourcehash" {
		t.Errorf("restore must persist the pre-computed hash verbatim, got %q", repo.created.PasswordHash)
	}
	if repo.created.PasswordEnc != nil {
		t.Errorf("restore has no plaintext to seal — password_enc must be nil, got %d bytes", len(repo.created.PasswordEnc))
	}
	if repo.created.QuotaBytes != DefaultQuotaBytes {
		t.Errorf("quota 0 must default, got %d", repo.created.QuotaBytes)
	}
	if mb.ID == "" {
		t.Error("row must be assigned an id")
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

// GH #1637: the address of the domain directory's host principal is not a
// mailbox. A mailbox there would collide with the principal, whose address
// book the whole domain reads.
// GH #1637: a migrated source account at the directory's address is refused
// like a new one.
func TestCreateForRestore_RefusesTheDirectoryAddress(t *testing.T) {
	repo := &fakeMBRepo{}
	_, err := CreateForRestore(context.Background(), Deps{Mailboxes: repo, Addresses: okReleaser{}},
		RestoreCreateInput{DomainID: "d1", DomainName: "example.com", LocalPart: "jabali-directory", PasswordHash: "$2a$10$x"})
	if !errors.Is(err, ErrInvalidLocalPart) || !errors.Is(err, mailaddr.ErrLocalReserved) {
		t.Fatalf("want ErrInvalidLocalPart wrapping ErrLocalReserved, got %v", err)
	}
	if repo.created != nil {
		t.Fatalf("no row may be written for the reserved address, got %+v", repo.created)
	}
}

func TestCreate_RefusesTheDirectoryAddress(t *testing.T) {
	repo := &fakeMBRepo{}
	key := ssokey.Key{}
	_, _, err := Create(context.Background(), Deps{Mailboxes: repo, SSOKey: &key, Addresses: okReleaser{}},
		CreateInput{Domain: enabledDomain(), LocalPart: "Jabali-Directory"}, nil)
	if !errors.Is(err, ErrInvalidLocalPart) || !errors.Is(err, mailaddr.ErrLocalReserved) {
		t.Fatalf("want ErrInvalidLocalPart wrapping ErrLocalReserved, got %v", err)
	}
	if repo.created != nil {
		t.Fatalf("no row may be written for the reserved address, got %+v", repo.created)
	}
}

// ADR-0110: postmaster@ on a tenant domain belongs to the server admin. Stalwart
// keeps the address on the admin's postmaster account, so a tenant mailbox
// there would sign in to the admin's account.
func TestCreate_RefusesPostmasterOnATenantDomain(t *testing.T) {
	repo := &fakeMBRepo{}
	key := ssokey.Key{}
	_, _, err := Create(context.Background(), Deps{Mailboxes: repo, SSOKey: &key},
		CreateInput{Domain: enabledDomain(), LocalPart: "PostMaster"}, nil)
	if !errors.Is(err, ErrInvalidLocalPart) || !errors.Is(err, mailaddr.ErrPostmasterReserved) {
		t.Fatalf("want ErrInvalidLocalPart wrapping ErrPostmasterReserved, got %v", err)
	}
	if repo.created != nil {
		t.Fatalf("no row may be written for postmaster@ on a tenant domain, got %+v", repo.created)
	}
}

// On the panel hostname's domain, postmaster@ is the admin's own mailbox.
func TestCreate_AllowsPostmasterOnThePanelDomain(t *testing.T) {
	repo := &fakeMBRepo{}
	key := ssokey.Key{}
	dom := enabledDomain()
	dom.IsPanelPrimary = true
	if _, _, err := Create(context.Background(), Deps{Mailboxes: repo, SSOKey: &key, Addresses: okReleaser{}},
		CreateInput{Domain: dom, LocalPart: "postmaster"}, nil); err != nil {
		t.Fatalf("Create postmaster on the panel domain: %v", err)
	}
	if repo.created == nil || repo.created.LocalPart != "postmaster" {
		t.Fatalf("want the postmaster row written, got %+v", repo.created)
	}
}

// The database refuses postmaster@ on a tenant domain for doors that do not
// check first (migration 000306). That refusal is a reserved address, not an
// internal error, so importers report it as a skipped mailbox.
func TestCreateForRestore_DatabaseRefusalIsAReservedAddress(t *testing.T) {
	repo := &fakeMBRepo{createErr: mailaddr.ErrPostmasterReserved}
	_, err := CreateForRestore(context.Background(), Deps{Mailboxes: repo, Addresses: okReleaser{}},
		RestoreCreateInput{DomainID: "d1", DomainName: "example.com", LocalPart: "postmaster", PasswordHash: "$2a$10$x"})
	if !errors.Is(err, ErrInvalidLocalPart) || !errors.Is(err, mailaddr.ErrPostmasterReserved) {
		t.Fatalf("want ErrInvalidLocalPart wrapping ErrPostmasterReserved, got %v", err)
	}
	if errors.Is(err, ErrInternal) {
		t.Fatalf("a reserved address is not an internal error: %v", err)
	}
}
