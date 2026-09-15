package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
)

// JAB-373 end-to-end: a second poll of /admin/server-status inside the slice
// TTLs reuses the cached snapshot, so the agent is called ONCE per slice across
// both requests — not once per request. Proves the aggregator actually routes
// through the cache (the unit test proves the cache mechanism in isolation).
func TestServerStatus_SecondPollWithinTTL_NoExtraAgentCalls(t *testing.T) {
	mock := agent.NewMockClient().
		On("system.info", map[string]any{
			"hostname": "t.local", "os": "Debian 13", "kernel": "6.12",
			"cpu_count": 4, "load_avg": []float64{0.1, 0.1, 0.1},
			"partitions": []map[string]any{}, "mem_total_kb": 1000, "mem_used_kb": 100,
		}).
		On("system.cpu_usage", map[string]any{"usage_percent": 10.0, "warming_up": false}).
		On("system.network", map[string]any{"interfaces": []any{}}).
		On("system.service_details", map[string]any{
			"services": []map[string]any{
				{"unit": "jabali-panel.service", "active": "active", "sub": "running", "unit_file_state": "enabled"},
			},
		}).
		On("nginx.status", map[string]any{"active": true})

	r := newServerStatusRouter(mock, true)
	do := func() api.ServerStatusEnvelope {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/server-status", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		var env api.ServerStatusEnvelope
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
		return env
	}

	do()         // populates every slice cache
	env2 := do() // within all TTLs → every cached slice served without a fresh call

	// Count agent calls per command across BOTH requests.
	counts := map[string]int{}
	for _, c := range mock.Calls() {
		counts[c.Command]++
	}
	// Cached commands: exactly one call for two requests.
	for _, cmd := range []string{"system.info", "system.cpu_usage", "system.network", "system.service_details", "nginx.status"} {
		assert.Equalf(t, 1, counts[cmd], "%s should be fetched once and cached, got %d calls over 2 requests", cmd, counts[cmd])
	}

	// Shared-bytes guard: the second request re-derives the services slice from
	// the SAME cached bytes the first request already read (filterModuleServices
	// runs per request). Prove those bytes were not mutated in place — the
	// second response's services slice must still be well-formed.
	if assert.NotNil(t, env2.Services, "services slice must survive a second read of the cached bytes") {
		var svc struct {
			Services []map[string]any `json:"services"`
		}
		assert.NoError(t, json.Unmarshal(*env2.Services, &svc), "cached services bytes corrupted after a second request")
		assert.Len(t, svc.Services, 1)
	}
}

// JAB-373 AC #7: the cache-metrics endpoint exposes per-slice counters. After a
// poll fetches each slice once, host reports one refresh and at least one miss.
func TestServerStatus_CacheMetricsEndpoint(t *testing.T) {
	mock := agent.NewMockClient().
		On("system.info", map[string]any{
			"hostname": "t.local", "os": "Debian 13", "kernel": "6.12",
			"cpu_count": 4, "load_avg": []float64{0.1, 0.1, 0.1},
			"partitions": []map[string]any{}, "mem_total_kb": 1000, "mem_used_kb": 100,
		}).
		On("system.cpu_usage", map[string]any{"usage_percent": 10.0, "warming_up": false}).
		On("system.network", map[string]any{"interfaces": []any{}}).
		On("system.service_details", map[string]any{"services": []map[string]any{}}).
		On("nginx.status", map[string]any{"active": true})

	r := newServerStatusRouter(mock, true)

	// A poll populates the per-slice counters.
	pr := httptest.NewRequest(http.MethodGet, "/api/v1/admin/server-status", nil)
	prec := httptest.NewRecorder()
	r.ServeHTTP(prec, pr)
	assert.Equal(t, http.StatusOK, prec.Code)

	// The metrics endpoint returns them.
	mr := httptest.NewRequest(http.MethodGet, "/api/v1/admin/server-status/cache-metrics", nil)
	mrec := httptest.NewRecorder()
	r.ServeHTTP(mrec, mr)
	assert.Equal(t, http.StatusOK, mrec.Code)

	var body struct {
		Slices map[string]struct {
			Miss    uint64 `json:"miss"`
			Refresh uint64 `json:"refresh"`
		} `json:"slices"`
	}
	assert.NoError(t, json.Unmarshal(mrec.Body.Bytes(), &body))
	host, ok := body.Slices["host"]
	if assert.True(t, ok, "host slice must appear in the cache metrics") {
		assert.Equal(t, uint64(1), host.Refresh, "one poll fetches host once")
		assert.GreaterOrEqual(t, host.Miss, uint64(1))
	}
}

// The cache-metrics endpoint inherits the parent group's RequireAdmin: a
// non-admin is forbidden, same as the base server-status route.
func TestServerStatus_CacheMetrics_RBAC(t *testing.T) {
	r := newServerStatusRouter(agent.NewMockClient(), false)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/server-status/cache-metrics", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}
