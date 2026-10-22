package api

// ADR-0175: Node.js apps on the Application Manager — runtime "node" in the
// python_apps registry, behind its own node_apps_enabled setting.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// appsOn is a settings repository with Python and Node.js apps enabled.
func appsOn() *fakeSettingsRepo {
	return &fakeSettingsRepo{s: &models.ServerSettings{PythonAppsEnabled: true, NodeAppsEnabled: true}}
}

type createFakeApps struct {
	repository.PythonAppRepository
	created *models.PythonApp
	app     *models.PythonApp
	env     []models.PythonAppEnv
}

func (f *createFakeApps) FindFreeLoopbackPort(context.Context) (int, error) { return 20007, nil }
func (f *createFakeApps) Create(_ context.Context, app *models.PythonApp) error {
	f.created = app
	return nil
}
func (f *createFakeApps) FindByID(context.Context, string) (*models.PythonApp, error) {
	return f.app, nil
}
func (f *createFakeApps) ReplaceEnv(_ context.Context, _ string, env []models.PythonAppEnv) error {
	f.env = env
	return nil
}

func nodeCreateBody(edit func(map[string]any)) map[string]any {
	b := map[string]any{
		"domain_id": "dom1", "name": "api", "runtime": "node",
		"app_root": "apps/api", "entrypoint": "server.js",
	}
	if edit != nil {
		edit(b)
	}
	return b
}

func errorOf(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return body.Error
}

func TestCreate_NodeAppIsStoredWithItsRuntime(t *testing.T) {
	apps := &createFakeApps{}
	domains := &staticFakeDomains{domain: &models.Domain{ID: "dom1", UserID: "u1"}}
	h := &pythonAppHandler{cfg: PythonAppHandlerConfig{Settings: appsOn(), Apps: apps, Domains: domains}}

	c, w := createCtx(t, nodeCreateBody(func(b map[string]any) {
		b["app_type"] = "npm"
		delete(b, "entrypoint")
		b["env"] = map[string]string{"API_KEY": "x"}
	}))
	h.create(c)

	if w.Code != 201 {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	a := apps.created
	if a == nil || a.Runtime != "node" || a.PythonVersion != "22" || a.AppType != "npm" || a.Entrypoint != "start" ||
		a.LoopbackPort == nil || *a.LoopbackPort != 20007 {
		t.Fatalf("created = %+v", a)
	}
	if len(apps.env) != 1 || apps.env[0].Key != "API_KEY" {
		t.Fatalf("env = %+v", apps.env)
	}
	// The domain proxies the mount to the app's loopback port.
	if r := domains.domain.NginxRules; len(r) != 1 || r[0].Type != "proxy_pass" || r[0].Target != "http://127.0.0.1:20007" {
		t.Fatalf("nginx rules = %+v", r)
	}
}

func TestCreate_NodeAppValidation(t *testing.T) {
	cases := []struct {
		name string
		edit func(map[string]any)
		want string
	}{
		{"unknown runtime", func(b map[string]any) { b["runtime"] = "ruby" }, "invalid_runtime"},
		{"framework", func(b map[string]any) { b["framework"] = "django" }, "invalid_framework"},
		{"other node version", func(b map[string]any) { b["node_version"] = "20" }, "invalid_node_version"},
		{"python app type", func(b map[string]any) { b["app_type"] = "wsgi" }, "invalid_app_type"},
		{"no entrypoint", func(b map[string]any) { delete(b, "entrypoint") }, "invalid_entrypoint"},
		{"entrypoint out of the app", func(b map[string]any) { b["entrypoint"] = "../other/server.js" }, "invalid_entrypoint"},
		{"absolute entrypoint", func(b map[string]any) { b["entrypoint"] = "/home/bob/server.js" }, "invalid_entrypoint"},
		{"python entrypoint", func(b map[string]any) { b["entrypoint"] = "app:app" }, "invalid_entrypoint"},
		{"npm script with a command", func(b map[string]any) { b["app_type"] = "npm"; b["entrypoint"] = "start; id" }, "invalid_entrypoint"},
		{"PORT in env", func(b map[string]any) { b["env"] = map[string]string{"PORT": "3000"} }, "reserved_env"},
		{"HOST in env", func(b map[string]any) { b["env"] = map[string]string{"HOST": "0.0.0.0"} }, "reserved_env"},
	}
	for _, tc := range cases {
		h := &pythonAppHandler{cfg: PythonAppHandlerConfig{Settings: appsOn()}}
		c, w := createCtx(t, nodeCreateBody(tc.edit))
		h.create(c)
		if w.Code != 400 || errorOf(t, w) != tc.want {
			t.Errorf("%s: status %d error %q, want 400 %q", tc.name, w.Code, errorOf(t, w), tc.want)
		}
	}
}

// Each runtime is created only while its own setting is on; with no settings
// both are off.
func TestCreate_RuntimeNeedsItsSetting(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings *fakeSettingsRepo
		runtime  string
		want     string
	}{
		{"node off", &fakeSettingsRepo{s: &models.ServerSettings{PythonAppsEnabled: true}}, "node", "node_apps_disabled"},
		{"python off", &fakeSettingsRepo{s: &models.ServerSettings{NodeAppsEnabled: true}}, "python", "python_apps_disabled"},
		{"python by default", &fakeSettingsRepo{s: &models.ServerSettings{NodeAppsEnabled: true}}, "", "python_apps_disabled"},
		{"no settings", &fakeSettingsRepo{}, "node", "node_apps_disabled"},
	} {
		h := &pythonAppHandler{cfg: PythonAppHandlerConfig{Settings: tc.settings}}
		c, w := createCtx(t, nodeCreateBody(func(b map[string]any) { b["runtime"] = tc.runtime }))
		h.create(c)
		if w.Code != 403 || errorOf(t, w) != tc.want {
			t.Errorf("%s: status %d error %q, want 403 %q", tc.name, w.Code, errorOf(t, w), tc.want)
		}
	}
}

