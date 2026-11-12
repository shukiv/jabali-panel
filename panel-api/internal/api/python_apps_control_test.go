package api

// The app controls: start, stop, restart, and rebuild, which builds the app
// again after its code changed (an app is otherwise built only when its
// package.json, lockfile or requirements.txt change). A rebuild while a build
// is running is a 409 a script can tell from a failure, and an unknown action
// never reaches the agent.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type pyControlFakeAgent struct {
	calls  []map[string]any
	err    error
	method string
}

func (f *pyControlFakeAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	f.method = cmd
	f.calls = append(f.calls, params.(map[string]any))
	return json.RawMessage(`{"active":true,"state":"active"}`), f.err
}

func controlPythonApp(t *testing.T, ag *pyControlFakeAgent, action string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	port := 20017
	h := &pythonAppHandler{cfg: PythonAppHandlerConfig{
		Apps:  &pyDeleteFakeApps{app: &models.PythonApp{ID: "app1", UserID: "u1", DomainID: "dom1", LoopbackPort: &port}, order: &pyDeleteOrder{}},
		Agent: ag,
	}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body, _ := json.Marshal(map[string]string{"action": action})
	c.Request = httptest.NewRequest("POST", "/python-apps/app1/control", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: "app1"}}
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
	h.control(c)
	return w
}

func TestPythonAppControl_RebuildReachesTheAgent(t *testing.T) {
	ag := &pyControlFakeAgent{}
	w := controlPythonApp(t, ag, "rebuild")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if ag.method != "app.python.control" || len(ag.calls) != 1 || ag.calls[0]["action"] != "rebuild" || ag.calls[0]["app_id"] != "app1" {
		t.Fatalf("agent got %s %v", ag.method, ag.calls)
	}
}

func TestPythonAppControl_RebuildWhileBuildingIs409(t *testing.T) {
	ag := &pyControlFakeAgent{err: &agentwire.AgentError{Code: agent.CodeFailedPrecondition, Message: "a build of this app is already running; rebuild it once that build has finished"}}
	w := controlPythonApp(t, ag, "rebuild")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"build_running"`) {
		t.Fatalf("status %d body %s, want 409 build_running", w.Code, w.Body)
	}
}

func TestPythonAppControl_UnknownActionNeverReachesTheAgent(t *testing.T) {
	for _, action := range []string{"reboot", "rebuild; rm", "START", ""} {
		ag := &pyControlFakeAgent{}
		w := controlPythonApp(t, ag, action)
		if w.Code != http.StatusBadRequest || len(ag.calls) != 0 {
			t.Fatalf("action %q: status %d, %d agent calls; want 400 and none", action, w.Code, len(ag.calls))
		}
	}
}

func TestPythonAppControl_KnownActionsPass(t *testing.T) {
	for _, action := range []string{"start", "stop", "restart", "rebuild"} {
		ag := &pyControlFakeAgent{}
		if w := controlPythonApp(t, ag, action); w.Code != http.StatusOK || len(ag.calls) != 1 {
			t.Fatalf("action %q: status %d, %d agent calls", action, w.Code, len(ag.calls))
		}
	}
}
