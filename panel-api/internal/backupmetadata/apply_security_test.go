package backupmetadata

import (
	"context"
	"strings"
	"testing"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// createGuardUsersRepo forces the applyUser CREATE path (FindByID → not found)
// and records whether a user row was inserted.
type createGuardUsersRepo struct {
	repository.UserRepository
	created int
}

func (r *createGuardUsersRepo) FindByID(context.Context, string) (*models.User, error) {
	return nil, repository.ErrNotFound
}
func (r *createGuardUsersRepo) Create(context.Context, *models.User) error {
	r.created++
	return nil
}

// recordingKratos records every identity it is asked to mint. IdentityIDByEmail
// returns "" (no identity for the bundle email) so the step-9 mint branch would
// fire if it were reachable.
type recordingKratos struct {
	mints   int
	imports int
}

func (k *recordingKratos) CreateIdentityWithPassword(context.Context, kratosclient.AdminTraits, string) (string, error) {
	k.mints++
	return "id-minted", nil
}
func (k *recordingKratos) ImportIdentities(_ context.Context, ids []kratosclient.ExportedIdentity) error {
	k.imports += len(ids)
	return nil
}
func (k *recordingKratos) IdentityIDByEmail(context.Context, string) (string, error) {
	return "", nil
}
func (k *recordingKratos) IdentityHasPassword(context.Context, string) (bool, error) {
	return false, nil
}

// GH #1408: a backup bundle claiming is_admin=true must NOT create an admin
// user — a crafted uploaded tar would otherwise mint an admin account.
func TestApply_RefusesAdminBundle(t *testing.T) {
	users := &createGuardUsersRepo{}
	meta := &internalbackup.AccountMetadata{
		User: internalbackup.MetadataUser{ID: "u-evil", Email: "attacker@evil.com", IsAdmin: true},
	}
	r := Apply(context.Background(), meta, Deps{Users: users})

	if users.created != 0 {
		t.Fatalf("no user may be created from an admin bundle, got %d", users.created)
	}
	found := false
	for _, e := range r.Errors {
		if strings.Contains(e, "refusing to create an admin") {
			found = true
		}
	}
	if !found {
		t.Errorf("want an admin-refusal error, got %v", r.Errors)
	}
}

// GH #1408: the restore-from-UPLOAD path remaps only user.id into an EXISTING
// target (created=false). The bundle's email + is_admin + password hash are
// attacker-controllable, so step 9 must NOT mint a Kratos identity from them —
// otherwise an uploaded tar could create a matching (admin) login on this box.
func TestApply_NoKratosMintForExistingUser(t *testing.T) {
	kratos := &recordingKratos{}
	// a realistic-length bcrypt hash so the (guarded) mint branch would qualify
	bcrypt := "$2a$10$" + strings.Repeat("a", 53)
	meta := &internalbackup.AccountMetadata{
		User: internalbackup.MetadataUser{ID: "u1", Email: "attacker@evil.com", PasswordHash: bcrypt, IsAdmin: true},
	}
	// existingUsersRepo → applyUser no-ops (created=false), like a real
	// upload-into-existing-user restore.
	r := Apply(context.Background(), meta, Deps{Users: existingUsersRepo{}, KratosClient: kratos})

	if kratos.mints != 0 {
		t.Errorf("step 9 must not mint an identity for a pre-existing user (created=false); minted %d", kratos.mints)
	}
	_ = r
}

// GH #1993: a bundle from an uploaded file can't put a sign-in identity into
// this server's Kratos: its exported identity (traits, state, metadata and a
// password hash) is chosen by whoever wrote the file. From this server's own
// backup the identity is still imported (GH #954).
func TestApply_UploadedBackupImportsNoKratosIdentity(t *testing.T) {
	exported := `{"schema_id":"default","state":"active",` +
		`"traits":{"email":"future-admin@example.com","is_admin":true},` +
		`"credentials":{"password":{"type":"password","identifiers":["future-admin@example.com"],` +
		`"config":{"hashed_password":"$2a$10$` + strings.Repeat("a", 53) + `"}}}}`
	meta := func() *internalbackup.AccountMetadata {
		return &internalbackup.AccountMetadata{
			User:   internalbackup.MetadataUser{ID: "u1", Email: "alice@example.com"},
			Kratos: &internalbackup.MetadataKratos{ExportedIdentity: exported},
		}
	}

	kratos := &recordingKratos{}
	r := Apply(context.Background(), meta(), Deps{Users: existingUsersRepo{}, KratosClient: kratos, Untrusted: true})
	if kratos.imports != 0 {
		t.Fatalf("an uploaded file's identity was imported into Kratos (%d)", kratos.imports)
	}
	if !hasError(r.Errors, "login: the sign-in identity in the uploaded file was not imported") {
		t.Fatalf("errors %v should say the identity was left out", r.Errors)
	}

	kratos = &recordingKratos{}
	Apply(context.Background(), meta(), Deps{Users: existingUsersRepo{}, KratosClient: kratos})
	if kratos.imports != 1 {
		t.Fatalf("this server's own backup: imported %d identities, want 1", kratos.imports)
	}
}
