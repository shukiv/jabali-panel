package backupmetadata

import (
	"context"
	"strings"
	"testing"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// untrustedSSHRepo has the account's keys and records the ones restored.
type untrustedSSHRepo struct {
	repository.SSHKeyRepository
	existing []models.SSHKey
	created  []string
}

func (r *untrustedSSHRepo) ListByUserID(context.Context, string) ([]models.SSHKey, error) {
	return r.existing, nil
}

func (r *untrustedSSHRepo) Create(_ context.Context, k *models.SSHKey) error {
	r.created = append(r.created, k.Name)
	return nil
}

// GH #1993: an SSH key is a login to the account. From an uploaded file it
// is restored only into an account the restore created; into one that was
// already here each key the account doesn't have is listed instead.
func TestApply_UploadedSSHKeysOnlyIntoACreatedAccount(t *testing.T) {
	alice := "alice"
	meta := &internalbackup.AccountMetadata{
		User: internalbackup.MetadataUser{ID: "u1", Username: &alice},
		SSHKeys: []internalbackup.MetadataSSHKey{
			{ID: "k1", Name: "laptop", PublicKey: "ssh-ed25519 AAAA1 laptop", Fingerprint: "SHA256:aaa"},
			{ID: "k2", Name: "stranger", PublicKey: "ssh-ed25519 AAAA2 stranger", Fingerprint: "SHA256:bbb"},
		},
	}
	const note = "not restored: an uploaded backup brings SSH keys back only into an account the restore created"
	has := []models.SSHKey{{ID: "x", UserID: "u1", Name: "laptop", Fingerprint: "SHA256:aaa"}}

	for _, c := range []struct {
		name               string
		untrusted, created bool
		wantCreated        string
		wantNoted          string
	}{
		{"uploaded into an existing account", true, false, "", `"stranger"`},
		{"uploaded into a created account", true, true, "laptop,stranger", ""},
		{"own backup", false, false, "laptop,stranger", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			keys := &untrustedSSHRepo{existing: has}
			r := Apply(context.Background(), meta, Deps{
				Users: namedUsersRepo{username: alice}, SSHKeys: keys,
				Untrusted: c.untrusted, AccountCreated: c.created,
			})
			if got := strings.Join(keys.created, ","); got != c.wantCreated {
				t.Errorf("keys restored = %q, want %q", got, c.wantCreated)
			}
			var noted []string
			for _, e := range r.Errors {
				if strings.Contains(e, note) {
					noted = append(noted, strings.TrimSuffix(strings.Fields(e)[1], ":"))
				}
			}
			if got := strings.Join(noted, ","); got != c.wantNoted {
				t.Errorf("report names %q, want %q (errors %v)", got, c.wantNoted, r.Errors)
			}
		})
	}
}