// The Application Manager's routes open when either runtime is enabled.
func TestRequireEnabled_EitherRuntime(t *testing.T) {
	for _, tc := range []struct {
		python, node bool
		open         bool
	}{
		{true, false, true}, {false, true, true}, {true, true, true}, {false, false, false},
	} {
		h := &pythonAppHandler{cfg: PythonAppHandlerConfig{Settings: &fakeSettingsRepo{s: &models.ServerSettings{PythonAppsEnabled: tc.python, NodeAppsEnabled: tc.node}}}}
		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/python-apps", nil)
		h.requireEnabled(c)
		if c.IsAborted() == tc.open {
			t.Errorf("python=%v node=%v: aborted=%v, want open=%v", tc.python, tc.node, c.IsAborted(), tc.open)
		}
	}
}

func putEnvCtx(t *testing.T, env map[string]string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	b, _ := json.Marshal(map[string]any{"env": env})
	c.Request = httptest.NewRequest("PUT", "/python-apps/app1/env", bytes.NewReader(b))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: "app1"}}
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
	return c, w
}

// PORT and HOST belong to the panel for a Node.js app; a Python app keeps any
// variable it likes.
func TestPutEnv_NodeAppReservesPortAndHost(t *testing.T) {
	for _, runtime := range []string{"node", "python"} {
		apps := &createFakeApps{app: &models.PythonApp{ID: "app1", UserID: "u1", Runtime: runtime}}
		h := &pythonAppHandler{cfg: PythonAppHandlerConfig{Apps: apps}}
		c, w := putEnvCtx(t, map[string]string{"PORT": "3000", "DEBUG": "1"})
		h.putEnv(c)
		if runtime == "node" && (w.Code != 400 || errorOf(t, w) != "reserved_env" || apps.env != nil) {
			t.Errorf("node: status %d error %q env %v, want 400 reserved_env and nothing saved", w.Code, errorOf(t, w), apps.env)
		}
		if runtime == "python" && (w.Code != 200 || len(apps.env) != 2) {
			t.Errorf("python: status %d env %v, want 200 and both saved", w.Code, apps.env)
		}
	}
}
