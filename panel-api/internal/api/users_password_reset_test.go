package api_test

// GH #2058: Admin → Users → Reset password set the new password in Kratos
// only. The Linux account kept its old password, so SSH and SFTP refused the
// new one, and the stored hash stayed old, so the user couldn't then change
// the password from their profile (current_password is checked against it).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/kratosclient"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
)

// pwResetAgent records the agent calls the reset makes.
type pwResetAgent struct {
	mu    sync.Mutex
	calls []map[string]any
	cmds  []string
	err   error
}

func (a *pwResetAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cmds = append(a.cmds, cmd)
	p, _ := params.(map[string]any)
	a.calls = append(a.calls, p)
	if a.err != nil {
		return nil, a.err
	}
	return json.RawMessage(`{"ok":true}`), nil
}

// pwResetKratos is a fake Kratos admin API that accepts (or refuses) the
// password patch and counts it.
func pwResetKratos(t *testing.T, status int) (*httptest.Server, *int) {
	t.Helper()
	patches := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch && r.URL.Path == "/admin/identities/kid-1" {
			patches++
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"id":"kid-1"}`))
			return
		}
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv, &patches
}

func pwResetRouter(repo *memUserRepo, kratosURL string, ag *pwResetAgent, adminID string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1", func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: adminID, IsAdmin: true})
		c.Next()
	})
	api.RegisterUserRoutes(g, api.UserHandlerConfig{
		Repo:         repo,
		BcryptCost:   bcrypt.MinCost,
		KratosClient: kratosclient.NewClient(kratosURL, kratosURL),
		Agent:        ag,
	})
	return r
}

// pwResetTenant seeds an admin and a tenant with a Linux account and a Kratos
// identity.
func pwResetTenant(t *testing.T, repo *memUserRepo) (adminID string, tenantID string) {
	t.Helper()
	admin := makeUser(t, "admin@example.com", true, "adminpassword")
	repo.seed(admin)
	tenant := makeUser(t, "alice@example.com", false, "old-password-123")
	username, kid := "alice", "kid-1"
	tenant.Username = &username
	tenant.KratosIdentityID = &kid
	repo.seed(tenant)
	return admin.ID, tenant.ID
}

type pwResetResponse struct {
	Password string `json:"password"`
	OSSynced *bool  `json:"os_synced"`
}

func TestResetPassword_SetsTheLinuxPasswordAndTheStoredHash(t *testing.T) {
	t.Parallel()
	repo := newMemUserRepo()
	adminID, tenantID := pwResetTenant(t, repo)
	kratos, patches := pwResetKratos(t, http.StatusOK)
	ag := &pwResetAgent{}

	rec := doJSON(t, pwResetRouter(repo, kratos.URL, ag, adminID), http.MethodPost, "/api/v1/admin/users/"+tenantID+"/password/reset", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out pwResetResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.NotEmpty(t, out.Password)
	assert.Nil(t, out.OSSynced, "a reset that fully worked says nothing about os_synced")

	assert.Equal(t, 1, *patches, "Kratos gets the new password")
	require.Equal(t, []string{"user.password"}, ag.cmds, "the Linux account gets the new password")
	assert.Equal(t, "alice", ag.calls[0]["username"])
	assert.Equal(t, out.Password, ag.calls[0]["password"])

	stored, err := repo.FindByID(context.Background(), tenantID)
	require.NoError(t, err)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(stored.PasswordHash), []byte(out.Password)),
		"the stored hash matches the new password, so the user can change it from their profile")
}

func TestResetPassword_LinuxSyncFailureStillHandsOverThePanelPassword(t *testing.T) {
	t.Parallel()
	repo := newMemUserRepo()
	adminID, tenantID := pwResetTenant(t, repo)
	kratos, patches := pwResetKratos(t, http.StatusOK)
	ag := &pwResetAgent{err: errors.New("chpasswd: exit status 1 (/etc/shadow is locked)")}

	rec := doJSON(t, pwResetRouter(repo, kratos.URL, ag, adminID), http.MethodPost, "/api/v1/admin/users/"+tenantID+"/password/reset", nil)
	// Kratos already took the new password: withholding it would lock the
	// user out of the panel.
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out pwResetResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	assert.NotEmpty(t, out.Password)
	require.NotNil(t, out.OSSynced)
	assert.False(t, *out.OSSynced)
	assert.Equal(t, 1, *patches)
	assert.NotContains(t, rec.Body.String(), "/etc/shadow", "the agent's error text is never echoed")
}

func TestResetPassword_KratosFailureChangesNothing(t *testing.T) {
	t.Parallel()
	repo := newMemUserRepo()
	adminID, tenantID := pwResetTenant(t, repo)
	kratos, _ := pwResetKratos(t, http.StatusInternalServerError)
	ag := &pwResetAgent{}

	rec := doJSON(t, pwResetRouter(repo, kratos.URL, ag, adminID), http.MethodPost, "/api/v1/admin/users/"+tenantID+"/password/reset", nil)
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.NotContains(t, rec.Body.String(), `"password"`)
	assert.Empty(t, ag.cmds, "the Linux password is not touched when Kratos refused")
	stored, err := repo.FindByID(context.Background(), tenantID)
	require.NoError(t, err)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(stored.PasswordHash), []byte("old-password-123")))
}

func TestResetPassword_MissingKratosIdentityIs404(t *testing.T) {
	t.Parallel()
	repo := newMemUserRepo()
	adminID, tenantID := pwResetTenant(t, repo)
	kratos, _ := pwResetKratos(t, http.StatusNotFound)
	ag := &pwResetAgent{}

	rec := doJSON(t, pwResetRouter(repo, kratos.URL, ag, adminID), http.MethodPost, "/api/v1/admin/users/"+tenantID+"/password/reset", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "kratos identity not found")
	assert.Empty(t, ag.cmds)
}
