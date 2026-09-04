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

// nginxTestSettings has every nginx tunable populated; hostname/postgres/docker/
// python are left at their zero values and matched by prev so only the nginx
// side effect fires.
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

// TestApplySideEffects_NginxDispatchesSharedWire proves the CLI executes the
// settingsops plan synchronously with the exact wire (JAB-290 AC5: the CLI's
// execution policy stays in the adapter; the wire is shared).
func TestApplySideEffects_NginxDispatchesSharedWire(t *testing.T) {
	nginxDirty = true
	t.Cleanup(func() { nginxDirty = false })
	calls := stubSettingsDispatch(t, nil)

	s := nginxTestSettings()
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})

	err := applySettingsSideEffects(context.Background(), cmd, nil, s, models.ServerSettings{}, sideEffectSnapshot(s))
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
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})

	err := applySettingsSideEffects(context.Background(), cmd, nil, s, models.ServerSettings{}, sideEffectSnapshot(s))
	require.Error(t, err)
	require.Contains(t, err.Error(), "DB updated, but nginx.tunables.apply failed")
	require.Contains(t, err.Error(), "nginx -t rejected config")
}

// TestApplySideEffects_NoNginx_NoDispatch: with nginxDirty clear, nothing is
// dispatched.
func TestApplySideEffects_NoNginx_NoDispatch(t *testing.T) {
	nginxDirty = false
	calls := stubSettingsDispatch(t, nil)

	s := nginxTestSettings()
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})

	err := applySettingsSideEffects(context.Background(), cmd, nil, s, models.ServerSettings{}, sideEffectSnapshot(s))
	require.NoError(t, err)
	require.Empty(t, *calls)
}
