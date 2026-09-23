package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dbconsoleops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/sso"
)

// TestSSOPhpMyAdmin_ShadowEnsureFail_UsesCanonicalTaxonomy locks JAB-348 AC5:
// the phpMyAdmin issuance door maps an issuance-side failure to the canonical
// dbconsoleops audit-outcome taxonomy (ensure_shadow_fail / mint_fail) — the
// exact strings the Adminer (sso_adminer.go) and CLI (db_sso_cmd.go) doors
// already emit — rather than a bare "unauthorized".
//
// "unauthorized" (and its "unauthorized:<reason>" variants) stays reserved for
// the pre-issuance authorization gates (no session, cross-origin, bad JSON,
// db-not-found, owner-mismatch). Once ownership and same-origin pass, a
// shadow-provision or mint failure is an issuance error, not an authorization
// denial, so it must carry an issuance-outcome string an operator can grep for
// alongside the Adminer/CLI doors.
//
// This exercises the shadow-ensure failure branch, which is reachable without a
// live database: EnsureShadow returns "user has no username" before it opens its
// FOR UPDATE transaction, so a user row with a nil Username drives the branch
// deterministically. Before the fix this branch audited "unauthorized"; the test
// asserts the canonical constant, so it is RED on the unfixed handler.
func TestSSOPhpMyAdmin_ShadowEnsureFail_UsesCanonicalTaxonomy(t *testing.T) {
	key := generateTestKeySSOPhpMyAdmin(t)

	mockDBs := &mockDatabaseRepo{
		databases: []models.Database{
			{ID: "db1", Name: "testdb", UserID: "user1"},
		},
	}
	// Ownership passes (db1.user_id == claims.sub), but the user has no panel
	// username, so EnsureShadow fails immediately — before any DB access.
	mockUsers := &mockUserRepo{
		users: map[string]*models.User{
			"user1": {ID: "user1", Username: nil},
		},
	}
	mockAgent := &mockAgent{}
	mockTokens := &mockSSOTokenRepo{}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ssoService := sso.NewService(nil, mockUsers, mockTokens, mockAgent, &key, logger)

	cfg := SSOPhpMyAdminHandlerConfig{
		Databases: mockDBs,
		SSO:       ssoService,
		Log:       logger,
	}
	h := &ssoPhpMyAdminHandler{cfg: cfg}

	body, _ := json.Marshal(ssoPhpMyAdminRequest{DatabaseID: "db1"})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/api/v1/sso/phpmyadmin", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Origin", "http://example.com")
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "user1"})

	h.issueSSOToken(c)

	require.Equal(t, http.StatusInternalServerError, w.Code,
		"a shadow-ensure failure returns 500 internal")

	logs := logBuf.String()
	require.Contains(t, logs, dbconsoleops.OutcomeEnsureShadowFail,
		"a shadow-ensure failure must audit the canonical ensure_shadow_fail outcome, matching the Adminer and CLI doors")
	require.NotContains(t, logs, `"outcome":"unauthorized"`,
		"a post-authorization issuance failure must not be miscategorised as an authorization denial")
}
