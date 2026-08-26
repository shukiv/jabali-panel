package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"log/slog"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// generateTestKey creates a random 32-byte AES-256 key for testing.
func generateTestKeyValidate(t *testing.T) ssokey.Key {
	var k ssokey.Key
	if _, err := rand.Read(k[:]); err != nil {
		t.Fatalf("failed to generate test key: %v", err)
	}
	return k
}

// TestValidate_HappyPath tests token validation with valid credentials.
func TestValidate_HappyPath(t *testing.T) {
	key := generateTestKeyValidate(t)

	// Prepare encrypted password
	plaintextPwd := []byte("secret123")
	encryptedPwd, err := key.Seal(plaintextPwd)
	require.NoError(t, err)

	// Generate a token
	tokenBytes := []byte("test-token-32-bytes-abcdefghijk")
	plaintextToken := base64.RawURLEncoding.EncodeToString(tokenBytes)
	hash := sha256.Sum256(tokenBytes)
	hashStr := fmt.Sprintf("%x", hash[:])

	mockDBs := &mockDatabaseRepoValidate{
		databases: map[string]*models.Database{
			"db1": {
				ID:     "db1",
				Name:   "testdb",
				UserID: "user1",
			},
		},
	}

	mockUsers := &mockUserRepoValidate{
		users: map[string]*models.User{
			"user1": {
				ID:                    "user1",
				Username:              ptrString("testuser"),
				MysqladminUsername:    ptrString("testuser_mysqladmin"),
				MysqladminPasswordEnc: encryptedPwd,
			},
		},
	}

	mockTokens := &mockSSOTokenRepoValidate{
		tokens: map[string]*models.PhpMyAdminSSOToken{
			hashStr: {
				ID:         "token1",
				UserID:     "user1",
				DatabaseID: "db1",
				TokenHash:  hashStr,
				ExpiresAt:  time.Now().Add(5 * time.Minute),
			},
		},
	}

	cfg := SSOPhpMyAdminValidateHandlerConfig{
		Databases: mockDBs,
		Users:     mockUsers,
		Tokens:    mockTokens,
		SSOKey:    &key,
		Log:       slog.Default(),
	}

	h := &ssoPhpMyAdminValidateHandler{cfg: cfg}

	body := ssoValidateRequest{Token: plaintextToken}
	bodyJSON, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/sso/phpmyadmin/validate", bytes.NewReader(bodyJSON))
	c.Request.Header.Set("Content-Type", "application/json")

	h.validate(c)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp ssoValidateResponse
	err = json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, "testuser_mysqladmin", resp.User)
	assert.Equal(t, "secret123", resp.Password)
	assert.Equal(t, "localhost", resp.Host)
	assert.Equal(t, 3306, resp.Port)
	assert.Equal(t, "/var/run/mysqld/mysqld.sock", resp.Socket)
	assert.Equal(t, "testdb", resp.OnlyDB)
	assert.Equal(t, "testdb", resp.DB)
}

// TestValidate_UnknownToken tests 404 for non-existent token.
func TestValidate_UnknownToken(t *testing.T) {
	key := generateTestKeyValidate(t)

	// Generate a valid 32-byte token that won't exist in the repo
	tokenBytes := []byte("valid-32-byte-token-not-in-repo!")
	plaintextToken := base64.RawURLEncoding.EncodeToString(tokenBytes)

	mockDBs := &mockDatabaseRepoValidate{}
	mockUsers := &mockUserRepoValidate{}
	mockTokens := &mockSSOTokenRepoValidate{
		tokens: make(map[string]*models.PhpMyAdminSSOToken),
	}

	cfg := SSOPhpMyAdminValidateHandlerConfig{
		Databases: mockDBs,
		Users:     mockUsers,
		Tokens:    mockTokens,
		SSOKey:    &key,
		Log:       slog.Default(),
	}

	h := &ssoPhpMyAdminValidateHandler{cfg: cfg}

	body := ssoValidateRequest{Token: plaintextToken}
	bodyJSON, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/sso/phpmyadmin/validate", bytes.NewReader(bodyJSON))
	c.Request.Header.Set("Content-Type", "application/json")

	h.validate(c)

	assert.Equal(t, http.StatusNotFound, w.Code)

	var resp ssoErrorResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Equal(t, "not_found", resp.Error)
}

