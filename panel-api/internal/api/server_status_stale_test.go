package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
)

// fullServerStatusEnvelope drives the aggregator once against the handler and
// returns the decoded envelope. Used to poll twice — before and after the cache
// goes stale — in the AC #5 display-only invariant test.
func fullServerStatusEnvelope(t *testing.T, h *adminServerStatusHandler) ServerStatusEnvelope {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/server-status", nil)
	h.get(c)
	if w.Code != http.StatusOK {
		t.Fatalf("server-status: HTTP %d", w.Code)
	}
	var env ServerStatusEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	return env
}

func hasAlertKind(env ServerStatusEnvelope, kind string) bool {
	for _, a := range env.Alerts {
		if a.Kind == kind {
			return true
		}
	}
	return false
}

// AC #5 correctness invariant: a stale last-good slice is DISPLAY-ONLY. It is
// returned in the envelope with meta.stale=true, but it must never drive alert
// synthesis — feeding a 40-second-old snapshot into the alert path could raise a
// phantom outage or paper over a real one. This pins that the `services` slice's
// content alert ("<unit> is failed") is present while the slice is fresh and
// GONE once it is only stale-served, even though the last-good body still
// carries the failed unit. Reverting the aggregator to feed `results` (fresh +
// stale) instead of `fresh` to synthesizeAlerts reddens this test.
func TestServerStatus_StaleSliceIsDisplayOnly_NotInAlerts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cl := &clock{t: time.Unix(1_000_000, 0)}
	cache := newStatusCache()
	cache.now = cl.now

	mock := agent.NewMockClient().
		On("system.info", map[string]any{
			"hostname": "t.local", "os": "Debian 13", "kernel": "6.12",
			"cpu_count": 4, "load_avg": []float64{0.1, 0.1, 0.1},
			"partitions": []map[string]any{}, "mem_total_kb": 1000, "mem_used_kb": 100,
		}).
		On("system.cpu_usage", map[string]any{"usage_percent": 10.0, "warming_up": false}).
		On("system.network", map[string]any{"interfaces": []any{}}).
		On("system.processes", map[string]any{"total": 100, "running": 1, "zombie": 0}).
		On("system.service_details", map[string]any{
			// A FAILED unit: while fresh this must raise a critical service alert.
			"services": []map[string]any{
				{"unit": "jabali-panel.service", "active": "failed", "unit_file_state": "enabled"},
			},
		}).
		On("system.user_slices", map[string]any{"slices": []any{}}).
		On("system.software", map[string]any{"packages": []any{}}).
		On("nginx.status", map[string]any{"active": true}).
		On("security.apparmor.summary", map[string]any{"enabled": false})

	h := &adminServerStatusHandler{
		cfg:   AdminServerStatusHandlerConfig{Agent: mock},
		cache: cache,
	}

	// Poll 1 — everything fresh. The failed unit MUST surface a service alert.
	fresh := fullServerStatusEnvelope(t, h)
	if !hasAlertKind(fresh, "service") {
		t.Fatal("fresh poll: a failed unit must raise a service alert (baseline)")
	}

	// Expire every slice (past the 30s AppArmor TTL and 15s service TTL), then
	// make every agent call fail so the next poll must stale-serve.
	cl.advance(40 * time.Second)
	for _, cmd := range []string{
		"system.info", "system.cpu_usage", "system.network", "system.processes",
		"system.service_details", "system.user_slices", "system.software",
		"nginx.status", "security.apparmor.summary",
	} {
		mock.OnError(cmd, errors.New("agent down"))
	}

	// Poll 2 — refreshes fail; last-good is stale-served.
	stale := fullServerStatusEnvelope(t, h)

	// AC #5 display: the slice body is still present (last-good), flagged stale.
	if stale.Services == nil {
		t.Fatal("stale poll: the services slice must still be served (last-good)")
	}
	m, ok := stale.Meta["services"]
	if !ok || !m.Stale {
		t.Fatalf("stale poll: meta[services].stale must be true, got %+v (ok=%v)", m, ok)
	}
	if m.ObservedAt == "" {
		t.Fatal("stale poll: meta[services].observed_at must be set")
	}
	if _, ok := stale.Errors["services"]; !ok {
		t.Fatal("stale poll: the failed refresh must still record an errors[services] entry")
	}

	// THE INVARIANT: the stale services body (still a failed unit) must NOT
	// produce the content-derived service alert.
	if hasAlertKind(stale, "service") {
		t.Fatal("stale poll: a stale (display-only) slice must not drive a content alert")
	}
}
