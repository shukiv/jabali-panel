package api

// GH #878: per-app static-asset split — validation + the PUT /static
// attach/replace/clear lifecycle against the domain's nginx rules.

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

func TestValidateStaticSplit(t *testing.T) {
	cases := []struct {
		name, url, root string
		wantErr         bool
	}{
		{"both empty ok", "", "", false},
		{"passenger public", "/static", "public", false},
		{"django style", "/static/", "staticfiles", false},
		{"nested", "/assets", "app/static/dist", false},
		{"url only", "/static", "", true},
		{"root only", "", "public", true},
		{"url no leading slash", "static", "public", true},
		{"url traversal", "/../etc", "public", true},
		{"url dotdot segment", "/a/../b", "public", true},
		{"root traversal", "/static", "../../etc", true},
		{"root absolute", "/static", "/etc", true},
		{"url space", "/sta tic", "public", true},
		{"url semicolon", "/static;", "public", true},
		{"root brace", "/static", "pub{lic}", true},
		{"url bare slash", "/", "public", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateStaticSplit(tc.url, tc.root)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateStaticSplit(%q, %q) err = %v, wantErr %v", tc.url, tc.root, err, tc.wantErr)
			}
		})
	}
}

type staticFakeApps struct {
	repository.PythonAppRepository
	app *models.PythonApp
}

func (f *staticFakeApps) FindByID(_ context.Context, id string) (*models.PythonApp, error) {
	return f.app, nil
}
func (f *staticFakeApps) Update(_ context.Context, app *models.PythonApp) error {
	f.app = app
	return nil
}

type staticFakeDomains struct {
	repository.DomainRepository
	domain *models.Domain
}

func (f *staticFakeDomains) FindByID(_ context.Context, id string) (*models.Domain, error) {
	return f.domain, nil
}
func (f *staticFakeDomains) Update(_ context.Context, d *models.Domain) error {
	f.domain = d
	return nil
}

func putStaticCtx(t *testing.T, appID string, body map[string]any) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	b, _ := json.Marshal(body)
	c.Request = httptest.NewRequest("PUT", "/python-apps/"+appID+"/static", bytes.NewReader(b))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: appID}}
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
	return c, w
}

func staticRulesOf(d *models.Domain) []models.NginxRule {
	var out []models.NginxRule
	for _, r := range d.NginxRules {
		if r.Type == "static_alias" {
			out = append(out, r)
		}
	}
	return out
}

func TestPutStatic_AttachReplaceClear(t *testing.T) {
	port := 21000
	apps := &staticFakeApps{app: &models.PythonApp{
		ID: "app1", UserID: "u1", DomainID: "dom1",
		AppRoot: "/home/u1/myapp", BaseURI: "/", LoopbackPort: &port,
	}}
	ws := true
	domains := &staticFakeDomains{domain: &models.Domain{
		ID: "dom1",
		NginxRules: models.NginxRules{
			{Type: "proxy_pass", Path: "/", Target: "http://127.0.0.1:21000", Websocket: &ws},
		},
	}}
	h := &pythonAppHandler{cfg: PythonAppHandlerConfig{Apps: apps, Domains: domains}}

	// Attach: /static -> public/
	c, w := putStaticCtx(t, "app1", map[string]any{"static_url": "/static", "static_root": "public"})
	h.putStatic(c)
	if w.Code != 200 {
		t.Fatalf("attach status = %d, body %s", w.Code, w.Body.String())
	}
	got := staticRulesOf(domains.domain)
	if len(got) != 1 || got[0].Path != "/static" || got[0].Target != "/home/u1/myapp/public/" {
		t.Fatalf("after attach rules = %+v", got)
	}
	if apps.app.StaticURL != "/static" || apps.app.StaticRoot != "public" {
		t.Fatalf("app not persisted: %+v", apps.app)
	}

	// Replace: /assets -> dist/ — the old /static location must go away.
	c, w = putStaticCtx(t, "app1", map[string]any{"static_url": "/assets", "static_root": "dist"})
	h.putStatic(c)
	if w.Code != 200 {
		t.Fatalf("replace status = %d", w.Code)
	}
	got = staticRulesOf(domains.domain)
	if len(got) != 1 || got[0].Path != "/assets" || got[0].Target != "/home/u1/myapp/dist/" {
		t.Fatalf("after replace rules = %+v", got)
	}

	// Clear: both empty — static_alias gone, proxy_pass survives.
	c, w = putStaticCtx(t, "app1", map[string]any{"static_url": "", "static_root": ""})
	h.putStatic(c)
	if w.Code != 200 {
		t.Fatalf("clear status = %d", w.Code)
	}
	if got = staticRulesOf(domains.domain); len(got) != 0 {
		t.Fatalf("after clear static rules = %+v", got)
	}
	proxies := 0
	for _, r := range domains.domain.NginxRules {
		if r.Type == "proxy_pass" {
			proxies++
		}
	}
	if proxies != 1 {
		t.Fatalf("proxy_pass count = %d, want 1", proxies)
	}
}

func TestPutStatic_RejectsInvalid(t *testing.T) {
	apps := &staticFakeApps{app: &models.PythonApp{ID: "app1", UserID: "u1", DomainID: "dom1", AppRoot: "/home/u1/myapp", BaseURI: "/"}}
	h := &pythonAppHandler{cfg: PythonAppHandlerConfig{Apps: apps, Domains: &staticFakeDomains{domain: &models.Domain{ID: "dom1"}}}}
	c, w := putStaticCtx(t, "app1", map[string]any{"static_url": "/static", "static_root": "../../etc"})
	h.putStatic(c)
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if body.Error != "invalid_static" {
		t.Errorf("error = %q, want invalid_static", body.Error)
	}
}

func TestPutStatic_ForbidsOtherUsersApp(t *testing.T) {
	apps := &staticFakeApps{app: &models.PythonApp{ID: "app1", UserID: "someone-else", DomainID: "dom1", AppRoot: "/home/x/app", BaseURI: "/"}}
	h := &pythonAppHandler{cfg: PythonAppHandlerConfig{Apps: apps}}
	c, w := putStaticCtx(t, "app1", map[string]any{"static_url": "/static", "static_root": "public"})
	h.putStatic(c)
	if w.Code != 403 {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}
