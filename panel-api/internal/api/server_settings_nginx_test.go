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

// nginxTunablesCall waits (the dispatch is an async goroutine) for the
// nginx.tunables.apply call the PATCH handler makes and returns its decoded
// params. Fails if none arrives. This characterizes the exact agent wire so the
// JAB-290 settingsops extraction is proven byte-identical: the assertions were
// written and run GREEN against the pre-refactor handler, and stay GREEN through
// the extraction.
func nginxTunablesCall(t *testing.T, mockAgent *agent.MockClient) map[string]any {
	t.Helper()
	var params map[string]any
	require.Eventually(t, func() bool {
		for _, c := range mockAgent.Calls() {
			if c.Command == "nginx.tunables.apply" {
				require.NoError(t, json.Unmarshal(c.Params, &params))
				return true
			}
		}
		return false
	}, 2*time.Second, 5*time.Millisecond, "nginx.tunables.apply was never dispatched")
	return params
}

func nginxCommandDispatched(mockAgent *agent.MockClient, command string) bool {
	for _, c := range mockAgent.Calls() {
		if c.Command == command {
			return true
		}
	}
	return false
}

// TestServerSettingsPatch_NginxTunables_Wire pins the exact nginx.tunables.apply
// parameter map (13 keys) the REST PATCH dispatches when a tunable is touched.
func TestServerSettingsPatch_NginxTunables_Wire(t *testing.T) {
	existing := &models.ServerSettings{ID: 1, SSHPort: 22}
	mockRepo := &mockServerSettingsRepo{getResult: existing}
	mockAgent := agent.NewMockClient()
	mockAgent.On("nginx.tunables.apply", map[string]any{"status": "ok"})

	r := settingsRouter(true, mockRepo, mockAgent)

	body, _ := json.Marshal(map[string]any{
		"nginx_client_max_body_size":  "50m",
		"nginx_keepalive_timeout":     "65s",
		"nginx_server_tokens":         true,
		"nginx_gzip":                  true,
		"nginx_client_body_timeout":   "60s",
		"nginx_client_header_timeout": "60s",
		"nginx_send_timeout":          "60s",
		"nginx_proxy_connect_timeout": "60s",
		"nginx_proxy_read_timeout":    "60s",
		"nginx_proxy_send_timeout":    "60s",
		"nginx_worker_processes":      "auto",
		"nginx_worker_connections":    1024,
		"nginx_custom_http":           "# hi",
	})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	params := nginxTunablesCall(t, mockAgent)
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
		"worker_connections":    float64(1024), // JSON round-trip
		"custom_http":           "# hi",
	}, params)
}

// TestServerSettingsPatch_NginxCache_Wire pins the nginx.cache.capacity_apply
// wire (REST-only; the CLI has no cache setters today).
func TestServerSettingsPatch_NginxCache_Wire(t *testing.T) {
	existing := &models.ServerSettings{ID: 1, SSHPort: 22}
	mockRepo := &mockServerSettingsRepo{getResult: existing}
	mockAgent := agent.NewMockClient()
	mockAgent.On("nginx.cache.capacity_apply", map[string]any{"status": "ok"})

	r := settingsRouter(true, mockRepo, mockAgent)

	body, _ := json.Marshal(map[string]any{
		"nginx_cache_max_size_gb":  4,
		"nginx_cache_keyzone_mb":   32,
		"nginx_cache_inactive_min": 90,
	})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var params map[string]any
	require.Eventually(t, func() bool {
		for _, c := range mockAgent.Calls() {
			if c.Command == "nginx.cache.capacity_apply" {
				require.NoError(t, json.Unmarshal(c.Params, &params))
				return true
			}
		}
		return false
	}, 2*time.Second, 5*time.Millisecond, "nginx.cache.capacity_apply was never dispatched")
	require.Equal(t, map[string]any{
		"max_size_gb":  float64(4),
		"keyzone_mb":   float64(32),
		"inactive_min": float64(90),
	}, params)
}

// TestServerSettingsPatch_NoNginxChange_NoDispatch: an unrelated PATCH must not
// dispatch either nginx verb.
func TestServerSettingsPatch_NoNginxChange_NoDispatch(t *testing.T) {
	existing := &models.ServerSettings{ID: 1, SSHPort: 22, Hostname: "keep.example.com"}
	mockRepo := &mockServerSettingsRepo{getResult: existing}
	mockAgent := agent.NewMockClient()

	r := settingsRouter(true, mockRepo, mockAgent)

	body, _ := json.Marshal(map[string]any{"admin_email": "ops@example.com"})
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	// Give any (erroneous) goroutine a chance to fire before asserting absence.
	time.Sleep(50 * time.Millisecond)
	require.False(t, nginxCommandDispatched(mockAgent, "nginx.tunables.apply"))
	require.False(t, nginxCommandDispatched(mockAgent, "nginx.cache.capacity_apply"))
}
