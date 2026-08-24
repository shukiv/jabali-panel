package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type ssAgent struct {
	calls   int32
	failCPU bool
}

func (a *ssAgent) Call(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
	atomic.AddInt32(&a.calls, 1)
	switch cmd {
	case "system.info":
		return json.RawMessage(`{"load_avg":[0.5,0.4,0.3],"mem_total_kb":8000000,"mem_used_kb":4000000,"partitions":[{"mount_point":"/","total_bytes":100,"used_bytes":40}]}`), nil
	case "system.cpu_usage":
		if a.failCPU {
			return nil, errors.New("cpu boom")
		}
		return json.RawMessage(`{"usage_percent":12.5,"iowait_percent":1.2,"as_of":"2026-08-24T00:00:00Z"}`), nil
	}
	return json.RawMessage(`{}`), nil
}

func doServerStatus(ss *automationServerStatus) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/automation/server-status", nil)
	ss.handle(c)
	return w
}

func TestServerStatus_NormalizedShape(t *testing.T) {
	ss := newAutomationServerStatus(&ssAgent{})
	w := doServerStatus(ss)
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["as_of"] != "2026-08-24T00:00:00Z" {
		t.Errorf("as_of should come from the cpu slice, got %v", body["as_of"])
	}
	cpu, _ := body["cpu"].(map[string]any)
	if cpu == nil || cpu["usage_percent"].(float64) != 12.5 || cpu["iowait_percent"].(float64) != 1.2 {
		t.Errorf("cpu shape wrong: %v", body["cpu"])
	}
	host, _ := body["host"].(map[string]any)
	if host == nil {
		t.Fatalf("missing host")
	}
	if host["mem_used_kb"].(float64) != 4000000 || host["mem_total_kb"].(float64) != 8000000 {
		t.Errorf("host mem wrong: %v", host)
	}
	if la, _ := host["load_avg"].([]any); len(la) != 3 {
		t.Errorf("load_avg should have 3 entries, got %v", host["load_avg"])
	}
	parts, _ := host["partitions"].([]any)
	if len(parts) != 1 {
		t.Fatalf("expected 1 partition, got %v", parts)
	}
	p0 := parts[0].(map[string]any)
	if p0["mount_point"] != "/" || p0["used_bytes"].(float64) != 40 || p0["total_bytes"].(float64) != 100 {
		t.Errorf("partition shape wrong: %v", p0)
	}
	if _, hasIO := body["io"]; hasIO {
		t.Errorf("io must be omitted when no collector (got %v)", body["io"])
	}
	if _, hasErr := body["errors"]; hasErr {
		t.Errorf("no errors expected on the happy path, got %v", body["errors"])
	}
}

func TestServerStatus_PerSliceErrorDegrades(t *testing.T) {
	ss := newAutomationServerStatus(&ssAgent{failCPU: true})
	w := doServerStatus(ss)
	if w.Code != http.StatusOK {
		t.Fatalf("a failing slice must not fail the whole endpoint; code=%d", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if _, ok := body["host"]; !ok {
		t.Error("host slice should still render when cpu fails")
	}
	if _, ok := body["cpu"]; ok {
		t.Error("cpu slice must be absent when its fetch failed")
	}
	errs, _ := body["errors"].(map[string]any)
	if errs == nil || errs["cpu"] == nil {
		t.Errorf("cpu failure must surface in the errors map, got %v", body["errors"])
	}
}

func TestServerStatus_NilAgent(t *testing.T) {
	ss := newAutomationServerStatus(nil)
	w := doServerStatus(ss)
	if w.Code != http.StatusOK {
		t.Fatalf("nil agent must not fail closed; code=%d", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body["as_of"] == nil {
		t.Error("nil-agent envelope must still carry as_of")
	}
}

func TestServerStatus_CachesRapidPolls(t *testing.T) {
	ag := &ssAgent{}
	ss := newAutomationServerStatus(ag)
	_ = doServerStatus(ss)
	first := atomic.LoadInt32(&ag.calls)
	if first == 0 {
		t.Fatal("expected agent calls on the first poll")
	}
	_ = doServerStatus(ss)
	if atomic.LoadInt32(&ag.calls) != first {
		t.Errorf("second poll within the TTL must hit the cache; calls %d -> %d", first, ag.calls)
	}
}

func ssScopeRouter(scope string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	grp := r.Group("")
	tok := &models.AutomationToken{ID: "t", Scopes: models.AutomationScopes{scope}}
	grp.Use(func(c *gin.Context) { c.Set("jabali_automation_token", tok); c.Next() })
	ss := newAutomationServerStatus(&ssAgent{})
	grp.GET("/server-status", middleware.RequireScope("read:metrics"), ss.handle)
	return r
}

func TestServerStatus_ScopeEnforcement(t *testing.T) {
	for _, tc := range []struct {
		scope string
		want  int
	}{
		{"read:metrics", http.StatusOK},
		{"read:*", http.StatusOK},
		{"read:status", http.StatusForbidden}, // wrong read scope
		{"read:domains", http.StatusForbidden},
	} {
		r := ssScopeRouter(tc.scope)
		req := httptest.NewRequest(http.MethodGet, "/server-status", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Errorf("scope %q: code=%d, want %d", tc.scope, w.Code, tc.want)
		}
	}
}

func TestServerStatus_ScopeInAllowlist(t *testing.T) {
	found := false
	for _, s := range models.AllowedAutomationScopes {
		if s == "read:metrics" {
			found = true
		}
	}
	if !found {
		t.Fatal("read:metrics must be in AllowedAutomationScopes so tokens can be minted with it")
	}
}
