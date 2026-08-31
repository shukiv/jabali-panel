package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// Reuses fakeSettingsRepo from dns_test.go (its Upsert stores into .s).
func newSSHIgnoreRouter(t *testing.T, repo *fakeSettingsRepo) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "admin", IsAdmin: true})
		c.Next()
	})
	RegisterSSHLoginIgnoreRoutes(v1, SSHLoginIgnoreHandlerConfig{Settings: repo})
	return r
}

func TestSSHLoginIgnore_Get(t *testing.T) {
	repo := &fakeSettingsRepo{s: &models.ServerSettings{SSHLoginIgnoreAccounts: "drfeed\nbackup"}}
	r := newSSHIgnoreRouter(t, repo)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/admin/settings/ssh-login-ignore", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Accounts []string `json:"accounts"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Accounts) != 2 || resp.Accounts[0] != "backup" || resp.Accounts[1] != "drfeed" {
		t.Fatalf("accounts = %v, want [backup drfeed]", resp.Accounts)
	}
}

func TestSSHLoginIgnore_PutPersistsNormalised(t *testing.T) {
	repo := &fakeSettingsRepo{s: &models.ServerSettings{}}
	r := newSSHIgnoreRouter(t, repo)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/v1/admin/settings/ssh-login-ignore",
		strings.NewReader(`{"accounts":["zeta","alpha","alpha"]}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("code = %d; body=%s", w.Code, w.Body.String())
	}
	// stored form is normalised (dedup + sort + newline-joined)
	if repo.s.SSHLoginIgnoreAccounts != "alpha\nzeta" {
		t.Fatalf("stored = %q, want %q", repo.s.SSHLoginIgnoreAccounts, "alpha\nzeta")
	}
}

func TestSSHLoginIgnore_PutRejectsBadUsername(t *testing.T) {
	repo := &fakeSettingsRepo{s: &models.ServerSettings{}}
	r := newSSHIgnoreRouter(t, repo)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/v1/admin/settings/ssh-login-ignore",
		strings.NewReader(`{"accounts":["ok","bad name,with-comma"]}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code = %d, want 422; body=%s", w.Code, w.Body.String())
	}
	// nothing persisted → the seeded empty value is unchanged
	if repo.s.SSHLoginIgnoreAccounts != "" {
		t.Fatalf("must not persist on invalid input, got %q", repo.s.SSHLoginIgnoreAccounts)
	}
}
