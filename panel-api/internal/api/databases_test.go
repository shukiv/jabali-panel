package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Mock implementations

type mockDatabaseRepo struct {
	databases []models.Database
	findErr   error
	listErr   error
	createErr error
	deleteErr error
	existsErr error
}

func (m *mockDatabaseRepo) FindByID(ctx context.Context, id string) (*models.Database, error) {
	if m.findErr != nil {
		return nil, m.findErr
	}
	for _, db := range m.databases {
		if db.ID == id {
			return &db, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *mockDatabaseRepo) List(ctx context.Context, opts repository.ListOptions) ([]models.Database, int64, error) {
	if m.listErr != nil {
		return nil, 0, m.listErr
	}
	return m.databases, int64(len(m.databases)), nil
}

func (m *mockDatabaseRepo) ListByUserID(ctx context.Context, userID string, opts repository.ListOptions) ([]models.Database, int64, error) {
	if m.listErr != nil {
		return nil, 0, m.listErr
	}
	var filtered []models.Database
	for _, db := range m.databases {
		if db.UserID == userID {
			filtered = append(filtered, db)
		}
	}
	return filtered, int64(len(filtered)), nil
}

func (m *mockDatabaseRepo) CountByUserID(ctx context.Context, userID string) (int64, error) {
	var count int64
	for _, db := range m.databases {
		if db.UserID == userID {
			count++
		}
	}
	return count, nil
}

func (m *mockDatabaseRepo) Create(ctx context.Context, d *models.Database) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.databases = append(m.databases, *d)
	return nil
}

func (m *mockDatabaseRepo) Delete(ctx context.Context, id string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	for i, db := range m.databases {
		if db.ID == id {
			m.databases = append(m.databases[:i], m.databases[i+1:]...)
			return nil
		}
	}
	return repository.ErrNotFound
}

func (m *mockDatabaseRepo) ExistsByUserAndName(ctx context.Context, userID, name string) (bool, error) {
	if m.existsErr != nil {
		return false, m.existsErr
	}
	for _, db := range m.databases {
		if db.UserID == userID && db.Name == name {
			return true, nil
		}
	}
	return false, nil
}

type mockDatabaseUserRepo struct{}

func (m *mockDatabaseUserRepo) FindByID(ctx context.Context, id string) (*models.DatabaseUser, error) {
	return nil, nil
}

func (m *mockDatabaseUserRepo) List(ctx context.Context, opts repository.ListOptions) ([]models.DatabaseUser, int64, error) {
	return nil, 0, nil
}

func (m *mockDatabaseUserRepo) ListByUserID(ctx context.Context, userID string, opts repository.ListOptions) ([]models.DatabaseUser, int64, error) {
	return nil, 0, nil
}

func (m *mockDatabaseUserRepo) CountByUserID(ctx context.Context, userID string) (int64, error) {
	return 0, nil
}

func (m *mockDatabaseUserRepo) Create(ctx context.Context, du *models.DatabaseUser) error {
	return nil
}

func (m *mockDatabaseUserRepo) Delete(ctx context.Context, id string) error {
	return nil
}

func (m *mockDatabaseUserRepo) UpdatePasswordHash(ctx context.Context, id string, hash string) error {
	return nil
}

func (m *mockDatabaseUserRepo) ExistsByUserAndUsername(ctx context.Context, userID string, username string) (bool, error) {
	return false, nil
}

type mockUserRepo struct {
	users map[string]*models.User
}

func (m *mockUserRepo) Create(ctx context.Context, u *models.User) error {
	return nil
}

func (m *mockUserRepo) FindByID(ctx context.Context, id string) (*models.User, error) {
	if m.users != nil {
		if user, ok := m.users[id]; ok {
			return user, nil
		}
	}
	return nil, nil
}

func (m *mockUserRepo) FindByEmail(ctx context.Context, email string) (*models.User, error) {
	for _, u := range m.users {
		if u.Email == email {
			return u, nil
		}
	}
	return nil, repository.ErrNotFound
}

func (m *mockUserRepo) FindByUsername(ctx context.Context, username string) (*models.User, error) {
	return nil, nil
}

func (m *mockUserRepo) FindByKratosIdentityID(_ context.Context, _ string) (*models.User, error) {
	return nil, repository.ErrNotFound
}

func (m *mockUserRepo) FindByIDs(_ context.Context, _ []string) ([]models.User, error) {
	return nil, nil
}

func (m *mockUserRepo) List(ctx context.Context, opts repository.ListOptions) ([]models.User, int64, error) {
	return nil, 0, nil
}

func (m *mockUserRepo) Update(ctx context.Context, u *models.User) error {
	return nil
}

func (m *mockUserRepo) UpdateCLIPHPVersion(context.Context, string, *string) error { return nil }

func (m *mockUserRepo) LinkKratosIdentity(ctx context.Context, userID, kratosID string) error {
	if m.users != nil {
		if u, ok := m.users[userID]; ok {
			u.KratosIdentityID = &kratosID
		}
	}
	return nil
}

func (m *mockUserRepo) SetAdmin(ctx context.Context, id string, isAdmin bool) error {
	return nil
}

func (m *mockUserRepo) CountAdmins(ctx context.Context) (int64, error) {
	return 0, nil
}

func (m *mockUserRepo) FindAdminsByEmail(ctx context.Context) ([]*models.User, error) {
	return nil, nil
}

func (m *mockUserRepo) SetSuspended(_ context.Context, _ string, _ bool, _ string) error { return nil }

func (m *mockUserRepo) Delete(ctx context.Context, id string) error {
	return nil
}

type mockPackageRepo struct {
	packages map[string]*models.HostingPackage
}

func (m *mockPackageRepo) Create(ctx context.Context, p *models.HostingPackage) error {
	return nil
}

func (m *mockPackageRepo) FindByID(ctx context.Context, id string) (*models.HostingPackage, error) {
	if m.packages != nil {
		if pkg, ok := m.packages[id]; ok {
			return pkg, nil
		}
	}
	return nil, nil
}

func (m *mockPackageRepo) FindByName(ctx context.Context, name string) (*models.HostingPackage, error) {
	return nil, nil
}

func (m *mockPackageRepo) List(ctx context.Context, opts repository.ListOptions) ([]models.HostingPackage, int64, error) {
	return nil, 0, nil
}

func (m *mockPackageRepo) Update(ctx context.Context, p *models.HostingPackage) error {
	return nil
}

func (m *mockPackageRepo) Delete(ctx context.Context, id string) error {
	return nil
}

func (m *mockPackageRepo) EnsureDefaults(ctx context.Context) error {
	return nil
}

// Mock agent for testing
type mockAgent struct {
	callFn      func(ctx context.Context, command string, params any) (json.RawMessage, error)
	callErr     error
	callCount   int
	lastCommand string
}

func (m *mockAgent) Call(ctx context.Context, command string, params any) (json.RawMessage, error) {
	m.callCount++
	m.lastCommand = command
	if m.callFn != nil {
		return m.callFn(ctx, command, params)
	}
	if m.callErr != nil {
		return nil, m.callErr
	}
	return json.RawMessage(`{}`), nil
}

// Helper to setup router with optional claims
func databaseRouter(userID string, isAdmin bool) (*gin.Engine, *mockDatabaseRepo) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")

	// Inject claims if provided
	if userID != "" {
		v1.Use(func(c *gin.Context) {
			ginctx.SetClaims(c, &auth.AccessClaims{
				UserID:  userID,
				IsAdmin: isAdmin,
			})
			c.Next()
		})
	}

	dbRepo := &mockDatabaseRepo{}
	userRepo := &mockUserRepo{
		users: map[string]*models.User{
			"user1": {
				ID:       "user1",
				Username: stringPtr("alice"),
			},
			"user2": {
				ID:       "user2",
				Username: stringPtr("bob"),
			},
			"admin1": {
				ID:       "admin1",
				Username: stringPtr("admin"),
			},
		},
	}
	pkgRepo := &mockPackageRepo{
		packages: map[string]*models.HostingPackage{
			"pkg1": {
				ID:           "pkg1",
				MaxDatabases: 0, // unlimited
			},
		},
	}

	RegisterDatabaseRoutes(v1, DatabaseHandlerConfig{
		Databases:     dbRepo,
		DatabaseUsers: &mockDatabaseUserRepo{},
		Users:         userRepo,
		Packages:      pkgRepo,
		Agent:         &mockAgent{},
	})

	return r, dbRepo
}

