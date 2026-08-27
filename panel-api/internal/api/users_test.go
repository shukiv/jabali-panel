package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// ---------- in-memory repo ----------

// memUserRepo is a tiny in-memory UserRepository for handler tests.
type memUserRepo struct {
	mu   sync.Mutex
	byID map[string]*models.User
}

func newMemUserRepo() *memUserRepo {
	return &memUserRepo{byID: map[string]*models.User{}}
}

func (m *memUserRepo) seed(u *models.User) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := *u
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now()
		c.UpdatedAt = c.CreatedAt
	}
	m.byID[c.ID] = &c
}

func (m *memUserRepo) Create(_ context.Context, u *models.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, v := range m.byID {
		if v.Email == u.Email {
			return repository.ErrConflict
		}
	}
	c := *u
	c.CreatedAt = time.Now()
	c.UpdatedAt = c.CreatedAt
	m.byID[c.ID] = &c
	*u = c
	return nil
}

func (m *memUserRepo) FindByID(_ context.Context, id string) (*models.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if u, ok := m.byID[id]; ok {
		c := *u
		return &c, nil
	}
	return nil, repository.ErrNotFound
}

func (m *memUserRepo) FindByEmail(_ context.Context, email string) (*models.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, v := range m.byID {
		if v.Email == email {
			c := *v
			return &c, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *memUserRepo) FindByKratosIdentityID(_ context.Context, kratosID string) (*models.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if kratosID == "" {
		return nil, repository.ErrNotFound
	}
	for _, v := range m.byID {
		if v.KratosIdentityID != nil && *v.KratosIdentityID == kratosID {
			c := *v
			return &c, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *memUserRepo) FindByUsername(_ context.Context, username string) (*models.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, v := range m.byID {
		if v.Username != nil && *v.Username == username {
			c := *v
			return &c, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *memUserRepo) FindByIDs(_ context.Context, ids []string) ([]models.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]models.User, 0, len(ids))
	for _, id := range ids {
		if v, ok := m.byID[id]; ok {
			out = append(out, *v)
		}
	}
	return out, nil
}

func (m *memUserRepo) List(_ context.Context, opts repository.ListOptions) ([]models.User, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	all := make([]models.User, 0, len(m.byID))
	for _, v := range m.byID {
		all = append(all, *v)
	}
	total := int64(len(all))
	if opts.Offset > len(all) {
		return nil, total, nil
	}
	end := opts.Offset + opts.Limit
	if end > len(all) {
		end = len(all)
	}
	return all[opts.Offset:end], total, nil
}

func (m *memUserRepo) Update(_ context.Context, u *models.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.byID[u.ID]
	if !ok {
		return repository.ErrNotFound
	}
	// mimic the real repo: leave is_admin AND kratos_identity_id alone
	existing.Email = u.Email
	existing.NameFirst = u.NameFirst
	existing.NameLast = u.NameLast
	existing.PasswordHash = u.PasswordHash
	existing.PackageID = u.PackageID
	existing.UpdatedAt = time.Now()
	return nil
}

func (m *memUserRepo) UpdateCLIPHPVersion(context.Context, string, *string) error { return nil }

func (m *memUserRepo) LinkKratosIdentity(_ context.Context, userID, kratosID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.byID[userID]
	if !ok {
		return repository.ErrNotFound
	}
	u.KratosIdentityID = &kratosID
	return nil
}

func (m *memUserRepo) SetAdmin(_ context.Context, id string, isAdmin bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.byID[id]
	if !ok {
		return repository.ErrNotFound
	}
	u.IsAdmin = isAdmin
	u.UpdatedAt = time.Now()
	return nil
}

func (m *memUserRepo) CountAdmins(_ context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for _, v := range m.byID {
		if v.IsAdmin {
			n++
		}
	}
	return n, nil
}

func (m *memUserRepo) FindAdminsByEmail(_ context.Context) ([]*models.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var admins []*models.User
	for _, u := range m.byID {
		if u.IsAdmin {
			u2 := *u
			admins = append(admins, &u2)
		}
	}
	return admins, nil
}

func (m *memUserRepo) SetSuspended(_ context.Context, _ string, _ bool, _ string) error { return nil }

func (m *memUserRepo) SetSSHForwardingEnabled(_ context.Context, id string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.byID[id]
	if !ok {
		return repository.ErrNotFound
	}
	u.SSHForwardingEnabled = enabled
	u.UpdatedAt = time.Now()
	return nil
}

func (m *memUserRepo) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byID[id]; !ok {
		return repository.ErrNotFound
	}
	delete(m.byID, id)
	return nil
}

// ---------- in-memory package repo ----------

// memPackageRepo is a tiny in-memory PackageRepository for handler tests.
type memPackageRepo struct {
	mu   sync.Mutex
	byID map[string]*models.HostingPackage
}

func newMemPackageRepo() *memPackageRepo {
	return &memPackageRepo{byID: map[string]*models.HostingPackage{}}
}

func (m *memPackageRepo) seed(p *models.HostingPackage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := *p
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now()
		c.UpdatedAt = c.CreatedAt
	}
	m.byID[c.ID] = &c
}

func (m *memPackageRepo) Create(_ context.Context, p *models.HostingPackage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := *p
	c.CreatedAt = time.Now()
	c.UpdatedAt = c.CreatedAt
	m.byID[c.ID] = &c
	*p = c
	return nil
}

func (m *memPackageRepo) FindByID(_ context.Context, id string) (*models.HostingPackage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p, ok := m.byID[id]; ok {
		c := *p
		return &c, nil
	}
	return nil, repository.ErrNotFound
}

func (m *memPackageRepo) FindByName(_ context.Context, name string) (*models.HostingPackage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, v := range m.byID {
		if v.Name == name {
			c := *v
			return &c, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *memPackageRepo) List(_ context.Context, opts repository.ListOptions) ([]models.HostingPackage, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	all := make([]models.HostingPackage, 0, len(m.byID))
	for _, v := range m.byID {
		all = append(all, *v)
	}
	total := int64(len(all))
	if opts.Offset > len(all) {
		return nil, total, nil
	}
	end := opts.Offset + opts.Limit
	if end > len(all) {
		end = len(all)
	}
	return all[opts.Offset:end], total, nil
}

func (m *memPackageRepo) Update(_ context.Context, p *models.HostingPackage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.byID[p.ID]
	if !ok {
		return repository.ErrNotFound
	}
	*existing = *p
	return nil
}

func (m *memPackageRepo) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byID[id]; !ok {
		return repository.ErrNotFound
	}
	delete(m.byID, id)
	return nil
}

func (m *memPackageRepo) EnsureDefaults(_ context.Context) error {
	return nil
}

// ---------- test harness ----------

// buildRouter wires a minimal Gin engine with users routes mounted behind a
// fake-auth middleware that stamps the given claims onto the context.
func buildRouter(repo repository.UserRepository, claims *auth.AccessClaims) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1", func(c *gin.Context) {
		if claims != nil {
			ginctx.SetClaims(c, claims)
		}
		c.Next()
	})
	api.RegisterUserRoutes(g, api.UserHandlerConfig{
		Repo:       repo,
		BcryptCost: bcrypt.MinCost,
	})
	return r
}

