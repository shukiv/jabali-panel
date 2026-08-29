package auth_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// fakeUserRepo is an in-memory UserRepository for bootstrap tests. Kept here
// (not in the auth package proper) so it doesn't leak into production binaries.
// Post-M20 the repo interface shrank — there's no TOTP/refresh-token state to
// mock anymore.
type fakeUserRepo struct {
	mu      sync.Mutex
	byEmail map[string]*models.User
	byID    map[string]*models.User
}

func newFakeUserRepo() *fakeUserRepo {
	return &fakeUserRepo{byEmail: map[string]*models.User{}, byID: map[string]*models.User{}}
}

func (f *fakeUserRepo) Create(_ context.Context, u *models.User) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, dup := f.byEmail[u.Email]; dup {
		return repository.ErrConflict
	}
	c := *u
	f.byEmail[u.Email] = &c
	f.byID[u.ID] = &c
	return nil
}

func (f *fakeUserRepo) FindByID(_ context.Context, id string) (*models.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.byID[id]; ok {
		c := *u
		return &c, nil
	}
	return nil, repository.ErrNotFound
}

func (f *fakeUserRepo) FindByEmail(_ context.Context, email string) (*models.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.byEmail[email]; ok {
		c := *u
		return &c, nil
	}
	return nil, repository.ErrNotFound
}

func (f *fakeUserRepo) FindByKratosIdentityID(_ context.Context, _ string) (*models.User, error) {
	return nil, repository.ErrNotFound
}

func (f *fakeUserRepo) FindByIDs(_ context.Context, _ []string) ([]models.User, error) {
	return nil, nil
}

func (f *fakeUserRepo) FindByUsername(_ context.Context, username string) (*models.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.byID {
		if u.Username != nil && *u.Username == username {
			c := *u
			return &c, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (f *fakeUserRepo) List(context.Context, repository.ListOptions) ([]models.User, int64, error) {
	return nil, 0, nil
}

func (f *fakeUserRepo) Update(_ context.Context, u *models.User) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.byID[u.ID]; !ok {
		return repository.ErrNotFound
	}
	c := *u
	f.byID[u.ID] = &c
	f.byEmail[u.Email] = &c
	return nil
}

func (f *fakeUserRepo) UpdateComposerChannel(ctx context.Context, id string, channel *string) error { return nil }

func (f *fakeUserRepo) UpdateCLIPHPVersion(context.Context, string, *string) error { return nil }

func (f *fakeUserRepo) LinkKratosIdentity(_ context.Context, userID, kratosID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[userID]
	if !ok {
		return repository.ErrNotFound
	}
	u.KratosIdentityID = &kratosID
	return nil
}

func (f *fakeUserRepo) SetAdmin(context.Context, string, bool) error { return nil }
func (f *fakeUserRepo) CountAdmins(context.Context) (int64, error)   { return 0, nil }

func (f *fakeUserRepo) FindAdminsByEmail(_ context.Context) ([]*models.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var admins []*models.User
	for _, u := range f.byID {
		if u.IsAdmin {
			c := *u
			admins = append(admins, &c)
		}
	}
	return admins, nil
}

func (f *fakeUserRepo) SetSuspended(_ context.Context, _ string, _ bool, _ string) error { return nil }

func (f *fakeUserRepo) SetSSHForwardingEnabled(_ context.Context, _ string, _ bool) error {
	return nil
}

func (f *fakeUserRepo) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.byID[id]
	if !ok {
		return repository.ErrNotFound
	}
	delete(f.byID, id)
	delete(f.byEmail, u.Email)
	return nil
}

func seedUser(t *testing.T, users *fakeUserRepo, email, password string, admin bool) *models.User {
	t.Helper()
	hash, err := auth.HashPassword(password, testCost)
	require.NoError(t, err)
	u := &models.User{
		ID:           ids.NewULID(),
		Email:        email,
		PasswordHash: hash,
		IsAdmin:      admin,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
	}
	require.NoError(t, users.Create(context.Background(), u))
	return u
}

func (r *fakeUserRepo) UpdateUsername(context.Context, string, string) error { return nil }

// GH #1238 stub.
func (f *fakeUserRepo) UpdateShadowDBUsernames(context.Context, string, *string, *string) error {
	return nil
}
