package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
)

// JAB-373: the fixed Health projection (?view=health) — the admin header, mounted
// on every admin page, renders only alerts (from host/services/apparmor/nginx),
// so it must NOT pay for system.processes (walks every PID + a 200ms sample),
// system.software, or system.user_slices. Every other slice still refreshes.
func TestServerStatus_HealthProjectionSkipsExpensiveSlices(t *testing.T) {
	mock := agent.NewMockClient().
		On("system.info", map[string]any{"hostname": "t.local", "partitions": []any{}, "mem_total_kb": 1000, "mem_used_kb": 100}).
		On("system.cpu_usage", map[string]any{"usage_percent": 1.0}).
		On("system.network", map[string]any{"interfaces": []any{}}).
		On("system.processes", map[string]any{"total": 200}).
		On("system.service_details", map[string]any{"services": []any{}}).
		On("system.user_slices", map[string]any{"slices": []any{}}).
		On("system.software", map[string]any{"packages": []any{}}).
		On("nginx.status", map[string]any{"active": "active"}).
		On("security.apparmor.summary", map[string]any{"enabled": false})

	r := newServerStatusRouter(mock, true)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/server-status?view=health", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	called := map[string]bool{}
	for _, c := range mock.Calls() {
		called[c.Command] = true
	}
	for _, cmd := range []string{"system.processes", "system.software", "system.user_slices"} {
		assert.False(t, called[cmd], "health view must NOT invoke %s", cmd)
	}
	for _, cmd := range []string{"system.info", "system.cpu_usage", "system.network", "system.service_details", "nginx.status", "security.apparmor.summary"} {
		assert.True(t, called[cmd], "health view must still invoke %s", cmd)
	}

	var env map[string]json.RawMessage
	assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	for _, k := range []string{"processes", "software", "user_slices"} {
		_, ok := env[k]
		assert.False(t, ok, "health envelope must omit %q", k)
	}
	// The header's inputs are present.
	for _, k := range []string{"host", "services", "alerts"} {
		_, ok := env[k]
		assert.True(t, ok, "health envelope must keep %q", k)
	}
}

// The full envelope (no view param) still fetches everything — the Health
// projection must not change default behavior.
func TestServerStatus_FullViewFetchesAllSlices(t *testing.T) {
	mock := agent.NewMockClient().
		On("system.info", map[string]any{"partitions": []any{}}).
		On("system.cpu_usage", map[string]any{"usage_percent": 1.0}).
		On("system.network", map[string]any{"interfaces": []any{}}).
		On("system.processes", map[string]any{"total": 1}).
		On("system.service_details", map[string]any{"services": []any{}}).
		On("system.user_slices", map[string]any{"slices": []any{}}).
		On("system.software", map[string]any{"packages": []any{}}).
		On("nginx.status", map[string]any{"active": "active"}).
		On("security.apparmor.summary", map[string]any{"enabled": false})

	r := newServerStatusRouter(mock, true)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/server-status", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	called := map[string]bool{}
	for _, c := range mock.Calls() {
		called[c.Command] = true
	}
	for _, cmd := range []string{"system.processes", "system.software", "system.user_slices"} {
		assert.True(t, called[cmd], "full view must invoke %s", cmd)
	}
}
