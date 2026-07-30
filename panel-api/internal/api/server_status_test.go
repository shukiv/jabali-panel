package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
)

func newServerStatusRouter(mock agent.AgentInterface, isAdmin bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(injectAdminClaims(isAdmin))
	api.RegisterAdminServerStatusRoutes(v1, api.AdminServerStatusHandlerConfig{Agent: mock})
	return r
}

func TestServerStatus_RBAC(t *testing.T) {
	mock := agent.NewMockClient()
	r := newServerStatusRouter(mock, false)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/server-status", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestServerStatus_HappyPath(t *testing.T) {
	mock := agent.NewMockClient().
		On("system.info", map[string]any{
			"hostname":     "test.local",
			"os":           "Debian 13",
			"kernel":       "6.12",
			"cpu_count":    4,
			"load_avg":     []float64{0.1, 0.1, 0.1},
			"partitions":   []map[string]any{},
			"mem_total_kb": 1000,
			"mem_used_kb":  100,
		}).
		On("system.cpu_usage", map[string]any{"usage_percent": 12.5, "warming_up": false}).
		On("system.network", map[string]any{"interfaces": []any{}}).
		On("system.processes", map[string]any{"total": 200, "running": 1, "zombie": 0}).
		On("system.service_details", map[string]any{
			"services": []map[string]any{
				{"unit": "jabali-panel.service", "active": "active", "sub": "running", "unit_file_state": "enabled"},
				{"unit": "mariadb.service", "active": "inactive", "unit_file_state": "enabled"},
				// Lazy-started service: disabled + inactive must NOT alert.
				{"unit": "jabali-webmail.service", "active": "inactive", "unit_file_state": "disabled"},
			},
		})

	r := newServerStatusRouter(mock, true)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/server-status", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var env api.ServerStatusEnvelope
	require := func(err error) {
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	require(json.Unmarshal(rec.Body.Bytes(), &env))

	if env.Host == nil {
		t.Fatal("host slice missing")
	}
	if env.Services == nil {
		t.Fatal("services slice missing")
	}

	// mariadb inactive (unit_file_state=enabled) must produce a critical
	// service alert. jabali-webmail inactive (unit_file_state=disabled)
	// must NOT — it's a lazy-started service.
	mariadbAlert := false
	webmailAlert := false
	for _, a := range env.Alerts {
		if a.Kind != "service" || a.Level != "critical" {
			continue
		}
		switch a.Detail {
		case "mariadb.service is inactive":
			mariadbAlert = true
		case "jabali-webmail.service is inactive":
			webmailAlert = true
		}
	}
	assert.True(t, mariadbAlert, "expected critical alert for inactive enabled mariadb")
	assert.False(t, webmailAlert, "must not alert on inactive disabled jabali-webmail (lazy-started)")
}

// slowMockAgent simulates a sub-call that exceeds the per-call timeout.
type slowMockAgent struct {
	*agent.MockClient
	slowCommand string
	delay       time.Duration
}

func (m *slowMockAgent) Call(ctx context.Context, cmd string, params any) (json.RawMessage, error) {
	if cmd == m.slowCommand {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return m.MockClient.Call(ctx, cmd, params)
}

func TestServerStatus_TimeoutDoesNotKillEnvelope(t *testing.T) {
	base := agent.NewMockClient().
		On("system.info", map[string]any{"hostname": "h"}).
		On("system.network", map[string]any{"interfaces": []any{}}).
		On("system.cpu_usage", map[string]any{"usage_percent": 0.0}).
		On("system.service_details", map[string]any{"services": []map[string]any{}})
	// processes returns nothing (mock has no entry → ErrUnknownCommand);
	// AND we delay it past the 5s sub-call timeout via a slow wrapper.
	// To keep the test fast we use a shorter delay than the prod cap;
	// the assertion is "envelope still arrives even if a slice times out".
	wrapped := &slowMockAgent{
		MockClient:  base,
		slowCommand: "system.processes",
		delay:       50 * time.Millisecond,
	}
	// Simulate timeout by having the slow command return a deadline
	// exceeded — quicker than waiting 5s in a unit test.
	_ = wrapped
	base.OnError("system.processes", context.DeadlineExceeded)

	r := newServerStatusRouter(base, true)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/server-status", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var env api.ServerStatusEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Host == nil {
		t.Fatal("host slice missing despite processes timeout")
	}
	if env.Errors["processes"] == "" {
		t.Fatal("expected processes error captured in envelope")
	}
	// Alert for the timed-out slice surfaces as a warning.
	gotProcAlert := false
	for _, a := range env.Alerts {
		if a.Kind == "agent" && a.Level == "warning" {
			gotProcAlert = true
		}
	}
	assert.True(t, gotProcAlert, "expected agent-warning alert for failed sub-call")

	_ = errors.Is // keep import
}

// TestServerStatus_QueuesTolerateNOGROUP: when the notifications consumer group
// doesn't exist yet (fresh box / Redis wipe), the queues probe must report
// zeros rather than surfacing a raw NOGROUP error as a status banner.
func TestServerStatus_QueuesTolerateNOGROUP(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	// Deliberately do NOT create the stream/consumer group → XPending NOGROUPs.

	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(injectAdminClaims(true))
	api.RegisterAdminServerStatusRoutes(v1, api.AdminServerStatusHandlerConfig{
		Agent: agent.NewMockClient(),
		Redis: rdb,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/server-status", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var env struct {
		Queues *struct {
			NotificationsQueue   int64 `json:"notifications_queue"`
			NotificationsPending int64 `json:"notifications_pending"`
		} `json:"queues"`
		Errors map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, bad := env.Errors["queues"]; bad {
		t.Errorf("queues probe surfaced a NOGROUP error banner: %q", env.Errors["queues"])
	}
	if env.Queues == nil {
		t.Fatal("queues slice missing — should report zeros on an uninitialized group")
	}
	assert.Equal(t, int64(0), env.Queues.NotificationsPending)
}
