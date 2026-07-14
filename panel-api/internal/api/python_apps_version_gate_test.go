package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
)

// createCtx builds a gin context carrying claims + a JSON create body.
func createCtx(t *testing.T, body map[string]any) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	b, _ := json.Marshal(body)
	c.Request = httptest.NewRequest("POST", "/python-apps", bytes.NewReader(b))
	c.Request.Header.Set("Content-Type", "application/json")
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
	return c, w
}

// TestCreate_RejectsUninstalledPythonVersion locks the GH #357 gate: the
// create handler bounces a version whose interpreter isn't on the host —
// before any domain/package work — echoing the installed set.
func TestCreate_RejectsUninstalledPythonVersion(t *testing.T) {
	ag := agent.NewMockClient()
	ag.On("app.python.versions", map[string]any{"versions": []string{"3.13"}, "default": "3.13"})
	h := &pythonAppHandler{cfg: PythonAppHandlerConfig{Agent: ag}}

	c, w := createCtx(t, map[string]any{
		"domain_id":      "dom1",
		"name":           "app",
		"python_version": "3.11", // not installed → must be rejected
		"app_root":       "domains/x/app",
		"app_type":       "wsgi",
		"entrypoint":     "app:app",
		"base_uri":       "/",
	})
	h.create(c)

	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	var body struct {
		Error     string   `json:"error"`
		Available []string `json:"available"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Error != "python_version_not_available" {
		t.Errorf("error = %q, want python_version_not_available", body.Error)
	}
	if len(body.Available) != 1 || body.Available[0] != "3.13" {
		t.Errorf("available = %v, want [3.13]", body.Available)
	}
}

// TestCreate_VersionGateFailsOpen ensures an unavailable probe (agent nil /
// error) does NOT block creation on the version check — the request falls
// through to the normal path (which 404s here on the missing domain repo).
func TestCreate_VersionGateFailsOpen(t *testing.T) {
	// Agent that errors on the probe → gate skipped (fail-open).
	ag := agent.NewMockClient() // no On() → returns unknown_command error
	h := &pythonAppHandler{cfg: PythonAppHandlerConfig{Agent: ag}}

	c, w := createCtx(t, map[string]any{
		"domain_id":      "dom1",
		"name":           "app",
		"python_version": "3.11",
		"app_root":       "domains/x/app",
		"app_type":       "wsgi",
		"entrypoint":     "app:app",
		"base_uri":       "/",
	})
	// Domains repo is nil; a fail-open gate lets execution reach the domain
	// lookup, which panics on nil. Guard so the test asserts intent without
	// wiring every repo: we only care that it did NOT 400 on the version.
	defer func() { _ = recover() }()
	h.create(c)
	if w.Code == 400 {
		var body struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if body.Error == "python_version_not_available" {
			t.Error("gate must fail-open when the probe is unavailable")
		}
	}
}

// TestVersionsEndpoint_NoAgentReturnsEmpty verifies the versions endpoint
// degrades to an empty list rather than erroring when no agent is wired.
func TestVersionsEndpoint_NoAgentReturnsEmpty(t *testing.T) {
	h := &pythonAppHandler{cfg: PythonAppHandlerConfig{}}
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/python-apps/versions", nil)
	h.versions(c)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body struct {
		Versions []string `json:"versions"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if len(body.Versions) != 0 {
		t.Errorf("versions = %v, want empty", body.Versions)
	}
}
