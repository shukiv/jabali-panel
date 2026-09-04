package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// This file characterizes the exact optional-module agent wire the REST PATCH
// dispatches (postgres / docker marketplace / tenant docker / python and the
// dns/mail/quota/security/ftp system.module.* loop). Every assertion was written
// and run GREEN against the pre-refactor handler to lock the wire, so the
// JAB-294 settingsops extraction is proven byte-identical: same verb, params,
// and — critically for JAB-259 — ftp disable stays the fail-closed ftp.disable
// verb, never the generic system.module.disable.

// moduleCallParams waits for command (dispatch is an async goroutine) and
// returns its decoded params. Fails if none arrives.
func moduleCallParams(t *testing.T, mock *agent.MockClient, command string) map[string]any {
	t.Helper()
	params := map[string]any{}
	require.Eventually(t, func() bool {
		for _, c := range mock.Calls() {
			if c.Command == command {
				if len(c.Params) > 0 {
					require.NoError(t, json.Unmarshal(c.Params, &params))
				}
				return true
			}
		}
		return false
	}, 2*time.Second, 5*time.Millisecond, command+" was never dispatched")
	return params
}

// requireNoCommand asserts command is never dispatched within a short window.
func requireNoCommand(t *testing.T, mock *agent.MockClient, command string) {
	t.Helper()
	require.Never(t, func() bool {
		return nginxCommandDispatched(mock, command)
	}, 250*time.Millisecond, 10*time.Millisecond, command+" should not have been dispatched")
}

// patchSettings runs a PATCH against a handler seeded with existing, registering
// canned OK responses for every command name in expectVerbs so the mock does not
// log unknown-command errors, and returns the mock for wire assertions.
func patchSettings(t *testing.T, existing *models.ServerSettings, body map[string]any, expectVerbs ...string) *agent.MockClient {
	t.Helper()
	mockRepo := &mockServerSettingsRepo{getResult: existing}
	mockAgent := agent.NewMockClient()
	for _, v := range expectVerbs {
		mockAgent.On(v, map[string]any{"status": "ok"})
	}
	r := settingsRouter(true, mockRepo, mockAgent)

	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "PATCH should succeed; body=%s", rec.Body.String())
	return mockAgent
}

func TestServerSettingsPatch_Postgres_Wire(t *testing.T) {
	t.Run("enable", func(t *testing.T) {
		m := patchSettings(t, &models.ServerSettings{ID: 1, SSHPort: 22, PostgresEnabled: false},
			map[string]any{"postgres_enabled": true}, "db.postgres.install")
		require.Equal(t, map[string]any{}, moduleCallParams(t, m, "db.postgres.install"))
		requireNoCommand(t, m, "db.postgres.disable")
	})
	t.Run("disable", func(t *testing.T) {
		m := patchSettings(t, &models.ServerSettings{ID: 1, SSHPort: 22, PostgresEnabled: true},
			map[string]any{"postgres_enabled": false}, "db.postgres.disable")
		require.Equal(t, map[string]any{}, moduleCallParams(t, m, "db.postgres.disable"))
		requireNoCommand(t, m, "db.postgres.install")
	})
	t.Run("noop unrelated patch", func(t *testing.T) {
		m := patchSettings(t, &models.ServerSettings{ID: 1, SSHPort: 22, PostgresEnabled: true},
			map[string]any{"postgres_enabled": true}, "db.postgres.install")
		requireNoCommand(t, m, "db.postgres.install")
		requireNoCommand(t, m, "db.postgres.disable")
	})
}

func TestServerSettingsPatch_ModuleLoop_Wire(t *testing.T) {
	for _, key := range []string{"dns", "mail", "quota", "security"} {
		key := key
		t.Run(key+" enable", func(t *testing.T) {
			existing := &models.ServerSettings{ID: 1, SSHPort: 22}
			m := patchSettings(t, existing, map[string]any{key + "_enabled": true}, "system.module.install")
			require.Equal(t, map[string]any{"key": key}, moduleCallParams(t, m, "system.module.install"))
		})
		t.Run(key+" disable", func(t *testing.T) {
			existing := &models.ServerSettings{ID: 1, SSHPort: 22}
			setModuleFlag(existing, key, true)
			m := patchSettings(t, existing, map[string]any{key + "_enabled": false}, "system.module.disable")
			require.Equal(t, map[string]any{"key": key}, moduleCallParams(t, m, "system.module.disable"))
		})
	}
}