// TestValidate_ReplayToken tests 410 for already-consumed token.
func TestValidate_ReplayToken(t *testing.T) {
	key := generateTestKeyValidate(t)

	tokenBytes := []byte("test-token-32-bytes-abcdefghijk")
	plaintextToken := base64.RawURLEncoding.EncodeToString(tokenBytes)

	mockDBs := &mockDatabaseRepoValidate{}
	mockUsers := &mockUserRepoValidate{}
	mockTokens := &mockSSOTokenRepoValidate{
		tokens:       make(map[string]*models.PhpMyAdminSSOToken),
		consumeError: repository.ErrNotFound, // Simulate already consumed
	}

	cfg := SSOPhpMyAdminValidateHandlerConfig{
		Databases: mockDBs,
		Users:     mockUsers,
		Tokens:    mockTokens,
		SSOKey:    &key,
		Log:       slog.Default(),
	}

	h := &ssoPhpMyAdminValidateHandler{cfg: cfg}

	body := ssoValidateRequest{Token: plaintextToken}
	bodyJSON, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/sso/phpmyadmin/validate", bytes.NewReader(bodyJSON))
	c.Request.Header.Set("Content-Type", "application/json")

	h.validate(c)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestValidate_InvalidTokenEncoding tests 400 for invalid base64 token.
func TestValidate_InvalidTokenEncoding(t *testing.T) {
	key := generateTestKeyValidate(t)

	mockDBs := &mockDatabaseRepoValidate{}
	mockUsers := &mockUserRepoValidate{}
	mockTokens := &mockSSOTokenRepoValidate{}

	cfg := SSOPhpMyAdminValidateHandlerConfig{
		Databases: mockDBs,
		Users:     mockUsers,
		Tokens:    mockTokens,
		SSOKey:    &key,
		Log:       slog.Default(),
	}

	h := &ssoPhpMyAdminValidateHandler{cfg: cfg}

	body := ssoValidateRequest{Token: "!!!invalid-base64!!!"}
	bodyJSON, _ := json.Marshal(body)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/sso/phpmyadmin/validate", bytes.NewReader(bodyJSON))
	c.Request.Header.Set("Content-Type", "application/json")

	h.validate(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// Test helper mocks for validate endpoint

type mockDatabaseRepoValidate struct {
	databases map[string]*models.Database
}

func (m *mockDatabaseRepoValidate) Create(ctx context.Context, db *models.Database) error {
	return nil
}

func (m *mockDatabaseRepoValidate) FindByID(ctx context.Context, id string) (*models.Database, error) {
	db, ok := m.databases[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return db, nil
}

func (m *mockDatabaseRepoValidate) List(ctx context.Context, opts repository.ListOptions) ([]models.Database, int64, error) {
	return nil, 0, nil
}

func (m *mockDatabaseRepoValidate) ListByUserID(ctx context.Context, userID string, opts repository.ListOptions) ([]models.Database, int64, error) {
	return nil, 0, nil
}

func (m *mockDatabaseRepoValidate) CountByUserID(ctx context.Context, userID string) (int64, error) {
	return 0, nil
}

func (m *mockDatabaseRepoValidate) Delete(ctx context.Context, id string) error {
	return nil
}

func (m *mockDatabaseRepoValidate) ExistsByUserAndName(ctx context.Context, userID, name string) (bool, error) {
	return false, nil
}

type mockUserRepoValidate struct {
	users map[string]*models.User
}

func (m *mockUserRepoValidate) Create(ctx context.Context, u *models.User) error {
	return nil
}

func (m *mockUserRepoValidate) FindByID(ctx context.Context, id string) (*models.User, error) {
	u, ok := m.users[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return u, nil
}

func (m *mockUserRepoValidate) FindByEmail(ctx context.Context, email string) (*models.User, error) {
	return nil, nil
}

func (m *mockUserRepoValidate) FindByUsername(ctx context.Context, username string) (*models.User, error) {
	return nil, nil
}

func (m *mockUserRepoValidate) FindByKratosIdentityID(_ context.Context, _ string) (*models.User, error) {
	return nil, repository.ErrNotFound
}

func (m *mockUserRepoValidate) FindByIDs(_ context.Context, _ []string) ([]models.User, error) {
	return nil, nil
}

func (m *mockUserRepoValidate) List(ctx context.Context, opts repository.ListOptions) ([]models.User, int64, error) {
	return nil, 0, nil
}

func (m *mockUserRepoValidate) Update(ctx context.Context, u *models.User) error {
	return nil
}

func (m *mockUserRepoValidate) UpdateCLIPHPVersion(context.Context, string, *string) error {
	return nil
}

func (m *mockUserRepoValidate) LinkKratosIdentity(ctx context.Context, userID, kratosID string) error {
	return nil
}

func (m *mockUserRepoValidate) SetAdmin(ctx context.Context, id string, isAdmin bool) error {
	return nil
}

func (m *mockUserRepoValidate) CountAdmins(ctx context.Context) (int64, error) {
	return 0, nil
}

func (m *mockUserRepoValidate) FindAdminsByEmail(ctx context.Context) ([]*models.User, error) {
	return nil, nil
}

func (m *mockUserRepoValidate) SetSuspended(_ context.Context, _ string, _ bool, _ string) error {
	return nil
}

func (m *mockUserRepoValidate) SetSSHForwardingEnabled(_ context.Context, _ string, _ bool) error {
	return nil
}

func (m *mockUserRepoValidate) Delete(ctx context.Context, id string) error {
	return nil
}

func (m *mockUserRepoValidate) SetTOTPSecret(ctx context.Context, id string, encrypted []byte) error {
	return nil
}
func (m *mockUserRepoValidate) EnableTOTP(ctx context.Context, id string, now time.Time) error {
	return nil
}
func (m *mockUserRepoValidate) DisableTOTP(ctx context.Context, id string) error { return nil }

type mockSSOTokenRepoValidate struct {
	tokens       map[string]*models.PhpMyAdminSSOToken
	consumeError error
}

func (m *mockSSOTokenRepoValidate) Create(ctx context.Context, token *models.PhpMyAdminSSOToken) error {
	m.tokens[token.TokenHash] = token
	return nil
}

func (m *mockSSOTokenRepoValidate) ConsumeByHash(ctx context.Context, hash string) (*models.PhpMyAdminSSOToken, error) {
	if m.consumeError != nil {
		return nil, m.consumeError
	}

	token, ok := m.tokens[hash]
	if !ok {
		return nil, repository.ErrNotFound
	}

	// Simulate atomic delete
	delete(m.tokens, hash)

	return token, nil
}

func (m *mockSSOTokenRepoValidate) PurgeExpired(ctx context.Context) (int64, error) {
	return 0, nil
}

// jab8Setup builds a valid-token harness; callers mutate the user/db before
// running to exercise the JAB-8 validation-time authorization checks.
func jab8Setup(t *testing.T, user *models.User, db *models.Database) (*ssoPhpMyAdminValidateHandler, string) {
	t.Helper()
	key := generateTestKeyValidate(t)
	enc, err := key.Seal([]byte("secret123"))
	require.NoError(t, err)
	if user.MysqladminPasswordEnc == nil {
		user.MysqladminPasswordEnc = enc
	}
	tokenBytes := []byte("test-token-32-bytes-abcdefghijk")
	plaintextToken := base64.RawURLEncoding.EncodeToString(tokenBytes)
	hash := sha256.Sum256(tokenBytes)
	hashStr := fmt.Sprintf("%x", hash[:])
	cfg := SSOPhpMyAdminValidateHandlerConfig{
		Databases: &mockDatabaseRepoValidate{databases: map[string]*models.Database{db.ID: db}},
		Users:     &mockUserRepoValidate{users: map[string]*models.User{user.ID: user}},
		Tokens: &mockSSOTokenRepoValidate{tokens: map[string]*models.PhpMyAdminSSOToken{
			hashStr: {ID: "token1", UserID: "user1", DatabaseID: "db1", TokenHash: hashStr, ExpiresAt: time.Now().Add(5 * time.Minute)},
		}},
		SSOKey: &key,
		Log:    slog.Default(),
	}
	return &ssoPhpMyAdminValidateHandler{cfg: cfg}, plaintextToken
}

func jab8Run(t *testing.T, h *ssoPhpMyAdminValidateHandler, token string) int {
	t.Helper()
	bodyJSON, _ := json.Marshal(ssoValidateRequest{Token: token})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/sso/phpmyadmin/validate", bytes.NewReader(bodyJSON))
	c.Request.Header.Set("Content-Type", "application/json")
	h.validate(c)
	return w.Code
}

// JAB-8: a suspended user must not redeem an existing SSO token.
func TestValidate_SuspendedUserRefused(t *testing.T) {
	user := &models.User{ID: "user1", Username: ptrString("u"), MysqladminUsername: ptrString("u_my"), Suspended: true}
	db := &models.Database{ID: "db1", Name: "testdb", UserID: "user1"}
	h, tok := jab8Setup(t, user, db)
	assert.Equal(t, http.StatusUnauthorized, jab8Run(t, h, tok))
}

// JAB-8: a database no longer owned by the token user must be refused.
func TestValidate_OwnerMismatchRefused(t *testing.T) {
	user := &models.User{ID: "user1", Username: ptrString("u"), MysqladminUsername: ptrString("u_my")}
	db := &models.Database{ID: "db1", Name: "testdb", UserID: "someone-else"}
	h, tok := jab8Setup(t, user, db)
	assert.Equal(t, http.StatusUnauthorized, jab8Run(t, h, tok))
}

func (m *mockUserRepoValidate) UpdateUsername(context.Context, string, string) error { return nil }
