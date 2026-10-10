package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #2056: mail's install needs the DNS module (install.sh _dies without the
// pdns self-zone). Turning mail on while DNS is off, or while DNS isn't
// installed and running yet, started an install that was sure to fail, and the
// Modules card never said why. The PATCH and the Retry route now refuse it
// with a reason instead.

// errMustNotPersist makes any save fail with a 500, so a refused PATCH proves
// it returned before saving by answering 400/409 instead.
var errMustNotPersist = errors.New("this PATCH must not be saved")

func patchSettingsRaw(t *testing.T, existing *models.ServerSettings, mockAgent *agent.MockClient, body map[string]any, upsertErr ...error) (*httptest.ResponseRecorder, *mockServerSettingsRepo) {
	t.Helper()
	repo := &mockServerSettingsRepo{getResult: existing}
	if len(upsertErr) > 0 {
		repo.upsertErr = upsertErr[0]
	}
	r := settingsRouter(true, repo, mockAgent)
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec, repo
}

func dnsStatusAgent(installed, active bool) *agent.MockClient {
	m := agent.NewMockClient()
	m.On("system.module.status", map[string]any{"key": "dns", "installed": installed, "active": active})
	m.On("system.module.install", map[string]any{"installed": true, "active": true})
	return m
}

func settingsErrorBody(t *testing.T, rec *httptest.ResponseRecorder) (string, string) {
	t.Helper()
	var body struct {
		Error  string `json:"error"`
		Detail string `json:"detail"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	return body.Error, body.Detail
}

func TestServerSettingsPatch_MailNeedsDNS(t *testing.T) {
	t.Run("DNS off: refused, nothing saved or installed", func(t *testing.T) {
		m := dnsStatusAgent(false, false)
		existing := &models.ServerSettings{ID: 1, SSHPort: 22}
		rec, _ := patchSettingsRaw(t, existing, m, map[string]any{"mail_enabled": true}, errMustNotPersist)
		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		code, detail := settingsErrorBody(t, rec)
		assert.Equal(t, "mail_requires_dns", code)
		assert.Contains(t, detail, "DNS")
		requireNoCommand(t, m, "system.module.install")
	})

	t.Run("DNS on but not running yet: refused", func(t *testing.T) {
		m := dnsStatusAgent(true, false)
		existing := &models.ServerSettings{ID: 1, SSHPort: 22, DNSEnabled: true}
		rec, _ := patchSettingsRaw(t, existing, m, map[string]any{"mail_enabled": true}, errMustNotPersist)
		require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		code, detail := settingsErrorBody(t, rec)
		assert.Equal(t, "dns_not_ready", code)
		assert.Contains(t, detail, "DNS")
		requireNoCommand(t, m, "system.module.install")
	})

	t.Run("DNS and mail turned on together, DNS not installed: refused", func(t *testing.T) {
		m := dnsStatusAgent(false, false)
		existing := &models.ServerSettings{ID: 1, SSHPort: 22}
		rec, _ := patchSettingsRaw(t, existing, m, map[string]any{"dns_enabled": true, "mail_enabled": true}, errMustNotPersist)
		require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		requireNoCommand(t, m, "system.module.install")
	})

	t.Run("DNS running: mail turns on and installs", func(t *testing.T) {
		m := dnsStatusAgent(true, true)
		existing := &models.ServerSettings{ID: 1, SSHPort: 22, DNSEnabled: true}
		rec, repo := patchSettingsRaw(t, existing, m, map[string]any{"mail_enabled": true})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.True(t, repo.getResult.MailEnabled)
		require.Equal(t, map[string]any{"key": "mail"}, moduleCallParams(t, m, "system.module.install"))
	})

	t.Run("agent can't say: not blocked (the reconciler still waits for DNS)", func(t *testing.T) {
		m := agent.NewMockClient()
		m.OnError("system.module.status", errors.New("agent down"))
		m.On("system.module.install", map[string]any{"installed": true, "active": true})
		existing := &models.ServerSettings{ID: 1, SSHPort: 22, DNSEnabled: true}
		rec, _ := patchSettingsRaw(t, existing, m, map[string]any{"mail_enabled": true})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})

	t.Run("mail already on, DNS off: other settings still save", func(t *testing.T) {
		m := dnsStatusAgent(false, false)
		existing := &models.ServerSettings{ID: 1, SSHPort: 22, MailEnabled: true}
		rec, _ := patchSettingsRaw(t, existing, m, map[string]any{"panel_brand_text": "Acme"})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})

	t.Run("turning mail off is never blocked", func(t *testing.T) {
		m := dnsStatusAgent(false, false)
		m.On("system.module.disable", map[string]any{"installed": true, "active": false})
		existing := &models.ServerSettings{ID: 1, SSHPort: 22, MailEnabled: true}
		rec, _ := patchSettingsRaw(t, existing, m, map[string]any{"mail_enabled": false})
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})
}

// The Modules card's Retry for mail gets the same check.
func TestModuleInstallEndpoint_MailWaitsForDNS(t *testing.T) {
	post := func(m *agent.MockClient) *httptest.ResponseRecorder {
		r := settingsRouter(true, &mockServerSettingsRepo{}, m)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/admin/settings/modules/install",
			strings.NewReader(`{"key":"mail"}`)))
		return rec
	}

	down := dnsStatusAgent(true, false)
	rec := post(down)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	code, _ := settingsErrorBody(t, rec)
	assert.Equal(t, "dns_not_ready", code)
	requireNoCommand(t, down, "system.module.install")

	up := dnsStatusAgent(true, true)
	require.Equal(t, http.StatusAccepted, post(up).Code)
	require.Eventually(t, func() bool { return nginxCommandDispatched(up, "system.module.install") },
		2*time.Second, 5*time.Millisecond)
}

// The status route passes the agent's install state and last failure through.
func TestModuleStatusEndpoint_InstallErrorPassesThrough(t *testing.T) {
	ag := &moduleAgentStub{status: map[string]map[string]any{
		"mail": {
			"installed": false, "active": false, "installing": false,
			"last_error":    "the DNS module must be installed first. Enable DNS, then mail.",
			"last_error_at": "2026-10-09T10:00:00Z",
			"install_log":   "/var/log/jabali/install-2026-10-09_10-00-00.log",
		},
		"dns": {"installed": false, "active": false, "installing": true},
	}}
	r := settingsRouter(true, &mockServerSettingsRepo{}, ag)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings/modules/status", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Modules map[string]moduleStatusEntry `json:"modules"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "the DNS module must be installed first. Enable DNS, then mail.", body.Modules["mail"].LastError)
	assert.Equal(t, "2026-10-09T10:00:00Z", body.Modules["mail"].LastErrorAt)
	assert.Equal(t, "/var/log/jabali/install-2026-10-09_10-00-00.log", body.Modules["mail"].InstallLog)
	assert.True(t, body.Modules["dns"].Installing)
	assert.False(t, body.Modules["mail"].Installing)
}
