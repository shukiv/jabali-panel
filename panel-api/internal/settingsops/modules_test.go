package settingsops

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// findModule returns the ModuleEffect for key from a plan's generic loop.
func findModule(t *testing.T, p ModulePlan, key string) ModuleEffect {
	t.Helper()
	for _, m := range p.Modules {
		if m.Key == key {
			return m
		}
	}
	t.Fatalf("module %q not in plan", key)
	return ModuleEffect{}
}

func TestModuleEffects_Postgres(t *testing.T) {
	enable := ModuleEffects(&models.ServerSettings{}, &models.ServerSettings{PostgresEnabled: true})
	require.Equal(t, Changed, enable.Postgres.Kind)
	require.Equal(t, &AgentCall{Method: "db.postgres.install", Params: map[string]any{}, Timeout: 5 * time.Minute}, enable.Postgres.Call)

	disable := ModuleEffects(&models.ServerSettings{PostgresEnabled: true}, &models.ServerSettings{})
	require.Equal(t, &AgentCall{Method: "db.postgres.disable", Params: map[string]any{}, Timeout: 5 * time.Minute}, disable.Postgres.Call)

	noop := ModuleEffects(&models.ServerSettings{PostgresEnabled: true}, &models.ServerSettings{PostgresEnabled: true})
	require.Equal(t, NoOp, noop.Postgres.Kind)
	require.Nil(t, noop.Postgres.Call)
}

func TestModuleEffects_DockerMarketplace(t *testing.T) {
	enable := ModuleEffects(&models.ServerSettings{}, &models.ServerSettings{DockerMarketplaceEnabled: true})
	require.Equal(t, &AgentCall{Method: "docker.install", Params: map[string]any{}, Timeout: 10 * time.Minute}, enable.Docker.Call)
	disable := ModuleEffects(&models.ServerSettings{DockerMarketplaceEnabled: true}, &models.ServerSettings{})
	require.Equal(t, &AgentCall{Method: "docker.disable", Params: map[string]any{}, Timeout: 10 * time.Minute}, disable.Docker.Call)
}

func TestModuleEffects_DockerTenant_RevertOnlyOnEnable(t *testing.T) {
	enable := ModuleEffects(&models.ServerSettings{}, &models.ServerSettings{DockerAppsForUsersEnabled: true})
	require.Equal(t, &AgentCall{Method: "docker.tenant_set", Params: map[string]any{"enabled": true}, Timeout: 12 * time.Minute}, enable.DockerTenant.Call)
	require.True(t, enable.DockerTenant.RevertOnFailure, "failed ENABLE must revert the persisted flag")

	disable := ModuleEffects(&models.ServerSettings{DockerAppsForUsersEnabled: true}, &models.ServerSettings{})
	require.Equal(t, map[string]any{"enabled": false}, disable.DockerTenant.Call.Params)
	require.False(t, disable.DockerTenant.RevertOnFailure, "disable must not revert")

	noop := ModuleEffects(&models.ServerSettings{}, &models.ServerSettings{})
	require.Equal(t, NoOp, noop.DockerTenant.Kind)
	require.False(t, noop.DockerTenant.RevertOnFailure)
}

func TestModuleEffects_Python_EnableOnly(t *testing.T) {
	enable := ModuleEffects(&models.ServerSettings{}, &models.ServerSettings{PythonAppsEnabled: true})
	require.Equal(t, &AgentCall{Method: "app.python.install_runtime", Params: map[string]any{}, Timeout: 10 * time.Minute}, enable.Python.Call)

	// disable leaves packages — no verb, NoOp (not a Changed with nil Call).
	disable := ModuleEffects(&models.ServerSettings{PythonAppsEnabled: true}, &models.ServerSettings{})
	require.Equal(t, NoOp, disable.Python.Kind)
	require.Nil(t, disable.Python.Call)
}

func TestModuleEffects_GenericLoop_OrderAndVerbs(t *testing.T) {
	// order is fixed: dns, mail, quota, security, ftp
	p := ModuleEffects(&models.ServerSettings{}, &models.ServerSettings{})
	got := make([]string, 0, len(p.Modules))
	for _, m := range p.Modules {
		got = append(got, m.Key)
	}
	require.Equal(t, []string{"dns", "mail", "quota", "security", "ftp"}, got)

	for _, key := range []string{"dns", "mail", "quota", "security"} {
		enable := findModule(t, ModuleEffects(&models.ServerSettings{}, withModule(key, true)), key)
		require.Equal(t, &AgentCall{Method: "system.module.install", Params: map[string]any{"key": key}, Timeout: 10 * time.Minute}, enable.Call)

		disable := findModule(t, ModuleEffects(withModule(key, true), &models.ServerSettings{}), key)
		require.Equal(t, &AgentCall{Method: "system.module.disable", Params: map[string]any{"key": key}, Timeout: 2 * time.Minute}, disable.Call, "non-ftp disable is fail-soft")
	}
}

// TestModuleEffects_FTP_FailClosed is the assertion that must never regress:
// FTP disable is the fail-closed ftp.disable verb (JAB-259), never
// system.module.disable.
func TestModuleEffects_FTP_FailClosed(t *testing.T) {
	enable := findModule(t, ModuleEffects(&models.ServerSettings{}, &models.ServerSettings{FTPEnabled: true}), "ftp")
	require.Equal(t, &AgentCall{Method: "system.module.install", Params: map[string]any{"key": "ftp"}, Timeout: 10 * time.Minute}, enable.Call)

	disable := findModule(t, ModuleEffects(&models.ServerSettings{FTPEnabled: true}, &models.ServerSettings{}), "ftp")
	require.Equal(t, "ftp.disable", disable.Call.Method, "FTP disable must be fail-closed ftp.disable")
	require.Equal(t, map[string]any{}, disable.Call.Params)
	require.Equal(t, 2*time.Minute, disable.Call.Timeout)
}

func TestModuleEffects_FTPReapply(t *testing.T) {
	// config change while enabled → reapply via install.
	before := &models.ServerSettings{FTPEnabled: true, FTPAllowPlaintext: false}
	after := &models.ServerSettings{FTPEnabled: true, FTPAllowPlaintext: true}
	p := ModuleEffects(before, after)
	require.Equal(t, Reapply, p.FTPReapply.Kind)
	require.Equal(t, &AgentCall{Method: "system.module.install", Params: map[string]any{"key": "ftp"}, Timeout: 10 * time.Minute}, p.FTPReapply.Call)
	// module itself did not toggle
	require.Equal(t, NoOp, findModule(t, p, "ftp").Kind)

	// enabling FTP with a config field also set: the toggle installs, reapply
	// stays NoOp (it fires only when FTP was ALREADY enabled).
	toggling := ModuleEffects(&models.ServerSettings{FTPEnabled: false, FTPAllowPlaintext: false},
		&models.ServerSettings{FTPEnabled: true, FTPAllowPlaintext: true})
	require.Equal(t, NoOp, toggling.FTPReapply.Kind, "reapply must not fire while FTP is toggling on")
	require.Equal(t, Changed, findModule(t, toggling, "ftp").Kind)

	// no config change → no reapply.
	same := ModuleEffects(&models.ServerSettings{FTPEnabled: true}, &models.ServerSettings{FTPEnabled: true})
	require.Equal(t, NoOp, same.FTPReapply.Kind)
}

// withModule returns a ServerSettings with one generic-loop module flag set.
func withModule(key string, v bool) *models.ServerSettings {
	s := &models.ServerSettings{}
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
	return s
}