// Helper to setup router with agent
func databaseRouterWithAgent(userID string, isAdmin bool, agent *mockAgent) (*gin.Engine, *mockDatabaseRepo) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")

	// Inject claims if provided
	if userID != "" {
		v1.Use(func(c *gin.Context) {
			ginctx.SetClaims(c, &auth.AccessClaims{
				UserID:  userID,
				IsAdmin: isAdmin,
			})
			c.Next()
		})
	}

	dbRepo := &mockDatabaseRepo{}
	userRepo := &mockUserRepo{
		users: map[string]*models.User{
			"user1": {
				ID:       "user1",
				Username: stringPtr("alice"),
			},
			"user2": {
				ID:       "user2",
				Username: stringPtr("bob"),
			},
			"admin1": {
				ID:       "admin1",
				Username: stringPtr("admin"),
			},
		},
	}
	pkgRepo := &mockPackageRepo{
		packages: map[string]*models.HostingPackage{
			"pkg1": {
				ID:           "pkg1",
				MaxDatabases: 0, // unlimited
			},
		},
	}

	cfg := DatabaseHandlerConfig{
		Databases:     dbRepo,
		DatabaseUsers: &mockDatabaseUserRepo{},
		Users:         userRepo,
		Packages:      pkgRepo,
		Agent:         agent,
	}
	RegisterDatabaseRoutes(v1, cfg)

	return r, dbRepo
}