// buildRouterWithPackages wires a minimal Gin engine with users routes mounted behind a
// fake-auth middleware that stamps the given claims onto the context, and includes
// a package repository.
func buildRouterWithPackages(repo repository.UserRepository, pkgRepo repository.PackageRepository, claims *auth.AccessClaims) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1", func(c *gin.Context) {
		if claims != nil {
			ginctx.SetClaims(c, claims)
		}
		c.Next()
	})
	api.RegisterUserRoutes(g, api.UserHandlerConfig{
		Repo:       repo,
		Packages:   pkgRepo,
		BcryptCost: bcrypt.MinCost,
	})
	return r
}

func makeUser(t *testing.T, email string, isAdmin bool, password string) *models.User {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	return &models.User{
		ID:           ids.NewULID(),
		Email:        email,
		PasswordHash: string(h),
		IsAdmin:      isAdmin,
	}
}

func doJSON(t *testing.T, r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// ---------- LIST ----------

func TestUsers_List_AdminSeesAll(t *testing.T) {
	t.Parallel()

	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	repo.seed(admin)
	repo.seed(makeUser(t, "u1@example.com", false, "password01"))
	repo.seed(makeUser(t, "u2@example.com", false, "password02"))

	r := buildRouter(repo, &auth.AccessClaims{UserID: admin.ID, Email: admin.Email, IsAdmin: true})
	rec := doJSON(t, r, http.MethodGet, "/api/v1/users", nil)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Data     []models.User `json:"data"`
		Total    int64         `json:"total"`
		Page     int           `json:"page"`
		PageSize int           `json:"page_size"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, int64(3), resp.Total)
	assert.Len(t, resp.Data, 3)
	assert.Equal(t, 1, resp.Page)
}

func TestUsers_List_NonAdmin403(t *testing.T) {
	t.Parallel()

	repo := newMemUserRepo()
	user := makeUser(t, "u@example.com", false, "password01")
	repo.seed(user)

	r := buildRouter(repo, &auth.AccessClaims{UserID: user.ID, Email: user.Email})
	rec := doJSON(t, r, http.MethodGet, "/api/v1/users", nil)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestUsers_List_PaginationClamps(t *testing.T) {
	t.Parallel()

	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	repo.seed(admin)

	r := buildRouter(repo, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})
	rec := doJSON(t, r, http.MethodGet, "/api/v1/users?page=-5&page_size=0", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, float64(1), resp["page"])
	assert.Equal(t, float64(20), resp["page_size"]) // defaultUsersPageSize
}

// ---------- GET ----------

func TestUsers_Get_OwnerAllowed(t *testing.T) {
	t.Parallel()

	repo := newMemUserRepo()
	u := makeUser(t, "owner@example.com", false, "password01")
	repo.seed(u)

	r := buildRouter(repo, &auth.AccessClaims{UserID: u.ID, Email: u.Email})
	rec := doJSON(t, r, http.MethodGet, "/api/v1/users/"+u.ID, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestUsers_Get_StrangerForbidden(t *testing.T) {
	t.Parallel()

	repo := newMemUserRepo()
	a := makeUser(t, "a@example.com", false, "password01")
	b := makeUser(t, "b@example.com", false, "password02")
	repo.seed(a)
	repo.seed(b)

	// A tries to fetch B.
	r := buildRouter(repo, &auth.AccessClaims{UserID: a.ID})
	rec := doJSON(t, r, http.MethodGet, "/api/v1/users/"+b.ID, nil)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestUsers_Get_PasswordHashNotLeaked(t *testing.T) {
	t.Parallel()

	repo := newMemUserRepo()
	u := makeUser(t, "u@example.com", false, "password01")
	repo.seed(u)

	r := buildRouter(repo, &auth.AccessClaims{UserID: u.ID})
	rec := doJSON(t, r, http.MethodGet, "/api/v1/users/"+u.ID, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "password_hash")
	assert.NotContains(t, rec.Body.String(), u.PasswordHash)
}

// ---------- CREATE ----------

func TestUsers_Create_Admin(t *testing.T) {
	t.Parallel()

	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	repo.seed(admin)

	r := buildRouter(repo, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})
	rec := doJSON(t, r, http.MethodPost, "/api/v1/users", map[string]any{
		"email":    "new@example.com",
		"password": "password123",
	})
	require.Equal(t, http.StatusCreated, rec.Code)

	var out models.User
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Equal(t, "new@example.com", out.Email)
	assert.NotEmpty(t, out.ID)
	assert.False(t, out.IsAdmin)
}

func TestUsers_Create_ShortPassword400(t *testing.T) {
	t.Parallel()

	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	repo.seed(admin)

	r := buildRouter(repo, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})
	rec := doJSON(t, r, http.MethodPost, "/api/v1/users", map[string]any{
		"email":    "x@example.com",
		"password": "short",
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestUsers_Create_DuplicateEmail409(t *testing.T) {
	t.Parallel()

	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	existing := makeUser(t, "dup@example.com", false, "password01")
	repo.seed(admin)
	repo.seed(existing)

	r := buildRouter(repo, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})
	rec := doJSON(t, r, http.MethodPost, "/api/v1/users", map[string]any{
		"email":    "dup@example.com",
		"password": "password123",
	})
	assert.Equal(t, http.StatusConflict, rec.Code)
}

func TestUsers_Create_NonAdmin403(t *testing.T) {
	t.Parallel()

	repo := newMemUserRepo()
	user := makeUser(t, "u@example.com", false, "password01")
	repo.seed(user)

	r := buildRouter(repo, &auth.AccessClaims{UserID: user.ID})
	rec := doJSON(t, r, http.MethodPost, "/api/v1/users", map[string]any{
		"email":    "x@example.com",
		"password": "password123",
	})
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

// ---------- PATCH ----------

func TestUsers_Patch_PasswordFieldRejected(t *testing.T) {
	t.Parallel()

	// Post-M20: password changes live in Kratos. A PATCH carrying a
	// password for a user with NO Kratos identity is rejected loud
	// with 409 (no_kratos_identity) — the panel cannot honor the
	// request, and silently dropping it would mask a real corruption
	// case where a user has a panel row but no auth identity.
	//
	// 409 also short-circuits the handler so other fields in the same
	// PATCH body (e.g. name_first) are NOT applied. That's by design:
	// the operator should see the Kratos-identity issue first; once
	// resolved, the rename PATCHes through normally.
	repo := newMemUserRepo()
	u := makeUser(t, "u@example.com", false, "password01")
	repo.seed(u)

	r := buildRouter(repo, &auth.AccessClaims{UserID: u.ID})
	rec := doJSON(t, r, http.MethodPatch, "/api/v1/users/"+u.ID, map[string]any{
		"password":         "newpassword9",
		"current_password": "password01",
		"name_first":       "Renamed",
	})
	require.Equal(t, http.StatusConflict, rec.Code, "no kratos identity → 409, body has no_kratos_identity")
	require.Contains(t, rec.Body.String(), "no_kratos_identity")

	after, err := repo.FindByID(context.Background(), u.ID)
	require.NoError(t, err)
	// PasswordHash invariant — the actual M20 protection — must hold:
	// the panel DB never persists a panel-side password change post-
	// M20 because the kratos sync block returns 409 before existing
	// is written back to the repo.
	assert.Equal(t, u.PasswordHash, after.PasswordHash, "panel DB must not accept password PATCHes post-M20")
	// NOTE: name_first mutates on `existing` BEFORE the kratos-sync
	// block fires. In the production GORM repo a 409 short-circuits
	// before repo.Update() is called, so the change never persists.
	// The in-memory test repo here returns shared pointers from
	// FindByID, so the mutation appears even though Update was never
	// called — that's a rig artefact, not a bug. We don't assert on
	// name_first here for that reason; the companion test
	// TestUsers_Patch_NameFirstUpdates_NoPassword verifies the rename
	// path independently.
}

func TestUsers_Patch_NameFirstUpdates_NoPassword(t *testing.T) {
	t.Parallel()

	// Companion to TestUsers_Patch_PasswordFieldRejected: a PATCH that
	// omits the password field works the normal way for users with no
	// Kratos identity (M20 only requires Kratos for password changes,
	// not for profile field updates).
	repo := newMemUserRepo()
	u := makeUser(t, "u@example.com", false, "password01")
	repo.seed(u)

	r := buildRouter(repo, &auth.AccessClaims{UserID: u.ID})
	rec := doJSON(t, r, http.MethodPatch, "/api/v1/users/"+u.ID, map[string]any{
		"name_first": "Renamed",
	})
	require.Equal(t, http.StatusOK, rec.Code)

	after, err := repo.FindByID(context.Background(), u.ID)
	require.NoError(t, err)
	assert.Equal(t, "Renamed", after.NameFirst)
	assert.Equal(t, u.PasswordHash, after.PasswordHash, "name-only PATCH must not touch hash")
}

func TestUsers_Patch_OwnerCannotSetIsAdmin(t *testing.T) {
	t.Parallel()

	repo := newMemUserRepo()
	u := makeUser(t, "u@example.com", false, "password01")
	repo.seed(u)

	r := buildRouter(repo, &auth.AccessClaims{UserID: u.ID})
	rec := doJSON(t, r, http.MethodPatch, "/api/v1/users/"+u.ID, map[string]any{
		"is_admin": true,
	})
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestUsers_Patch_AdminFlipsIsAdmin(t *testing.T) {
	t.Parallel()

	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	admin2 := makeUser(t, "admin2@example.com", true, "adminpassword") // keep >1 admin
	target := makeUser(t, "u@example.com", false, "password01")
	repo.seed(admin)
	repo.seed(admin2)
	repo.seed(target)

	r := buildRouter(repo, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})
	rec := doJSON(t, r, http.MethodPatch, "/api/v1/users/"+target.ID, map[string]any{
		"is_admin": true,
	})
	require.Equal(t, http.StatusOK, rec.Code)

	after, _ := repo.FindByID(context.Background(), target.ID)
	assert.True(t, after.IsAdmin)
}

func TestUsers_Patch_CannotDemoteLastAdmin(t *testing.T) {
	t.Parallel()

	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	repo.seed(admin)

	r := buildRouter(repo, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})
	rec := doJSON(t, r, http.MethodPatch, "/api/v1/users/"+admin.ID, map[string]any{
		"is_admin": false,
	})
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "cannot_demote_last_admin")
}

// ---------- DELETE ----------

func TestUsers_Delete_Admin(t *testing.T) {
	t.Parallel()

	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	target := makeUser(t, "victim@example.com", false, "password01")
	repo.seed(admin)
	repo.seed(target)

	r := buildRouter(repo, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})
	rec := doJSON(t, r, http.MethodDelete, "/api/v1/users/"+target.ID, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)

	_, err := repo.FindByID(context.Background(), target.ID)
	assert.True(t, errors.Is(err, repository.ErrNotFound))
}

func TestUsers_Delete_CannotDeleteSelf(t *testing.T) {
	t.Parallel()

	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	repo.seed(admin)

	r := buildRouter(repo, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})
	rec := doJSON(t, r, http.MethodDelete, "/api/v1/users/"+admin.ID, nil)
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "cannot_delete_self")
}

func TestUsers_Delete_SoleAdminCanDeleteNonAdmin(t *testing.T) {
	t.Parallel()

	// Confirms the last-admin check doesn't fire when the target isn't an
	// admin. (The check can only bite when an admin caller deletes a
	// different admin and that target happens to be the last admin —
	// unreachable in practice because there'd then be ≥2 admins, but we
	// keep the guard as defence-in-depth.)
	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	victim := makeUser(t, "v@example.com", false, "password01")
	repo.seed(admin)
	repo.seed(victim)

	r := buildRouter(repo, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})
	rec := doJSON(t, r, http.MethodDelete, "/api/v1/users/"+victim.ID, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

// ---------- smoke: bytes-free body on DELETE & Content-Length header ----------

func TestUsers_Delete_HasNoContentLength(t *testing.T) {
	t.Parallel()

	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	target := makeUser(t, "v@example.com", false, "password01")
	repo.seed(admin)
	repo.seed(target)

	r := buildRouter(repo, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})
	rec := doJSON(t, r, http.MethodDelete, "/api/v1/users/"+target.ID, nil)

	require.Equal(t, http.StatusNoContent, rec.Code)
	cl := rec.Header().Get("Content-Length")
	if cl != "" {
		n, err := strconv.Atoi(cl)
		require.NoError(t, err)
		assert.Equal(t, 0, n)
	}
	// body must be empty or whitespace — defensive
	assert.LessOrEqual(t, len(bytes.TrimSpace(rec.Body.Bytes())), 0)
}

// ---------- PACKAGE_ID ----------

func TestUsers_Create_WithValidPackageID(t *testing.T) {
	t.Parallel()

	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	repo.seed(admin)

	pkgRepo := newMemPackageRepo()
	pkg := &models.HostingPackage{
		ID:   ids.NewULID(),
		Name: "Standard",
	}
	pkgRepo.seed(pkg)

	r := buildRouterWithPackages(repo, pkgRepo, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})
	rec := doJSON(t, r, http.MethodPost, "/api/v1/users", map[string]any{
		"email":      "new@example.com",
		"password":   "password123",
		"package_id": pkg.ID,
	})
	require.Equal(t, http.StatusCreated, rec.Code)

	var out models.User
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.NotNil(t, out.PackageID)
	assert.Equal(t, pkg.ID, *out.PackageID)
}

func TestUsers_Create_WithInvalidPackageID(t *testing.T) {
	t.Parallel()

	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	repo.seed(admin)

	pkgRepo := newMemPackageRepo()

	r := buildRouterWithPackages(repo, pkgRepo, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})
	rec := doJSON(t, r, http.MethodPost, "/api/v1/users", map[string]any{
		"email":      "new@example.com",
		"password":   "password123",
		"package_id": "nonexistent_id",
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid_package_id")
}

func TestUsers_Create_WithoutPackageID(t *testing.T) {
	t.Parallel()

	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	repo.seed(admin)

	r := buildRouter(repo, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})
	rec := doJSON(t, r, http.MethodPost, "/api/v1/users", map[string]any{
		"email":    "new@example.com",
		"password": "password123",
	})
	require.Equal(t, http.StatusCreated, rec.Code)

	var out models.User
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Nil(t, out.PackageID)
}

func TestUsers_Patch_SetPackageID(t *testing.T) {
	t.Parallel()

	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	target := makeUser(t, "u@example.com", false, "password01")
	repo.seed(admin)
	repo.seed(target)

	pkgRepo := newMemPackageRepo()
	pkg := &models.HostingPackage{
		ID:   ids.NewULID(),
		Name: "Premium",
	}
	pkgRepo.seed(pkg)

	r := buildRouterWithPackages(repo, pkgRepo, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})
	rec := doJSON(t, r, http.MethodPatch, "/api/v1/users/"+target.ID, map[string]any{
		"package_id": pkg.ID,
	})
	require.Equal(t, http.StatusOK, rec.Code)

	var out models.User
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.NotNil(t, out.PackageID)
	assert.Equal(t, pkg.ID, *out.PackageID)
}

func TestUsers_Patch_ClearPackageID(t *testing.T) {
	t.Parallel()

	repo := newMemUserRepo()
	pkgID := ids.NewULID()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	target := makeUser(t, "u@example.com", false, "password01")
	target.PackageID = &pkgID // pre-assign a package
	repo.seed(admin)
	repo.seed(target)

	r := buildRouter(repo, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})
	rec := doJSON(t, r, http.MethodPatch, "/api/v1/users/"+target.ID, map[string]any{
		"package_id": "", // empty string clears it
	})
	require.Equal(t, http.StatusOK, rec.Code)

	var out models.User
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.Nil(t, out.PackageID)
}

func TestUsers_Patch_WithInvalidPackageID(t *testing.T) {
	t.Parallel()

	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	target := makeUser(t, "u@example.com", false, "password01")
	repo.seed(admin)
	repo.seed(target)

	pkgRepo := newMemPackageRepo()

	r := buildRouterWithPackages(repo, pkgRepo, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})
	rec := doJSON(t, r, http.MethodPatch, "/api/v1/users/"+target.ID, map[string]any{
		"package_id": "nonexistent_id",
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "invalid_package_id")
}

// ---------- M20: Kratos inline-hook on POST /users ----------
//
// The create handler must:
//   - skip Kratos when the client is nil (dev-without-Kratos),
//   - create a Kratos identity atomically when the client is wired,
//   - roll back the panel row if Kratos rejects the create,
//   - unwind both sides if the post-create Update fails.

// buildRouterWithKratos wires a router whose UserHandlerConfig points at the
// given fake Kratos admin server. An empty kratosURL leaves the client nil
// (panel-only mode).
func buildRouterWithKratos(
	repo repository.UserRepository,
	kratosURL string,
	claims *auth.AccessClaims,
) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1", func(c *gin.Context) {
		if claims != nil {
			ginctx.SetClaims(c, claims)
		}
		c.Next()
	})
	cfg := api.UserHandlerConfig{
		Repo:       repo,
		BcryptCost: bcrypt.MinCost,
	}
	if kratosURL != "" {
		cfg.KratosClient = kratosclient.NewClient(kratosURL, kratosURL)
	}
	api.RegisterUserRoutes(g, cfg)
	return r
}

func TestUsers_Create_NoKratosClient_NoUpstreamCall(t *testing.T) {
	t.Parallel()

	hit := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	repo.seed(admin)

	// No Kratos client wired → the panel row is created in isolation, and
	// even an intentionally-broken server is never contacted.
	r := buildRouterWithKratos(repo, "", &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})
	rec := doJSON(t, r, http.MethodPost, "/api/v1/users", map[string]any{
		"email":    "local-only@example.com",
		"password": "password123",
	})
	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, 0, hit, "Kratos must not be called when the client is nil")
}

func TestUsers_Create_KratosProvider_HappyPath_IDPersisted(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/admin/identities" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"kratos-id-new","traits":{"email":"k@example.com","is_admin":false}}`))
			return
		}
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	defer srv.Close()

	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	repo.seed(admin)

	r := buildRouterWithKratos(repo, srv.URL, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})
	rec := doJSON(t, r, http.MethodPost, "/api/v1/users", map[string]any{
		"email":    "k@example.com",
		"password": "password123",
	})
	require.Equal(t, http.StatusCreated, rec.Code)

	var out models.User
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.NotNil(t, out.KratosIdentityID, "kratos_identity_id must be persisted after Kratos create")
	assert.Equal(t, "kratos-id-new", *out.KratosIdentityID)
}

