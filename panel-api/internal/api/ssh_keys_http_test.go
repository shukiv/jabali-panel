package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// validSSHKey is a real ed25519 authorized-keys line (the handler actually
// parses + fingerprints it via internal/sshkeys).
const validSSHKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINhDTlDCUJiIIWOejraVqB0FPMRzhFUhtt7Ih0tnPAPs test@jabali"

// sshKeyHTTPRouter wires the ssh-keys routes with claims for userID. These are
// the first real HTTP-level tests of the handler (JAB-292); the pre-existing
// ssh_keys_test.go cases were stubs that never exercised the routes.
//
// The scheduler fires an async reconcile goroutine; these tests deliberately do
// not assert its callCount (that would race the goroutine's write). Schedule-
// once is proven synchronously in sshkeyops_test.go. If you ever assert the
// reconcile call here, add a WaitGroup/channel — don't read callCount directly.
func sshKeyHTTPRouter(repo *mockSSHKeyRepository, userID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: userID})
		c.Next()
	})
	RegisterSSHKeysRoutes(v1, SSHKeysHandlerConfig{
		SSHKeys:    repo,
		Reconciler: &mockSSHKeyReconciler{},
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return r
}

func sshRaw(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func TestSSHKeysHTTP_Create_HappyPath_201(t *testing.T) {
	repo := &mockSSHKeyRepository{keys: map[string]*models.SSHKey{}}
	r := sshKeyHTTPRouter(repo, "user1")

	w := sshRaw(r, http.MethodPost, "/api/v1/ssh-keys",
		`{"name":"laptop","public_key":"`+validSSHKey+`"}`)
	require.Equal(t, http.StatusCreated, w.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.NotEmpty(t, body["id"])
	require.Equal(t, "laptop", body["name"])
	require.NotEmpty(t, body["fingerprint"])
	require.Len(t, repo.keys, 1, "row persisted")
}

func TestSSHKeysHTTP_Create_InvalidKey_400(t *testing.T) {
	repo := &mockSSHKeyRepository{keys: map[string]*models.SSHKey{}}
	r := sshKeyHTTPRouter(repo, "user1")

	w := sshRaw(r, http.MethodPost, "/api/v1/ssh-keys", `{"name":"x","public_key":"not a key"}`)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "invalid_key")
	require.Empty(t, repo.keys)
}

func TestSSHKeysHTTP_Create_MissingPublicKey_400(t *testing.T) {
	repo := &mockSSHKeyRepository{keys: map[string]*models.SSHKey{}}
	r := sshKeyHTTPRouter(repo, "user1")

	w := sshRaw(r, http.MethodPost, "/api/v1/ssh-keys", `{"name":"x"}`)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "invalid_body")
}

func TestSSHKeysHTTP_Create_Duplicate_409(t *testing.T) {
	repo := &mockSSHKeyRepository{keys: map[string]*models.SSHKey{}}
	r := sshKeyHTTPRouter(repo, "user1")
	body := `{"name":"laptop","public_key":"` + validSSHKey + `"}`

	require.Equal(t, http.StatusCreated, sshRaw(r, http.MethodPost, "/api/v1/ssh-keys", body).Code)
	w := sshRaw(r, http.MethodPost, "/api/v1/ssh-keys", body)
	require.Equal(t, http.StatusConflict, w.Code)
	require.Contains(t, w.Body.String(), "duplicate_key")
}

func TestSSHKeysHTTP_Delete_Owned_204(t *testing.T) {
	repo := &mockSSHKeyRepository{keys: map[string]*models.SSHKey{
		"k1": {ID: "k1", UserID: "user1", Name: "laptop", Fingerprint: "fp"},
	}}
	r := sshKeyHTTPRouter(repo, "user1")

	w := sshRaw(r, http.MethodDelete, "/api/v1/ssh-keys/k1", "")
	require.Equal(t, http.StatusNoContent, w.Code)
	require.Empty(t, repo.keys, "key deleted")
}

func TestSSHKeysHTTP_Delete_NotFound_404(t *testing.T) {
	repo := &mockSSHKeyRepository{keys: map[string]*models.SSHKey{}}
	r := sshKeyHTTPRouter(repo, "user1")

	w := sshRaw(r, http.MethodDelete, "/api/v1/ssh-keys/nope", "")
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Contains(t, w.Body.String(), "key_not_found")
}

// Owner scoping: user1 must not be able to delete user2's key — 404, and the
// row survives (ErrNotFound collapses missing + not-owned).
func TestSSHKeysHTTP_Delete_OtherUsersKey_404(t *testing.T) {
	repo := &mockSSHKeyRepository{keys: map[string]*models.SSHKey{
		"k2": {ID: "k2", UserID: "user2", Name: "theirs", Fingerprint: "fp2"},
	}}
	r := sshKeyHTTPRouter(repo, "user1")

	w := sshRaw(r, http.MethodDelete, "/api/v1/ssh-keys/k2", "")
	require.Equal(t, http.StatusNotFound, w.Code)
	require.Contains(t, w.Body.String(), "key_not_found")
	require.Len(t, repo.keys, 1, "another user's key must survive")
}
