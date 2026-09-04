package main

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/settingsops"
)

// nginxTestSettings has every nginx tunable populated; the module flags are left
// at their zero values (matched by a zero `before`) so only the nginx side effect
// fires.
func nginxTestSettings() *models.ServerSettings {
	return &models.ServerSettings{
		SSHPort:                  22,
		NginxClientMaxBodySize:   "50m",
		NginxKeepaliveTimeout:    "65s",
		NginxServerTokens:        true,
		NginxGzip:                true,
		NginxClientBodyTimeout:   "60s",
		NginxClientHeaderTimeout: "60s",
		NginxSendTimeout:         "60s",
		NginxProxyConnectTimeout: "60s",
		NginxProxyReadTimeout:    "60s",
		NginxProxySendTimeout:    "60s",
		NginxWorkerProcesses:     "auto",
		NginxWorkerConnections:   1024,
		NginxCustomHTTP:          "# hi",
	}
}

// stubSettingsDispatch swaps in a recording dispatcher and restores the real one
// after the test. Returns a pointer to the captured calls.
func stubSettingsDispatch(t *testing.T, retErr error) *[]settingsops.AgentCall {
	t.Helper()
	var calls []settingsops.AgentCall
	orig := dispatchSettingsAgentCall
	dispatchSettingsAgentCall = func(_ context.Context, call settingsops.AgentCall) error {
		calls = append(calls, call)
		return retErr
	}
	t.Cleanup(func() { dispatchSettingsAgentCall = orig })
	return &calls
}

// recordingRepo is a minimal ServerSettingsRepository that records Upsert calls
// so the docker-tenant revert path can be asserted.
type recordingRepo struct {
	current *models.ServerSettings
	upserts []*models.ServerSettings
}

func (r *recordingRepo) Get(context.Context) (*models.ServerSettings, error) {
	cp := *r.current
	return &cp, nil
}
func (r *recordingRepo) Upsert(_ context.Context, s *models.ServerSettings) error {
	cp := *s
	r.upserts = append(r.upserts, &cp)
	r.current = &cp
	return nil
}
func (r *recordingRepo) EnsureVAPID(context.Context, string) (bool, error) { return false, nil }
func (r *recordingRepo) SetDigestLastSent(context.Context, string) error   { return nil }
func (r *recordingRepo) RecordDRSync(context.Context, string, string, string) error {
	return nil
}
func (r *recordingRepo) ReassertDRPairing(context.Context, string, string, *time.Time) error {
	return nil
}

func applyCmd() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	return cmd
}

// TestApplySideEffects_NginxDispatchesSharedWire proves the CLI executes the
// settingsops plan synchronously with the exact wire (JAB-290 AC5: the CLI's
// execution policy stays in the adapter; the wire is shared).
func TestApplySideEffects_NginxDispatchesSharedWire(t *testing.T) {
	nginxDirty = true
	t.Cleanup(func() { nginxDirty = false })
	calls := stubSettingsDispatch(t, nil)

	s := nginxTestSettings()
	err := applySettingsSideEffects(context.Background(), applyCmd(), nil, s, models.ServerSettings{})
	require.NoError(t, err)

	require.Len(t, *calls, 1, "exactly one nginx dispatch (no cache verb on the CLI)")
	call := (*calls)[0]
	require.Equal(t, "nginx.tunables.apply", call.Method)
	require.Equal(t, 60*time.Second, call.Timeout)
	require.Equal(t, map[string]any{
		"client_max_body_size":  "50m",
		"keepalive_timeout":     "65s",
		"server_tokens":         true,
		"gzip":                  true,
		"client_body_timeout":   "60s",
		"client_header_timeout": "60s",
		"send_timeout":          "60s",
		"proxy_connect_timeout": "60s",
		"proxy_read_timeout":    "60s",
		"proxy_send_timeout":    "60s",
		"worker_processes":      "auto",
		"worker_connections":    uint32(1024),
		"custom_http":           "# hi",
	}, call.Params)
}

