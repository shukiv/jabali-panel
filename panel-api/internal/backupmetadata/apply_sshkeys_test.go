package backupmetadata

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// restoreSSHRepo fails a chosen key ID with a repository conflict (the shape a
// duplicate key restore hits) and counts the rest as persisted.
type restoreSSHRepo struct {
	repository.SSHKeyRepository
	conflictIDs map[string]bool
	created     int
}

func (r *restoreSSHRepo) Create(_ context.Context, k *models.SSHKey) error {
	if r.conflictIDs[k.ID] {
		return repository.ErrConflict
	}
	r.created++
	return nil
}

// The load-bearing behavioral guard for JAB-292 AC4 routing: a duplicate SSH key
// during restore is a SKIP (already present), not a hard error, and it must not
// stop the surrounding restore. A source-pin can't see this — it depends on the
// conflict being recognized as sshkeyops.ErrDuplicate after the row is routed
// through the shared RestoreBatch. Falsify by reverting apply.go's conflict test
// to errors.Is(err, repository.ErrConflict): RestoreBatch returns ErrDuplicate
// (not ErrConflict), so the duplicate lands in r.Errors and r.Skipped stays 0.
func TestApply_SSHKeys_DuplicateSkipsAndContinues(t *testing.T) {
	users := &createGuardUsersRepo{}
	keys := &restoreSSHRepo{conflictIDs: map[string]bool{"k1": true}}
	meta := &internalbackup.AccountMetadata{
		User: internalbackup.MetadataUser{ID: "u1", Email: "u@example.com"},
		SSHKeys: []internalbackup.MetadataSSHKey{
			{ID: "k1", Name: "dup", PublicKey: "ssh-ed25519 AAAA1 dup", Fingerprint: "SHA256:aaa"},
			{ID: "k2", Name: "ok", PublicKey: "ssh-ed25519 AAAA2 ok", Fingerprint: "SHA256:bbb"},
		},
	}

	r := Apply(context.Background(), meta, Deps{Users: users, SSHKeys: keys})

	require.Equal(t, 1, r.SSHKeys, "the non-duplicate key is persisted")
	require.Equal(t, 1, r.Skipped, "the duplicate key is skipped, not counted as created")
	require.Empty(t, r.Errors, "a duplicate SSH key during restore is not a hard error")
	require.Equal(t, 1, keys.created, "exactly the non-duplicate row reached the repo")
}

// Source-pin: apply.go persists SSH keys through the shared restore operation and
// no longer writes ssh_keys rows directly. The negative on d.SSHKeys.Create is
// safe here — §6 was its only call site in apply.go (no sibling shares it), so it
// catches a regression back to direct-write, the exact AC4 point.
func TestApply_SSHKeys_RoutesThroughRestoreBatch(t *testing.T) {
	src, err := os.ReadFile("apply.go")
	require.NoError(t, err)
	s := string(src)

	require.Contains(t, s, "sshkeyops.NewRestoreBatch(",
		"restore must route SSH keys through the shared RestoreBatch (JAB-292 AC4)")
	require.Contains(t, s, "sshkeyops.ErrDuplicate",
		"the duplicate branch must match the module's ErrDuplicate, not repository.ErrConflict")
	require.NotContains(t, s, "d.SSHKeys.Create(",
		"apply.go must not write ssh_keys rows directly any more")
}