func stringPtr(s string) *string {
	return &s
}

// Test cases

func TestDatabaseListUnauthenticated(t *testing.T) {
	r, _ := databaseRouter("", false)

	req := httptest.NewRequest("GET", "/api/v1/databases", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, "unauthorized", resp["error"])
}

func TestDatabaseListAdmin(t *testing.T) {
	r, dbRepo := databaseRouter("admin1", true)

	// Setup test data
	db1 := models.Database{ID: "id1", UserID: "user1", Name: "db1"}
	db2 := models.Database{ID: "id2", UserID: "user2", Name: "db2"}
	dbRepo.databases = []models.Database{db1, db2}

	req := httptest.NewRequest("GET", "/api/v1/databases", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, float64(2), resp["total"])
	assert.NotNil(t, resp["data"])
}

func TestDatabaseListUser(t *testing.T) {
	r, dbRepo := databaseRouter("user1", false)

	// Setup test data (names include the linux-username prefix to pass
	// the prefix filter applied to non-admin list responses).
	db1 := models.Database{ID: "id1", UserID: "user1", Name: "alice_db1"}
	db2 := models.Database{ID: "id2", UserID: "user2", Name: "bob_db2"}
	dbRepo.databases = []models.Database{db1, db2}

	req := httptest.NewRequest("GET", "/api/v1/databases", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	// User should only see their own database
	assert.Equal(t, float64(1), resp["total"])
}

func TestDatabaseListError(t *testing.T) {
	r, dbRepo := databaseRouter("admin1", true)
	dbRepo.listErr = errors.New("database error")

	req := httptest.NewRequest("GET", "/api/v1/databases", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, "internal", resp["error"])
}

func TestDatabaseGetAdmin(t *testing.T) {
	r, dbRepo := databaseRouter("admin1", true)

	// Setup test data
	db1 := models.Database{ID: "id1", UserID: "user1", Name: "db1"}
	dbRepo.databases = []models.Database{db1}

	req := httptest.NewRequest("GET", "/api/v1/databases/id1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, "id1", resp["id"])
}

func TestDatabaseGetUserOwned(t *testing.T) {
	r, dbRepo := databaseRouter("user1", false)

	// Setup test data - user's own database
	db1 := models.Database{ID: "id1", UserID: "user1", Name: "db1"}
	dbRepo.databases = []models.Database{db1}

	req := httptest.NewRequest("GET", "/api/v1/databases/id1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, "id1", resp["id"])
}

func TestDatabaseGetUserForbidden(t *testing.T) {
	r, dbRepo := databaseRouter("user1", false)

	// Setup test data - database belongs to different user
	db1 := models.Database{ID: "id1", UserID: "user2", Name: "db1"}
	dbRepo.databases = []models.Database{db1}

	req := httptest.NewRequest("GET", "/api/v1/databases/id1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, "forbidden", resp["error"])
}

func TestDatabaseGetNotFound(t *testing.T) {
	r, _ := databaseRouter("admin1", true)

	req := httptest.NewRequest("GET", "/api/v1/databases/nonexistent", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, "not_found", resp["error"])
}

func TestDatabaseGetError(t *testing.T) {
	r, dbRepo := databaseRouter("admin1", true)
	dbRepo.findErr = errors.New("database error")

	req := httptest.NewRequest("GET", "/api/v1/databases/id1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, "internal", resp["error"])
}

// POST /databases tests

func TestDatabaseCreateInvalidName(t *testing.T) {
	agent := &mockAgent{}
	r, _ := databaseRouterWithAgent("user1", false, agent)

	req := httptest.NewRequest("POST", "/api/v1/databases",
		strings.NewReader(`{"name":"123invalid"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, "invalid_database_name", resp["error"])
}

func TestDatabaseCreateNameTooLong(t *testing.T) {
	agent := &mockAgent{}
	r, _ := databaseRouterWithAgent("user1", false, agent)

	req := httptest.NewRequest("POST", "/api/v1/databases",
		strings.NewReader(`{"name":"verylongdatabasenamethatexceedsthirtychars123"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestDatabaseCreateNameCollision(t *testing.T) {
	agent := &mockAgent{}
	r, dbRepo := databaseRouterWithAgent("user1", false, agent)

	// Existing row already has the username prefix baked in (that's the
	// post-create contract now), so a new request for "testdb" by the
	// same user — which becomes "alice_testdb" after prefix — collides.
	existing := models.Database{ID: "id1", UserID: "user1", Name: "alice_testdb"}
	dbRepo.databases = []models.Database{existing}

	req := httptest.NewRequest("POST", "/api/v1/databases",
		strings.NewReader(`{"name":"testdb"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusConflict, w.Code)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, "database_name_exists", resp["error"])
}

func TestDatabaseCreateAgentFailure(t *testing.T) {
	agent := &mockAgent{callErr: errors.New("agent connection failed")}
	r, _ := databaseRouterWithAgent("user1", false, agent)

	req := httptest.NewRequest("POST", "/api/v1/databases",
		strings.NewReader(`{"name":"testdb"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadGateway, w.Code)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, "agent_failed", resp["error"])
}

func TestDatabaseCreateUnauthenticated(t *testing.T) {
	agent := &mockAgent{}
	r, _ := databaseRouterWithAgent("", false, agent)

	req := httptest.NewRequest("POST", "/api/v1/databases",
		strings.NewReader(`{"name":"testdb"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
}

// DELETE /databases/:id tests

func TestDatabaseDeleteSuccess(t *testing.T) {
	agent := &mockAgent{}
	r, dbRepo := databaseRouterWithAgent("user1", false, agent)

	// Pre-create a database
	db := models.Database{ID: "id1", UserID: "user1", Name: "testdb"}
	dbRepo.databases = []models.Database{db}

	req := httptest.NewRequest("DELETE", "/api/v1/databases/id1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, 0, len(dbRepo.databases))
}

func TestDatabaseDeleteNotFound(t *testing.T) {
	agent := &mockAgent{}
	r, _ := databaseRouterWithAgent("user1", false, agent)

	req := httptest.NewRequest("DELETE", "/api/v1/databases/nonexistent", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusNotFound, w.Code)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, "not_found", resp["error"])
}

func TestDatabaseDeleteForbidden(t *testing.T) {
	agent := &mockAgent{}
	r, dbRepo := databaseRouterWithAgent("user1", false, agent)

	// Database belongs to different user
	db := models.Database{ID: "id1", UserID: "user2", Name: "testdb"}
	dbRepo.databases = []models.Database{db}

	req := httptest.NewRequest("DELETE", "/api/v1/databases/id1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, "forbidden", resp["error"])
}

func TestDatabaseDeleteAdminCanDeleteAny(t *testing.T) {
	agent := &mockAgent{}
	r, dbRepo := databaseRouterWithAgent("admin1", true, agent)

	// Database belongs to different user, but admin can delete it
	db := models.Database{ID: "id1", UserID: "user2", Name: "testdb"}
	dbRepo.databases = []models.Database{db}

	req := httptest.NewRequest("DELETE", "/api/v1/databases/id1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusNoContent, w.Code)
}

func TestDatabaseDeleteAgentFailure(t *testing.T) {
	agent := &mockAgent{callErr: errors.New("agent connection failed")}
	r, dbRepo := databaseRouterWithAgent("user1", false, agent)

	db := models.Database{ID: "id1", UserID: "user1", Name: "testdb"}
	dbRepo.databases = []models.Database{db}

	req := httptest.NewRequest("DELETE", "/api/v1/databases/id1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusBadGateway, w.Code)
	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)
	assert.Equal(t, "agent_failed", resp["error"])
}

func TestDatabaseDeleteUnauthenticated(t *testing.T) {
	agent := &mockAgent{}
	r, dbRepo := databaseRouterWithAgent("", false, agent)

	db := models.Database{ID: "id1", UserID: "user1", Name: "testdb"}
	dbRepo.databases = []models.Database{db}

	req := httptest.NewRequest("DELETE", "/api/v1/databases/id1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
}

// GH #1045 PostgreSQL parity — GET /databases/:id/backup dispatches the agent
// verb by engine: Postgres → db.postgres.backup, MariaDB → db.backup. The
// stream path is engine-agnostic, so the mock returns a real temp dump the
// handler streams back (200).
func TestDatabaseBackup_DispatchesByEngine(t *testing.T) {
	cases := []struct {
		name    string
		engine  string
		wantCmd string
	}{
		{"postgres uses pg_dump verb", "postgres", "db.postgres.backup"},
		{"mariadb uses mysqldump verb", "mariadb", "db.backup"},
		{"empty engine defaults to mariadb", "", "db.backup"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dump := filepath.Join(t.TempDir(), "dump.sql")
			if err := os.WriteFile(dump, []byte("-- dump\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			agent := &mockAgent{callFn: func(_ context.Context, _ string, _ any) (json.RawMessage, error) {
				return json.RawMessage(`{"path":"` + dump + `","size_bytes":8}`), nil
			}}
			r, dbRepo := databaseRouterWithAgent("user1", false, agent)
			dbRepo.databases = []models.Database{{ID: "id1", UserID: "user1", Name: "alice_db", Engine: tc.engine}}

			req := httptest.NewRequest("GET", "/api/v1/databases/id1/backup", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			require.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, tc.wantCmd, agent.lastCommand)
		})
	}
}

// GH #1045 — pgGrantedRoles resolves the Postgres roles the restore post-pass
// reassigns ownership + re-grants to: `all` is every granted postgres role
// (mariadb grants filtered out), `owner` is the first of them; empty when the
// database has no postgres grants.
func TestPgGrantedRoles(t *testing.T) {
	h := &databaseHandler{cfg: DatabaseHandlerConfig{
		DatabaseGrants: &pgGrantFake{grants: []models.DatabaseUserGrant{
			{DatabaseUserID: "u1"}, {DatabaseUserID: "u2"}, {DatabaseUserID: "u3"},
		}},
		DatabaseUsers: &pgUserFake{users: map[string]*models.DatabaseUser{
			"u1": {Username: "alice_pg", Engine: "postgres"},
			"u2": {Username: "bob_maria", Engine: "mariadb"}, // filtered out
			"u3": {Username: "carol_pg", Engine: "postgres"},
		}},
	}}
	owner, all := h.pgGrantedRoles(context.Background(), "db1")
	assert.Equal(t, "alice_pg", owner)
	assert.Equal(t, []string{"alice_pg", "carol_pg"}, all)

	// No postgres grants → empty owner + no roles.
	h2 := &databaseHandler{cfg: DatabaseHandlerConfig{
		DatabaseGrants: &pgGrantFake{grants: []models.DatabaseUserGrant{{DatabaseUserID: "u2"}}},
		DatabaseUsers:  &pgUserFake{users: map[string]*models.DatabaseUser{"u2": {Username: "bob", Engine: "mariadb"}}},
	}}
	o2, a2 := h2.pgGrantedRoles(context.Background(), "db1")
	assert.Equal(t, "", o2)
	assert.Empty(t, a2)

	// Nil repos → empty, no panic (fail-safe).
	h3 := &databaseHandler{cfg: DatabaseHandlerConfig{}}
	o3, a3 := h3.pgGrantedRoles(context.Background(), "db1")
	assert.Equal(t, "", o3)
	assert.Nil(t, a3)
}

// Minimal fakes for pgGrantedRoles — embed the interfaces so only the two
// methods the helper calls need bodies (others panic if ever invoked).
type pgGrantFake struct {
	repository.DatabaseUserGrantRepository
	grants []models.DatabaseUserGrant
}

func (f *pgGrantFake) ListByDatabaseID(context.Context, string) ([]models.DatabaseUserGrant, error) {
	return f.grants, nil
}

type pgUserFake struct {
	repository.DatabaseUserRepository
	users map[string]*models.DatabaseUser
}

func (f *pgUserFake) FindByID(_ context.Context, id string) (*models.DatabaseUser, error) {
	return f.users[id], nil
}

func (r *mockUserRepo) UpdateUsername(context.Context, string, string) error { return nil }