// TestApplySideEffects_NginxFailureSurfaced: a failed apply is returned as a
// non-silent error naming the verb (the DB is already persisted).
func TestApplySideEffects_NginxFailureSurfaced(t *testing.T) {
	nginxDirty = true
	t.Cleanup(func() { nginxDirty = false })
	stubSettingsDispatch(t, errors.New("nginx -t rejected config"))

	s := nginxTestSettings()
	err := applySettingsSideEffects(context.Background(), applyCmd(), nil, s, models.ServerSettings{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "DB updated, but nginx.tunables.apply failed")
	require.Contains(t, err.Error(), "nginx -t rejected config")
}

// TestApplySideEffects_NoNginx_NoDispatch: with nginxDirty clear and no module
// change, nothing is dispatched.
func TestApplySideEffects_NoNginx_NoDispatch(t *testing.T) {
	nginxDirty = false
	calls := stubSettingsDispatch(t, nil)

	s := nginxTestSettings()
	err := applySettingsSideEffects(context.Background(), applyCmd(), nil, s, models.ServerSettings{})
	require.NoError(t, err)
	require.Empty(t, *calls)
}

// TestApplySideEffects_ModuleWire proves the CLI dispatches the four module
// transitions it has setters for with the shared settingsops wire.
func TestApplySideEffects_ModuleWire(t *testing.T) {
	cases := []struct {
		name           string
		before, after  models.ServerSettings
		wantMethod     string
		wantParams     map[string]any
		wantTimeoutMin time.Duration
	}{
		{"postgres enable", models.ServerSettings{}, models.ServerSettings{PostgresEnabled: true}, "db.postgres.install", map[string]any{}, 5},
		{"postgres disable", models.ServerSettings{PostgresEnabled: true}, models.ServerSettings{}, "db.postgres.disable", map[string]any{}, 5},
		{"docker enable", models.ServerSettings{}, models.ServerSettings{DockerMarketplaceEnabled: true}, "docker.install", map[string]any{}, 10},
		{"docker-tenant enable", models.ServerSettings{DockerMarketplaceEnabled: true}, models.ServerSettings{DockerMarketplaceEnabled: true, DockerAppsForUsersEnabled: true}, "docker.tenant_set", map[string]any{"enabled": true}, 12},
		{"python enable", models.ServerSettings{}, models.ServerSettings{PythonAppsEnabled: true}, "app.python.install_runtime", map[string]any{}, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := stubSettingsDispatch(t, nil)
			after := tc.after
			err := applySettingsSideEffects(context.Background(), applyCmd(), nil, &after, tc.before)
			require.NoError(t, err)
			require.Len(t, *calls, 1)
			require.Equal(t, tc.wantMethod, (*calls)[0].Method)
			require.Equal(t, tc.wantParams, (*calls)[0].Params)
			require.Equal(t, tc.wantTimeoutMin*time.Minute, (*calls)[0].Timeout)
		})
	}
}

// TestApplySideEffects_PythonDisableNoDispatch: disabling python installs nothing.
func TestApplySideEffects_PythonDisableNoDispatch(t *testing.T) {
	calls := stubSettingsDispatch(t, nil)
	after := models.ServerSettings{}
	err := applySettingsSideEffects(context.Background(), applyCmd(), nil, &after, models.ServerSettings{PythonAppsEnabled: true})
	require.NoError(t, err)
	require.Empty(t, *calls)
}

// TestApplySideEffects_DockerTenantRevertOnFailedEnable proves the CLI reverts
// the persisted flag when a tenant-Docker ENABLE fails — settingsops flags the
// decision (RevertOnEnableFailure), this adapter runs the repo write.
func TestApplySideEffects_DockerTenantRevertOnFailedEnable(t *testing.T) {
	stubSettingsDispatch(t, errors.New("unprivileged LXC: userns-remap unavailable"))
	repo := &recordingRepo{current: &models.ServerSettings{DockerMarketplaceEnabled: true, DockerAppsForUsersEnabled: true}}

	after := models.ServerSettings{DockerMarketplaceEnabled: true, DockerAppsForUsersEnabled: true}
	before := models.ServerSettings{DockerMarketplaceEnabled: true, DockerAppsForUsersEnabled: false}
	err := applySettingsSideEffects(context.Background(), applyCmd(), repo, &after, before)

	require.Error(t, err)
	require.Contains(t, err.Error(), "flag reverted")
	require.Len(t, repo.upserts, 1, "revert must persist exactly one flag-clearing Upsert")
	require.False(t, repo.upserts[0].DockerAppsForUsersEnabled, "revert must clear the flag")
}

// TestApplySideEffects_DockerTenantDisableNoRevert: a failed DISABLE surfaces the
// error but does not revert (there is nothing to undo).
func TestApplySideEffects_DockerTenantDisableNoRevert(t *testing.T) {
	stubSettingsDispatch(t, errors.New("agent down"))
	repo := &recordingRepo{current: &models.ServerSettings{}}

	after := models.ServerSettings{DockerMarketplaceEnabled: true, DockerAppsForUsersEnabled: false}
	before := models.ServerSettings{DockerMarketplaceEnabled: true, DockerAppsForUsersEnabled: true}
	err := applySettingsSideEffects(context.Background(), applyCmd(), repo, &after, before)

	require.Error(t, err)
	require.NotContains(t, err.Error(), "flag reverted")
	require.Empty(t, repo.upserts, "disable failure must not revert")
}

// TestApplySideEffects_RESTOnlyModulesSkippedByCLI: the dns/mail/quota/security/
// ftp loop is REST-only (no CLI setters). Even if `before`/`after` differ on
// those flags, the CLI dispatches nothing for them — the fail-closed ftp.disable
// path is never triggered from the CLI.
func TestApplySideEffects_RESTOnlyModulesSkippedByCLI(t *testing.T) {
	calls := stubSettingsDispatch(t, nil)
	after := models.ServerSettings{} // ftp/dns off
	before := models.ServerSettings{FTPEnabled: true, DNSEnabled: true, MailEnabled: true}
	err := applySettingsSideEffects(context.Background(), applyCmd(), nil, &after, before)
	require.NoError(t, err)
	require.Empty(t, *calls, "REST-only module loop must not dispatch from the CLI")
}
