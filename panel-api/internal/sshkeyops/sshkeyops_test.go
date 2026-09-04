package sshkeyops

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/sshkeys"
)

// A valid ed25519 authorized-keys line (ParseAndFingerprint really parses it).
const validKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINhDTlDCUJiIIWOejraVqB0FPMRzhFUhtt7Ih0tnPAPs test@jabali"

// The fake embeds the repository interface and overrides only the methods the
// lifecycle touches; an un-overridden method panics on the nil embed (same
// precedent as domainmailops / dirprivops fakes).
type fakeKeyRepo struct {
	repository.SSHKeyRepository
	createErr  error
	created    *models.SSHKey
	findByID   *models.SSHKey
	findScoped *models.SSHKey
	findErr    error
	deleteErr  error
	deletedID  string
}

func (f *fakeKeyRepo) Create(_ context.Context, k *models.SSHKey) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.created = k
	return nil
}

func (f *fakeKeyRepo) FindByID(_ context.Context, _ string) (*models.SSHKey, error) {
	if f.findErr != nil {
		return nil, f.findErr
	}
	return f.findByID, nil
}

func (f *fakeKeyRepo) FindByIDAndUserID(_ context.Context, _, _ string) (*models.SSHKey, error) {
	if f.findErr != nil {
		return nil, f.findErr
	}
	return f.findScoped, nil
}

func (f *fakeKeyRepo) Delete(_ context.Context, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deletedID = id
	return nil
}

type fakeSched struct{ users []string }

func (f *fakeSched) ScheduleUser(u string) { f.users = append(f.users, u) }

func deps(repo *fakeKeyRepo, sched Scheduler) Deps { return Deps{Keys: repo, Scheduler: sched} }

func TestAdd_HappyPath_PersistsAndSchedulesOnce(t *testing.T) {
	repo := &fakeKeyRepo{}
	sched := &fakeSched{}

	key, err := Add(context.Background(), deps(repo, sched), AddRequest{
		UserID: "user1", Name: "laptop", PublicKey: validKey,
	})
	require.NoError(t, err)
	require.NotNil(t, key)
	require.Equal(t, "user1", key.UserID)
	require.NotEmpty(t, key.ID, "ULID assigned")
	require.NotEmpty(t, key.Fingerprint, "fingerprint computed")
	require.Contains(t, key.PublicKey, "ssh-ed25519", "normalized key stored")

	require.NotNil(t, repo.created, "row persisted")
	require.Equal(t, []string{"user1"}, sched.users, "convergence scheduled exactly once for the owner")
}

func TestAdd_InvalidKey_NoPersist_NoSchedule(t *testing.T) {
	repo := &fakeKeyRepo{}
	sched := &fakeSched{}

	_, err := Add(context.Background(), deps(repo, sched), AddRequest{
		UserID: "user1", Name: "bad", PublicKey: "not a real key",
	})
	require.ErrorIs(t, err, sshkeys.ErrInvalidKeyFormat, "validation sentinel passed through unwrapped")
	require.Nil(t, repo.created, "no row on a validation failure")
	require.Empty(t, sched.users, "nothing scheduled on a validation failure")
}

func TestAdd_Duplicate_MapsConflict_NoSchedule(t *testing.T) {
	repo := &fakeKeyRepo{createErr: repository.ErrConflict}
	sched := &fakeSched{}

	_, err := Add(context.Background(), deps(repo, sched), AddRequest{
		UserID: "user1", Name: "dup", PublicKey: validKey,
	})
	require.ErrorIs(t, err, ErrDuplicate)
	require.Empty(t, sched.users, "a rejected persist schedules nothing")
}

func TestAdd_CreateError_Passthrough_NoSchedule(t *testing.T) {
	repo := &fakeKeyRepo{createErr: errors.New("db down")}
	sched := &fakeSched{}

	_, err := Add(context.Background(), deps(repo, sched), AddRequest{
		UserID: "user1", Name: "x", PublicKey: validKey,
	})
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrDuplicate, "a non-conflict store error is not a duplicate")
	require.Contains(t, err.Error(), "db down")
	require.Empty(t, sched.users)
}

func TestAdd_NilScheduler_NoPanic(t *testing.T) {
	repo := &fakeKeyRepo{}
	key, err := Add(context.Background(), deps(repo, nil), AddRequest{
		UserID: "user1", Name: "laptop", PublicKey: validKey,
	})
	require.NoError(t, err)
	require.NotNil(t, key)
}

func TestFind_OwnerScoped_UsesScopedLookup(t *testing.T) {
	repo := &fakeKeyRepo{
		findByID:   &models.SSHKey{ID: "unscoped"},
		findScoped: &models.SSHKey{ID: "scoped"},
	}
	// OwnerID set → FindByIDAndUserID (returns the scoped row).
	k, err := Find(context.Background(), deps(repo, nil), FindRequest{KeyID: "k1", OwnerID: "user1"})
	require.NoError(t, err)
	require.Equal(t, "scoped", k.ID)

	// OwnerID empty → FindByID (returns the unscoped row).
	k, err = Find(context.Background(), deps(repo, nil), FindRequest{KeyID: "k1"})
	require.NoError(t, err)
	require.Equal(t, "unscoped", k.ID)
}

func TestFind_NotFound_Collapsed(t *testing.T) {
	repo := &fakeKeyRepo{findErr: repository.ErrNotFound}
	// Both missing and not-owned collapse to ErrNotFound (no existence probe).
	_, err := Find(context.Background(), deps(repo, nil), FindRequest{KeyID: "k1", OwnerID: "user1"})
	require.ErrorIs(t, err, ErrNotFound)
	_, err = Find(context.Background(), deps(repo, nil), FindRequest{KeyID: "k1"})
	require.ErrorIs(t, err, ErrNotFound)
}

func TestRemoveKey_DeletesAndSchedulesForOwner(t *testing.T) {
	repo := &fakeKeyRepo{}
	sched := &fakeSched{}
	key := &models.SSHKey{ID: "k1", UserID: "owner9"}

	err := RemoveKey(context.Background(), deps(repo, sched), key)
	require.NoError(t, err)
	require.Equal(t, "k1", repo.deletedID)
	require.Equal(t, []string{"owner9"}, sched.users, "scheduled for the key's owner, not the caller")
}

func TestRemoveKey_DeleteError_NoSchedule(t *testing.T) {
	repo := &fakeKeyRepo{deleteErr: errors.New("db down")}
	sched := &fakeSched{}
	err := RemoveKey(context.Background(), deps(repo, sched), &models.SSHKey{ID: "k1", UserID: "owner9"})
	require.Error(t, err)
	require.Empty(t, sched.users, "a failed delete schedules nothing")
}
