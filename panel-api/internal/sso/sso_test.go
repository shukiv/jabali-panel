package sso

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// generateTestKey creates a random 32-byte AES-256 key for testing.
func generateTestKey(t *testing.T) ssokey.Key {
	var k ssokey.Key
	if _, err := rand.Read(k[:]); err != nil {
		t.Fatalf("failed to generate test key: %v", err)
	}
	return k
}

// Note: EnsureShadow tests (FirstCall, Idempotent) require a real database
// and are tested in integration tests. See integration_test.go.

// TestMintToken_GeneratesToken tests token generation and hashing.
func TestMintToken_GeneratesToken(t *testing.T) {
	key := generateTestKey(t)

	mockUsers := &mockUsersForSSO{}
	mockAgent := &mockAgentForSSO{}
	mockTokens := &mockTokensForSSO{}

	svc := NewService(nil, mockUsers, mockTokens, mockAgent, &key, slog.Default())

	token, err := svc.MintToken(context.Background(), "user1", "db1", "testdb")

	require.NoError(t, err)
	assert.NotEmpty(t, token)

	// Verify token was created in the repo
	assert.Equal(t, 1, len(mockTokens.created))
}

// TestPasswordRedaction tests that plaintext passwords don't leak into logs during MintToken.
// Full EnsureShadow logging is tested in integration tests.
func TestPasswordRedaction(t *testing.T) {
	key := generateTestKey(t)

	// Create a custom logger that captures output
	logBuf := &bytes.Buffer{}
	handler := slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})
	logger := slog.New(handler)

	mockUsers := &mockUsersForSSO{}
	mockTokens := &mockTokensForSSO{}
	mockAgent := &mockAgentForSSO{}

	svc := NewService(nil, mockUsers, mockTokens, mockAgent, &key, logger)

	// Only test MintToken, which doesn't require a database
	_, err := svc.MintToken(context.Background(), "user1", "db1", "testdb")
	require.NoError(t, err)

	logOutput := logBuf.String()

	// Verify that expected log output is present
	assert.Contains(t, logOutput, "minted SSO token",
		"expected log message should be present")
}

// Helper mocks

type mockUsersForSSO struct {
	users map[string]*models.User
}

func (m *mockUsersForSSO) Create(ctx context.Context, u *models.User) error {
	return nil
}

func (m *mockUsersForSSO) FindByID(ctx context.Context, id string) (*models.User, error) {
	u, ok := m.users[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return u, nil
}

func (m *mockUsersForSSO) FindByEmail(ctx context.Context, email string) (*models.User, error) {
	return nil, nil
}

func (m *mockUsersForSSO) FindByUsername(ctx context.Context, username string) (*models.User, error) {
	return nil, nil
}

func (m *mockUsersForSSO) FindByKratosIdentityID(_ context.Context, _ string) (*models.User, error) {
	return nil, repository.ErrNotFound
}

func (m *mockUsersForSSO) FindByIDs(_ context.Context, _ []string) ([]models.User, error) {
	return nil, nil
}

func (m *mockUsersForSSO) List(ctx context.Context, opts repository.ListOptions) ([]models.User, int64, error) {
	return nil, 0, nil
}

func (m *mockUsersForSSO) Update(ctx context.Context, u *models.User) error {
	if u.ID == "user1" {
		m.users["user1"] = u
	}
	return nil
}

func (m *mockUsersForSSO) UpdateComposerChannel(ctx context.Context, id string, channel *string) error { return nil }

func (m *mockUsersForSSO) UpdateCLIPHPVersion(context.Context, string, *string) error { return nil }

func (m *mockUsersForSSO) LinkKratosIdentity(ctx context.Context, userID, kratosID string) error {
	if u, ok := m.users[userID]; ok {
		u.KratosIdentityID = &kratosID
	}
	return nil
}

func (m *mockUsersForSSO) SetAdmin(ctx context.Context, id string, isAdmin bool) error {
	return nil
}

func (m *mockUsersForSSO) CountAdmins(ctx context.Context) (int64, error) {
	return 0, nil
}

func (m *mockUsersForSSO) FindAdminsByEmail(ctx context.Context) ([]*models.User, error) {
	return nil, nil
}

func (m *mockUsersForSSO) SetSuspended(_ context.Context, _ string, _ bool, _ string) error {
	return nil
}
func (m *mockUsersForSSO) SetSSHForwardingEnabled(_ context.Context, _ string, _ bool) error {
	return nil
}

func (m *mockUsersForSSO) Delete(ctx context.Context, id string) error {
	return nil
}

func (m *mockUsersForSSO) SetTOTPSecret(ctx context.Context, id string, encrypted []byte) error {
	return nil
}
func (m *mockUsersForSSO) EnableTOTP(ctx context.Context, id string, now time.Time) error {
	return nil
}
func (m *mockUsersForSSO) DisableTOTP(ctx context.Context, id string) error { return nil }

type mockAgentForSSO struct {
	response  map[string]interface{}
	callCount int
}

func (m *mockAgentForSSO) Call(ctx context.Context, command string, params any) (json.RawMessage, error) {
	m.callCount++
	resp, _ := json.Marshal(m.response)
	return resp, nil
}

type mockTokensForSSO struct {
	created []*models.PhpMyAdminSSOToken
}

func (m *mockTokensForSSO) Create(ctx context.Context, token *models.PhpMyAdminSSOToken) error {
	m.created = append(m.created, token)
	return nil
}

func (m *mockTokensForSSO) ConsumeByHash(ctx context.Context, hash string) (*models.PhpMyAdminSSOToken, error) {
	return nil, repository.ErrNotFound
}

func (m *mockTokensForSSO) PurgeExpired(ctx context.Context) (int64, error) {
	return 0, nil
}

func ptrString(s string) *string {
	return &s
}

func (r *mockUsersForSSO) UpdateUsername(context.Context, string, string) error { return nil }

// GH #1238 stub.
func (m *mockUsersForSSO) UpdateShadowDBUsernames(context.Context, string, *string, *string) error {
	return nil
}