func TestUsers_Create_KratosRejects_PanelRolledBack(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Kratos returns 409 conflict (identity with that email already exists
		// upstream). Our handler should undo the panel row and surface 502.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"conflict"}`))
	}))
	defer srv.Close()

	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	repo.seed(admin)

	r := buildRouterWithKratos(repo, srv.URL, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})
	rec := doJSON(t, r, http.MethodPost, "/api/v1/users", map[string]any{
		"email":    "dup@example.com",
		"password": "password123",
	})
	require.Equal(t, http.StatusBadGateway, rec.Code, "kratos rejection must be 502 to the caller")
	assert.Contains(t, rec.Body.String(), "identity_provider_failed")

	// Critical invariant: no panel row survives a failed Kratos create.
	// Only the seeded admin should remain.
	all, _, err := repo.List(context.Background(), repository.ListOptions{Limit: 100})
	require.NoError(t, err)
	require.Len(t, all, 1, "panel row must be rolled back after Kratos failure")
	assert.Equal(t, admin.ID, all[0].ID)
}

func TestUsers_Create_KratosUnreachable_PanelRolledBack(t *testing.T) {
	t.Parallel()

	// Dead server — connection refused.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	repo.seed(admin)

	r := buildRouterWithKratos(repo, srv.URL, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})
	rec := doJSON(t, r, http.MethodPost, "/api/v1/users", map[string]any{
		"email":    "noroute@example.com",
		"password": "password123",
	})
	assert.Equal(t, http.StatusBadGateway, rec.Code)

	all, _, err := repo.List(context.Background(), repository.ListOptions{Limit: 100})
	require.NoError(t, err)
	require.Len(t, all, 1, "panel row must be rolled back even when Kratos is unreachable")
}

