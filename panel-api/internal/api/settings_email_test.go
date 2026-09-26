package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// settingsEmailRouter builds a minimal router with admin claims injected
// and the settings/email routes mounted against a shared mockDomainRepo.
// settings backs the server_settings read; &fakeSettingsRepo{} has no row
// (ErrNotFound), which means no applied mail hostname.
func settingsEmailRouter(t *testing.T, repo *mockDomainRepo, settings repository.ServerSettingsRepository) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "admin1", IsAdmin: true})
		c.Next()
	})
	RegisterSettingsEmailRoutes(v1, SettingsEmailHandlerConfig{
		Domains:        repo,
		ServerSettings: settings,
		Log:            slog.Default(),
	})
	return r
}

// panelPrimaryRepo returns a domain repo holding only the panel-primary
// row named name.
func panelPrimaryRepo(name string) *mockDomainRepo {
	repo := newMockDomainRepo()
	repo.domains["dom_panel"] = &models.Domain{
		ID:             "dom_panel",
		Name:           name,
		IsPanelPrimary: true,
		EmailEnabled:   true,
	}
	return repo
}

func getSettingsEmail(t *testing.T, r *gin.Engine) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings/email", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// TestSettingsEmail_ReturnsPrimaryDomain: panel-primary row exists with DKIM
// converged — 200 with the full shape.
func TestSettingsEmail_ReturnsPrimaryDomain(t *testing.T) {
	t.Parallel()

	repo := newMockDomainRepo()
	now := time.Date(2026, 4, 22, 18, 0, 0, 0, time.UTC)
	pk := "p=MIIBIjANBg... (trimmed)"
	repo.domains["dom_panel"] = &models.Domain{
		ID:             "dom_panel",
		Name:           "jabali-panel.local",
		IsPanelPrimary: true,
		EmailEnabled:   true,
		DkimPublicKey:  &pk,
		EmailEnabledAt: &now,
	}

	r := settingsEmailRouter(t, repo, &fakeSettingsRepo{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings/email", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body settingsEmailOK
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "jabali-panel.local", body.PrimaryDomainName)
	assert.Equal(t, "https://mail.jabali-panel.local/", body.WebmailURL)
	assert.True(t, body.DKIMPublished)
	require.NotNil(t, body.EmailEnabledAt)
	assert.Equal(t, now.Unix(), body.EmailEnabledAt.Unix())
	assert.Equal(t, "mail.jabali-panel.local", body.MailHostname.Effective)
	assert.Nil(t, body.MailHostname.Applied, "no applied mail hostname: the derived default is in effect")
}

// TestSettingsEmail_AppliedMailHostnameDrivesWebmailURL (JAB-390): when the
// reconciler has applied a custom mail hostname, the webmail URL and the
// reported effective hostname follow it (normalized), not mail.<primary>.
func TestSettingsEmail_AppliedMailHostnameDrivesWebmailURL(t *testing.T) {
	t.Parallel()

	applied := "  MX.Example.NET "
	settings := &fakeSettingsRepo{s: &models.ServerSettings{Hostname: "jabali-panel.local", MailHostname: &applied}}
	rec := getSettingsEmail(t, settingsEmailRouter(t, panelPrimaryRepo("jabali-panel.local"), settings))

	require.Equal(t, http.StatusOK, rec.Code)
	var body settingsEmailOK
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "https://mx.example.net/", body.WebmailURL)
	assert.Equal(t, "mx.example.net", body.MailHostname.Effective)
	require.NotNil(t, body.MailHostname.Applied)
	assert.Equal(t, "mx.example.net", *body.MailHostname.Applied)
}

// TestSettingsEmail_InvalidStoredMailHostnameIsNotApplied (JAB-390): a stored
// value that fails validation is not in effect. The response reports the
// derived default and no applied value, so a corrupt row never surfaces as a
// webmail link.
func TestSettingsEmail_InvalidStoredMailHostnameIsNotApplied(t *testing.T) {
	t.Parallel()

	bogus := "https://evil.example/login"
	settings := &fakeSettingsRepo{s: &models.ServerSettings{Hostname: "jabali-panel.local", MailHostname: &bogus}}
	rec := getSettingsEmail(t, settingsEmailRouter(t, panelPrimaryRepo("jabali-panel.local"), settings))

	require.Equal(t, http.StatusOK, rec.Code)
	var body settingsEmailOK
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "https://mail.jabali-panel.local/", body.WebmailURL)
	assert.Equal(t, "mail.jabali-panel.local", body.MailHostname.Effective)
	assert.Nil(t, body.MailHostname.Applied)
}

// TestSettingsEmail_SettingsReadErrorIs500 (JAB-390): a failed server_settings
// read must not be guessed around. Reporting the derived default could show a
// webmail link for a host the panel no longer uses.
func TestSettingsEmail_SettingsReadErrorIs500(t *testing.T) {
	t.Parallel()

	rec := getSettingsEmail(t, settingsEmailRouter(t, panelPrimaryRepo("jabali-panel.local"), errSettingsRepo{}))

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "webmail_url")
}

// TestSettingsEmail_DKIMNotPublished: row exists but DKIM not yet converged
// (nil public key). 200 with dkim_published=false. Clients show "Initializing"
// badge even though the row is present.
func TestSettingsEmail_DKIMNotPublished(t *testing.T) {
	t.Parallel()

	repo := newMockDomainRepo()
	repo.domains["dom_panel"] = &models.Domain{
		ID:             "dom_panel",
		Name:           "jabali-panel.local",
		IsPanelPrimary: true,
		EmailEnabled:   true,
		DkimPublicKey:  nil,
	}

	r := settingsEmailRouter(t, repo, &fakeSettingsRepo{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings/email", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body settingsEmailOK
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.False(t, body.DKIMPublished)
}

// TestSettingsEmail_Absent_Returns202: no panel-primary row → 202 with
// minimal "initializing" shape. Critical wire-contract test.
func TestSettingsEmail_Absent_Returns202(t *testing.T) {
	t.Parallel()

	repo := newMockDomainRepo()

	r := settingsEmailRouter(t, repo, &fakeSettingsRepo{})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings/email", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusAccepted, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Nil(t, body["primary_domain_name"])
	assert.Equal(t, "initializing", body["status"])
	// The 202 body must NOT contain the 200 fields — clients switch on
	// status code and our struct choice enforces this at encode time.
	_, hasURL := body["webmail_url"]
	_, hasDKIM := body["dkim_published"]
	_, hasTS := body["email_enabled_at"]
	_, hasMailHost := body["mail_hostname"]
	assert.False(t, hasMailHost, "mail_hostname must not appear in 202 body")
	assert.False(t, hasURL, "webmail_url must not appear in 202 body")
	assert.False(t, hasDKIM, "dkim_published must not appear in 202 body")
	assert.False(t, hasTS, "email_enabled_at must not appear in 202 body")
}
