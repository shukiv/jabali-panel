package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/sso"
)

// newAdminerHandlerForTest builds a tenant Adminer handler over mock repos. The
// AdminerService gets a nil token store because the refusal paths under test
// return before any token is minted. base's DB is nil for the same reason.
func newAdminerHandlerForTest(t *testing.T, dbs *mockDatabaseRepo) *ssoAdminerHandler {
	t.Helper()
	key := generateTestKeySSOPhpMyAdmin(t)
	base := sso.NewService(nil, &mockUserRepo{}, &mockSSOTokenRepo{}, &mockAgent{}, &key, slog.Default())
	adminer := sso.NewAdminerService(base, nil)
	return &ssoAdminerHandler{cfg: SSOAdminerHandlerConfig{
		Databases: dbs,
		SSO:       base,
		Adminer:   adminer,
		Log:       slog.Default(),
	}}
}

func adminerRequest(t *testing.T, dbID, origin string, withClaims bool) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	body, _ := json.Marshal(ssoAdminerRequest{DatabaseID: dbID})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/api/v1/sso/adminer", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Origin", origin) // httptest Host defaults to example.com
	if withClaims {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "user1"})
	}
	return w, c
}

// Ownership check: a database owned by another tenant is refused with 403.
func TestSSOAdminer_NotAuthorized(t *testing.T) {
	dbs := &mockDatabaseRepo{databases: []models.Database{
		{ID: "db1", Name: "otherdb", UserID: "user2", Engine: "postgres"},
	}}
	h := newAdminerHandlerForTest(t, dbs)

	w, c := adminerRequest(t, "db1", "http://example.com", true)
	h.issueSSOToken(c)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

// CSRF: a cross-origin request is refused with 403 before any DB lookup.
func TestSSOAdminer_CrossOrigin(t *testing.T) {
	dbs := &mockDatabaseRepo{databases: []models.Database{
		{ID: "db1", Name: "mydb", UserID: "user1", Engine: "postgres"},
	}}
	h := newAdminerHandlerForTest(t, dbs)

	w, c := adminerRequest(t, "db1", "https://attacker.com", true)
	h.issueSSOToken(c)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

// Missing JWT: no session is refused with 403.
func TestSSOAdminer_NoAuth(t *testing.T) {
	h := newAdminerHandlerForTest(t, &mockDatabaseRepo{})

	w, c := adminerRequest(t, "db1", "http://example.com", false)
	h.issueSSOToken(c)

	assert.Equal(t, http.StatusForbidden, w.Code)
}
