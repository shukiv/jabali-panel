package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/pyframeworks"
)

func loadTestCatalog(t *testing.T) *pyframeworks.Catalog {
	t.Helper()
	cat, errs := pyframeworks.LoadDir("../../../install/py-frameworks")
	if len(errs) != 0 {
		t.Fatalf("catalog load errors: %v", errs)
	}
	return cat
}

// TestFrameworksEndpoint returns the catalog as a clean snake_case DTO with no
// template bytes.
func TestFrameworksEndpoint(t *testing.T) {
	h := &pythonAppHandler{cfg: PythonAppHandlerConfig{Settings: appsOn(), Catalog: loadTestCatalog(t)}}
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/python-apps/frameworks", nil)
	h.frameworks(c)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if strings.Contains(w.Body.String(), "template_files") || strings.Contains(w.Body.String(), "TemplateFiles") {
		t.Error("frameworks response must not ship template bytes")
	}
	var body struct {
		Frameworks []struct {
			Slug    string `json:"slug"`
			AppType string `json:"app_type"`
			Server  string `json:"server"`
		} `json:"frameworks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	seen := map[string]string{}
	for _, f := range body.Frameworks {
		seen[f.Slug] = f.AppType
	}
	if seen["django"] != "wsgi" || seen["fastapi"] != "asgi" {
		t.Errorf("catalog DTO wrong: %v", seen)
	}
}

// TestCreate_UnknownFramework 400s before any provisioning.
func TestCreate_UnknownFramework(t *testing.T) {
	h := &pythonAppHandler{cfg: PythonAppHandlerConfig{Settings: appsOn(), Catalog: loadTestCatalog(t)}}
	c, w := createCtx(t, map[string]any{
		"domain_id": "dom1", "name": "app", "python_version": "3.12",
		"app_root": "domains/x/app", "base_uri": "/", "framework": "nope",
	})
	h.create(c)
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Error != "unknown_framework" {
		t.Errorf("error = %q, want unknown_framework", body.Error)
	}
}

// TestCreate_FrameworkDerivesEntrypoint proves a framework install needs no
// entrypoint in the body: flask derives app:app, so the request gets PAST the
// entrypoint validation (and here reaches the nil domain repo, recovered).
func TestCreate_FrameworkDerivesEntrypoint(t *testing.T) {
	ag := agent.NewMockClient()
	ag.On("app.python.versions", map[string]any{"versions": []string{"3.12"}, "default": "3.12"})
	h := &pythonAppHandler{cfg: PythonAppHandlerConfig{Settings: appsOn(), Agent: ag, Catalog: loadTestCatalog(t)}}
	c, w := createCtx(t, map[string]any{
		"domain_id": "dom1", "name": "app", "python_version": "3.12",
		"app_root": "domains/x/app", "base_uri": "/", "framework": "flask",
		// no entrypoint / app_type — must be derived from the catalog
	})
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
	defer func() { _ = recover() }() // nil Domains repo panics after the framework resolve
	h.create(c)
	if w.Code == 400 {
		var body struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if body.Error == "invalid_entrypoint" || body.Error == "invalid_app_type" {
			t.Errorf("framework install must derive entrypoint/app_type, got %q", body.Error)
		}
	}
}

// GH #2156: a framework whose pinned deps need a newer Python than the one
// picked is refused at create, naming the minimum, instead of an app that
// later fails inside pip. 3.11 is installed here, so only python_min can say no.
func TestCreate_FrameworkBelowPythonMin(t *testing.T) {
	ag := agent.NewMockClient()
	ag.On("app.python.versions", map[string]any{"versions": []string{"3.11", "3.12"}, "default": "3.12"})
	h := &pythonAppHandler{cfg: PythonAppHandlerConfig{Settings: appsOn(), Agent: ag, Catalog: loadTestCatalog(t)}}
	c, w := createCtx(t, map[string]any{
		"domain_id": "dom1", "name": "news", "python_version": "3.11",
		"app_root": "domains/x/news", "base_uri": "/", "framework": "sic",
	})
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
	// A request that gets past the gate panics on the nil Domains repo, so
	// the checks run after recover.
	defer func() {
		_ = recover()
		var body struct {
			Error  string `json:"error"`
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != 400 || body.Error != "python_version_too_old" {
			t.Fatalf("status %d error %q, want 400 python_version_too_old", w.Code, body.Error)
		}
		if !strings.Contains(body.Detail, "3.12") {
			t.Errorf("detail %q must name the minimum version", body.Detail)
		}
	}()
	h.create(c)
}

// The same request on the minimum version gets past the gate.
func TestCreate_FrameworkAtPythonMin(t *testing.T) {
	ag := agent.NewMockClient()
	ag.On("app.python.versions", map[string]any{"versions": []string{"3.11", "3.12"}, "default": "3.12"})
	h := &pythonAppHandler{cfg: PythonAppHandlerConfig{Settings: appsOn(), Agent: ag, Catalog: loadTestCatalog(t)}}
	c, w := createCtx(t, map[string]any{
		"domain_id": "dom1", "name": "news", "python_version": "3.12",
		"app_root": "domains/x/news", "base_uri": "/", "framework": "sic",
	})
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
	defer func() {
		_ = recover()
		var body struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if body.Error == "python_version_too_old" {
			t.Errorf("3.12 meets sic's python_min but was refused")
		}
	}()
	h.create(c)
}
