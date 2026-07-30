package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
)

type fakeAgent struct {
	called  bool
	lastCmd string
	resp    json.RawMessage
}

func (f *fakeAgent) Call(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
	f.called = true
	f.lastCmd = cmd
	if f.resp == nil {
		return json.RawMessage(`{"ok":true}`), nil
	}
	return f.resp, nil
}

func newAdminServicesTestRouter(t *testing.T, ag *fakeAgent) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "user-test", IsAdmin: true})
		c.Next()
	})
	RegisterAdminServicesRoutes(v1, AdminServicesHandlerConfig{Agent: ag})
	return r
}

func doPost(t *testing.T, r *gin.Engine, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(nil))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestAdminServices_AllowedActions(t *testing.T) {
	// cron is a non-panel-critical unit — every action is allowed on it.
	// (nginx/redis-server used to stand in here but are now stop/disable-
	// blocked, GH #746.)
	for _, action := range []string{"restart", "start", "stop", "reload", "enable", "disable"} {
		ag := &fakeAgent{}
		r := newAdminServicesTestRouter(t, ag)
		w := doPost(t, r, "/v1/admin/services/cron/"+action)
		if w.Code != http.StatusOK {
			t.Errorf("action %s: got %d, want 200; body=%s", action, w.Code, w.Body.String())
		}
		if !ag.called {
			t.Errorf("action %s: agent not called", action)
		}
		wantCmd := "service." + action
		if ag.lastCmd != wantCmd {
			t.Errorf("action %s: agent cmd=%q want %q", action, ag.lastCmd, wantCmd)
		}
	}
}

func TestAdminServices_RejectsUnknownAction(t *testing.T) {
	ag := &fakeAgent{}
	r := newAdminServicesTestRouter(t, ag)
	w := doPost(t, r, "/v1/admin/services/nginx/exec")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown action: got %d, want 400", w.Code)
	}
	if ag.called {
		t.Errorf("agent should not be called for unknown action")
	}
}

func TestAdminServices_SelfDestructBlocked(t *testing.T) {
	cases := []struct {
		unit   string
		action string
	}{
		{"jabali-panel", "stop"},
		{"jabali-panel", "disable"},
		{"jabali-agent", "stop"},
		{"jabali-agent", "disable"},
		{"mariadb", "stop"},
		{"mariadb", "disable"},
		// GH #746: nginx (reverse proxy -> 502) and redis-server (hard
		// jabali-panel dep -> panel dies) are also panel-critical.
		{"nginx", "stop"},
		{"nginx", "disable"},
		{"redis-server", "stop"},
		{"redis-server", "disable"},
		// GH #746 follow-up: jabali-kratos is the identity provider —
		// stopping it locks every operator out of the panel.
		{"jabali-kratos", "stop"},
		{"jabali-kratos", "disable"},
	}
	for _, c := range cases {
		ag := &fakeAgent{}
		r := newAdminServicesTestRouter(t, ag)
		w := doPost(t, r, "/v1/admin/services/"+c.unit+"/"+c.action)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s/%s: got %d, want 403; body=%s", c.unit, c.action, w.Code, w.Body.String())
		}
		if ag.called {
			t.Errorf("%s/%s: agent should not be called", c.unit, c.action)
		}
	}
}

func TestAdminServices_SelfDestructTrioCanRestart(t *testing.T) {
	for _, unit := range []string{"jabali-panel", "jabali-agent", "mariadb"} {
		for _, action := range []string{"restart", "reload", "start", "enable"} {
			ag := &fakeAgent{}
			r := newAdminServicesTestRouter(t, ag)
			w := doPost(t, r, "/v1/admin/services/"+unit+"/"+action)
			if w.Code != http.StatusOK {
				t.Errorf("%s/%s should be allowed: got %d", unit, action, w.Code)
			}
		}
	}
}

func TestAdminServices_RejectsBadName(t *testing.T) {
	ag := &fakeAgent{}
	r := newAdminServicesTestRouter(t, ag)
	w := doPost(t, r, "/v1/admin/services/nginx;rm/restart")
	// gin treats ; as a separator; bad name characters get filtered by
	// regex on whatever lands in :name — we expect a 400 or 404 here.
	if w.Code != http.StatusBadRequest && w.Code != http.StatusNotFound {
		t.Errorf("expected 400/404 for bad name, got %d", w.Code)
	}
}