// TestServerSettingsPatch_FTP_FailClosed is the assertion that must never
// regress: turning FTP OFF fires the fail-closed ftp.disable verb (JAB-259),
// NOT the generic system.module.disable.
func TestServerSettingsPatch_FTP_FailClosed(t *testing.T) {
	t.Run("enable installs", func(t *testing.T) {
		m := patchSettings(t, &models.ServerSettings{ID: 1, SSHPort: 22, FTPEnabled: false},
			map[string]any{"ftp_enabled": true}, "system.module.install")
		require.Equal(t, map[string]any{"key": "ftp"}, moduleCallParams(t, m, "system.module.install"))
		requireNoCommand(t, m, "ftp.disable")
	})
	t.Run("disable is fail-closed ftp.disable, not module.disable", func(t *testing.T) {
		m := patchSettings(t, &models.ServerSettings{ID: 1, SSHPort: 22, FTPEnabled: true},
			map[string]any{"ftp_enabled": false}, "ftp.disable")
		require.Equal(t, map[string]any{}, moduleCallParams(t, m, "ftp.disable"))
		requireNoCommand(t, m, "system.module.disable")
	})
	t.Run("config change while enabled re-renders via install", func(t *testing.T) {
		existing := &models.ServerSettings{ID: 1, SSHPort: 22, FTPEnabled: true, FTPAllowPlaintext: false}
		m := patchSettings(t, existing, map[string]any{"ftp_allow_plaintext": true}, "system.module.install")
		require.Equal(t, map[string]any{"key": "ftp"}, moduleCallParams(t, m, "system.module.install"))
		requireNoCommand(t, m, "ftp.disable")
	})
	t.Run("enable does not also fire a reapply", func(t *testing.T) {
		// false->true with a config field also set: install once, no extra reapply.
		existing := &models.ServerSettings{ID: 1, SSHPort: 22, FTPEnabled: false, FTPAllowPlaintext: false}
		m := patchSettings(t, existing,
			map[string]any{"ftp_enabled": true, "ftp_allow_plaintext": true}, "system.module.install")
		require.Equal(t, map[string]any{"key": "ftp"}, moduleCallParams(t, m, "system.module.install"))
		// exactly one install call
		var n int
		for _, c := range m.Calls() {
			if c.Command == "system.module.install" {
				n++
			}
		}
		require.Equal(t, 1, n, "enable+config-set must install once, not install+reapply")
	})
}

func TestServerSettingsPatch_DockerMarketplace_Wire(t *testing.T) {
	t.Run("enable", func(t *testing.T) {
		m := patchSettings(t, &models.ServerSettings{ID: 1, SSHPort: 22, DockerMarketplaceEnabled: false},
			map[string]any{"docker_marketplace_enabled": true}, "docker.install")
		require.Equal(t, map[string]any{}, moduleCallParams(t, m, "docker.install"))
		requireNoCommand(t, m, "docker.disable")
	})
	t.Run("disable", func(t *testing.T) {
		m := patchSettings(t, &models.ServerSettings{ID: 1, SSHPort: 22, DockerMarketplaceEnabled: true},
			map[string]any{"docker_marketplace_enabled": false}, "docker.disable")
		require.Equal(t, map[string]any{}, moduleCallParams(t, m, "docker.disable"))
	})
}

func TestServerSettingsPatch_DockerTenant_Wire(t *testing.T) {
	t.Run("enable carries enabled=true", func(t *testing.T) {
		existing := &models.ServerSettings{ID: 1, SSHPort: 22, DockerMarketplaceEnabled: true, DockerAppsForUsersEnabled: false}
		m := patchSettings(t, existing, map[string]any{"docker_apps_for_users_enabled": true}, "docker.tenant_set")
		require.Equal(t, map[string]any{"enabled": true}, moduleCallParams(t, m, "docker.tenant_set"))
	})
	t.Run("disable carries enabled=false", func(t *testing.T) {
		existing := &models.ServerSettings{ID: 1, SSHPort: 22, DockerMarketplaceEnabled: true, DockerAppsForUsersEnabled: true}
		m := patchSettings(t, existing, map[string]any{"docker_apps_for_users_enabled": false}, "docker.tenant_set")
		require.Equal(t, map[string]any{"enabled": false}, moduleCallParams(t, m, "docker.tenant_set"))
	})
}

func TestServerSettingsPatch_Python_Wire(t *testing.T) {
	t.Run("enable installs runtime", func(t *testing.T) {
		m := patchSettings(t, &models.ServerSettings{ID: 1, SSHPort: 22, PythonAppsEnabled: false},
			map[string]any{"python_apps_enabled": true}, "app.python.install_runtime")
		require.Equal(t, map[string]any{}, moduleCallParams(t, m, "app.python.install_runtime"))
	})
	t.Run("disable installs nothing", func(t *testing.T) {
		m := patchSettings(t, &models.ServerSettings{ID: 1, SSHPort: 22, PythonAppsEnabled: true},
			map[string]any{"python_apps_enabled": false})
		requireNoCommand(t, m, "app.python.install_runtime")
	})
}

// setModuleFlag flips one of the generic-loop module flags on s.
func setModuleFlag(s *models.ServerSettings, key string, v bool) {
	switch key {
	case "dns":
		s.DNSEnabled = v
	case "mail":
		s.MailEnabled = v
	case "quota":
		s.QuotaEnabled = v
	case "security":
		s.SecurityEnabled = v
	case "ftp":
		s.FTPEnabled = v
	}
}