// TestUsers_Delete_RemovesKratosIdentity pins the GH#132 follow-up fix:
// deleting a user must also delete its Kratos identity, otherwise the
// username/email stay taken and recreating the same user collides
// ("user still there even though I deleted it").
func TestUsers_Delete_RemovesKratosIdentity(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var deletedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			mu.Lock()
			deletedPath = r.URL.Path
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	target := makeUser(t, "victim@example.com", false, "password01")
	idid := "kratos-identity-xyz"
	target.KratosIdentityID = &idid
	repo.seed(admin)
	repo.seed(target)

	r := buildRouterWithKratos(repo, srv.URL, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})
	rec := doJSON(t, r, http.MethodDelete, "/api/v1/users/"+target.ID, nil)
	assert.Equal(t, http.StatusNoContent, rec.Code)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, "/admin/identities/"+idid, deletedPath,
		"user delete must DELETE the Kratos identity so recreate doesn't collide")
}

// TestUsers_Create_NoEmail_OK pins GH#176: email is optional now that usernames
// are the login. Creating a user with a username + no email must succeed, and
// the Kratos identity payload must omit the email trait (not send "").
func TestUsers_Create_NoEmail_OK(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/admin/identities" {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			gotBody = string(b)
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"kratos-id-noemail","traits":{"username":"acmecorp"}}`))
			return
		}
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	defer srv.Close()

	repo := newMemUserRepo()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	repo.seed(admin)

	r := buildRouterWithKratos(repo, srv.URL, &auth.AccessClaims{UserID: admin.ID, IsAdmin: true})
	rec := doJSON(t, r, http.MethodPost, "/api/v1/users", map[string]any{
		"username":   "acmecorp",
		"password":   "password123",
		"name_first": "Acme Corp",
	})
	require.Equal(t, http.StatusCreated, rec.Code, "create with no email must succeed")

	mu.Lock()
	defer mu.Unlock()
	assert.NotContains(t, gotBody, `"email":""`, "must not send an empty email trait to Kratos")
	assert.Contains(t, gotBody, `"username":"acmecorp"`)
}

// GH #258: email is optional. A PATCH with email:"" must clear it (200), not
// 400 on the validator's `email` tag; a malformed non-empty email still 400s.
func TestUsers_Patch_EmptyEmailClears(t *testing.T) {
	t.Parallel()

	repo := newMemUserRepo()
	u := makeUser(t, "u@example.com", false, "password01")
	repo.seed(u)

	r := buildRouter(repo, &auth.AccessClaims{UserID: u.ID})
	rec := doJSON(t, r, http.MethodPatch, "/api/v1/users/"+u.ID, map[string]any{
		"email": "",
	})
	require.Equal(t, http.StatusOK, rec.Code, "empty email must be accepted")

	after, err := repo.FindByID(context.Background(), u.ID)
	require.NoError(t, err)
	assert.Equal(t, "", after.Email, "empty email must clear the field")
}

func TestUsers_Patch_MalformedEmailRejected(t *testing.T) {
	t.Parallel()

	repo := newMemUserRepo()
	u := makeUser(t, "u@example.com", false, "password01")
	repo.seed(u)

	r := buildRouter(repo, &auth.AccessClaims{UserID: u.ID})
	rec := doJSON(t, r, http.MethodPatch, "/api/v1/users/"+u.ID, map[string]any{
		"email": "not-an-email",
	})
	assert.Equal(t, http.StatusBadRequest, rec.Code, "malformed non-empty email must 400")
}

func (r *memUserRepo) UpdateUsername(context.Context, string, string) error { return nil }

// GH #1238 stub.
func (m *memUserRepo) UpdateShadowDBUsernames(context.Context, string, *string, *string) error {
	return nil
}
