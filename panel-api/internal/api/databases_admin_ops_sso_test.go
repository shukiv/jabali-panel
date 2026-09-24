package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
)

// fakeAdminTokenMinter / fakeAdminerTokenMinter satisfy the privileged handler's
// dbconsoleops.PhpMyAdminMinter / AdminerMinter seams so the admin-all SSO handoff
// can be exercised without a real token store or database. They record what the
// door minted with, so the tests can assert the admin-all sentinel scope.
type fakeAdminTokenMinter struct {
	token   string
	gotDBID string
	gotName string
}

func (f *fakeAdminTokenMinter) MintToken(_ context.Context, _, databaseID, dbName string) (string, error) {
	f.gotDBID, f.gotName = databaseID, dbName
	return f.token, nil
}

type fakeAdminerTokenMinter struct {
	token     string
	gotDBID   string
	gotEngine string
}

func (f *fakeAdminerTokenMinter) MintAdminerToken(_ context.Context, _, databaseID, engine string) (string, error) {
	f.gotDBID, f.gotEngine = databaseID, engine
	return f.token, nil
}

// adminSSORequest builds a same-origin POST (httptest defaults Host to
// example.com, so an example.com Origin passes sameOriginStrict) carrying admin
// claims. origin overrides the Origin header for the cross-origin refusal test.
func adminSSORequest(origin string) (*httptest.ResponseRecorder, *gin.Context) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/api/v1/admin/databases/sso", nil)
	c.Request.Header.Set("Origin", origin)
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "admin1"})
	return w, c
}

// Privileged Adminer handoff is admin-all (ssoAdminAllSentinel, no single
// database) but must carry engine=postgres so the engine scope is encoded like
// the tenant Adminer door (JAB-348 AC3, #1787) — and must NOT carry a db.
func TestSSOAdminerAdmin_RedirectCarriesEngineNotDB(t *testing.T) {
	minter := &fakeAdminerTokenMinter{token: "ADM-ADMINER-TOK"}
	h := &databaseAdminOpsHandler{cfg: DatabaseAdminOpsHandlerConfig{
		DBAdmin:    &fakeDBAdmin{},
		AdminerSSO: minter,
		Log:        slog.Default(),
	}}

	w, c := adminSSORequest("https://example.com")
	h.ssoAdminerAdmin(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var resp ssoRedirectResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad response: %v (%s)", err, w.Body.String())
	}
	if !strings.Contains(resp.RedirectURL, "/jabali-adminer/?engine=postgres&token=ADM-ADMINER-TOK") {
		t.Errorf("redirect %q: want /jabali-adminer/?engine=postgres&token=ADM-ADMINER-TOK", resp.RedirectURL)
	}
	if strings.Contains(resp.RedirectURL, "db=") {
		t.Errorf("admin-all redirect must not carry a db: %q", resp.RedirectURL)
	}
	if minter.gotDBID != ssoAdminAllSentinel || minter.gotEngine != "postgres" {
		t.Errorf("minted dbID=%q engine=%q, want sentinel + postgres", minter.gotDBID, minter.gotEngine)
	}
}

// Privileged phpMyAdmin handoff is admin-all: token-only redirect, no db and no
// engine param.
func TestSSOPhpMyAdminAdmin_RedirectTokenOnly(t *testing.T) {
	minter := &fakeAdminTokenMinter{token: "ADM-PMA-TOK"}
	h := &databaseAdminOpsHandler{cfg: DatabaseAdminOpsHandlerConfig{
		Agent:   stubAgent{},
		DBAdmin: &fakeDBAdmin{},
		SSO:     minter,
		Log:     slog.Default(),
	}}

	w, c := adminSSORequest("https://example.com")
	h.ssoPhpMyAdminAdmin(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var resp ssoRedirectResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad response: %v (%s)", err, w.Body.String())
	}
	if !strings.Contains(resp.RedirectURL, "/phpmyadmin/sso.php?token=ADM-PMA-TOK") {
		t.Errorf("redirect %q: want /phpmyadmin/sso.php?token=ADM-PMA-TOK", resp.RedirectURL)
	}
	if strings.Contains(resp.RedirectURL, "db=") || strings.Contains(resp.RedirectURL, "engine=") {
		t.Errorf("admin-all phpMyAdmin redirect must be token-only: %q", resp.RedirectURL)
	}
	if minter.gotDBID != ssoAdminAllSentinel || minter.gotName != "" {
		t.Errorf("minted dbID=%q dbName=%q, want sentinel + empty name", minter.gotDBID, minter.gotName)
	}
}

// If the privileged phpMyAdmin shadow ensure fails, the door returns 502 before
// minting — the failure half of the audit-both-outcomes invariant for this door.
func TestSSOPhpMyAdminAdmin_AgentDownIsBadGateway(t *testing.T) {
	minter := &fakeAdminTokenMinter{token: "should-not-mint"}
	h := &databaseAdminOpsHandler{cfg: DatabaseAdminOpsHandlerConfig{
		Agent:   stubAgent{fail: true},
		DBAdmin: &fakeDBAdmin{},
		SSO:     minter,
		Log:     slog.Default(),
	}}

	w, c := adminSSORequest("https://example.com")
	h.ssoPhpMyAdminAdmin(c)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 when the shadow ensure fails", w.Code)
	}
	if minter.gotDBID != "" {
		t.Error("must not mint a token when the shadow ensure fails")
	}
}

// A cross-origin request is refused before any token is minted.
func TestSSOAdminerAdmin_CrossOriginRefused(t *testing.T) {
	minter := &fakeAdminerTokenMinter{token: "should-not-mint"}
	h := &databaseAdminOpsHandler{cfg: DatabaseAdminOpsHandlerConfig{
		DBAdmin:    &fakeDBAdmin{},
		AdminerSSO: minter,
		Log:        slog.Default(),
	}}

	w, c := adminSSORequest("https://attacker.com")
	h.ssoAdminerAdmin(c)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for a cross-origin admin SSO request", w.Code)
	}
	if minter.gotDBID != "" {
		t.Error("cross-origin request must be refused before minting a token")
	}
}
