package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/dockerapp"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// A retired catalog entry (bigcapital: its MinIO images are gone) is left out
// of the catalog and refused by every install door, while the installs made
// before keep it and are told it is retired.

const retiredReason = "its images are no longer published"

// retiredCatalog holds a retired tenant app (tgone) and one still offered
// (tkeep).
func retiredCatalog(t *testing.T) *dockerapp.Catalog {
	t.Helper()
	root := t.TempDir()
	for slug, extra := range map[string]string{"tgone": "retired: \"" + retiredReason + "\"\n", "tkeep": ""} {
		dir := filepath.Join(root, slug)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		app := "slug: " + slug + "\nname: " + strings.ToUpper(slug) + `
version: "1.0.0"
description: x
image_channel: docker.io/library/busybox:1` + digestOf("0") + `
tenant_installable: true
volumes:
  - name: data
    container_path: /data
ports:
  - name: http
    container_port: 8080
    protocol: tcp
    default_enabled: true
    default_bind: loopback
    default_reverse_proxy: true
` + extra
		os.WriteFile(filepath.Join(dir, "app.yaml"), []byte(app), 0o644)
		os.WriteFile(filepath.Join(dir, "compose.yml.tmpl"), []byte("services:\n  app:\n    image: {{ .ImageChannel }}\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "icon.svg"), []byte(`<svg/>`), 0o644)
	}
	cat, errs := dockerapp.LoadDir(root)
	if len(errs) > 0 {
		t.Fatalf("catalog load errors: %v", errs)
	}
	return cat
}

func catalogSlugs(t *testing.T, body []byte) map[string]string {
	t.Helper()
	var got struct {
		Items []struct {
			Slug    string `json:"slug"`
			Retired string `json:"retired"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	out := map[string]string{}
	for _, e := range got.Items {
		out[e.Slug] = e.Retired
	}
	return out
}

func TestAdminDockerCatalog_LeavesRetiredOutUnlessAsked(t *testing.T) {
	h := &dockerAppHandler{cfg: DockerAppHandlerConfig{Catalog: retiredCatalog(t)}}
	for query, want := range map[string]map[string]string{
		"":                   {"tkeep": ""},
		"?include_retired=1": {"tkeep": "", "tgone": retiredReason},
	} {
		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/docker-apps/catalog"+query, nil)
		h.listCatalog(c)
		got := catalogSlugs(t, w.Body.Bytes())
		if len(got) != len(want) {
			t.Fatalf("catalog%s = %v, want %v", query, got, want)
		}
		for slug, reason := range want {
			if r, ok := got[slug]; !ok || r != reason {
				t.Fatalf("catalog%s = %v, want %v", query, got, want)
			}
		}
	}
}

func TestAdminDockerInstall_RefusesARetiredApp(t *testing.T) {
	repo := &noDomainInstallRepo{trackRepo: &trackRepo{}}
	mock := agent.NewMockClient()
	h := &dockerAppHandler{cfg: DockerAppHandlerConfig{Catalog: retiredCatalog(t), Repo: repo, Agent: mock}}
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/docker-apps", strings.NewReader(`{"slug":"tgone","name":"books"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "admin1", IsAdmin: true})
	h.install(c)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "app_retired") || !strings.Contains(w.Body.String(), retiredReason) {
		t.Fatalf("want 400 app_retired with the reason, got %d: %s", w.Code, w.Body.String())
	}
	if repo.created != nil || len(mock.Calls()) != 0 {
		t.Fatalf("nothing may be created: row %+v, agent calls %d", repo.created, len(mock.Calls()))
	}
}

func TestTenantDocker_RetiredAppIsNotOfferedOrInstalled(t *testing.T) {
	gone := &models.DockerApp{ID: "a1", Slug: "tgone", Name: "books"}
	repo := &fakeDockerRepo{owned: map[string]*models.DockerApp{"a1": gone}}
	r := tenantRouter(t, UserDockerAppHandlerConfig{Repo: repo, Catalog: retiredCatalog(t)}, true)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/docker-apps/catalog", nil))
	if got := catalogSlugs(t, rec.Body.Bytes()); len(got) != 1 || got["tkeep"] != "" {
		t.Fatalf("tenant catalog = %v, want only tkeep", got)
	}

	rec = post(r, `{"slug":"tgone","name":"books","domain":"books.example.com"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "app_retired") {
		t.Fatalf("want 400 app_retired, got %d %s", rec.Code, rec.Body.String())
	}

	// The install made before is still served, and says why it is retired.
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/docker-apps/a1", nil))
	var got struct {
		Retired string `json:"retired"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Retired != retiredReason {
		t.Fatalf("get of the old install: %d %s", rec.Code, rec.Body.String())
	}
}

// The CLI's catalog listing and both CLI install doors.
func TestDockerAppCLI_RetiredAppIsNotOfferedOrInstalled(t *testing.T) {
	for file, calls := range map[string][]string{
		"../../cmd/server/docker_app_cmd.go":        {"entries := cat.Listed()", `if entry.Retired != "" {`},
		"../../cmd/server/docker_app_tenant_cmd.go": {`if entry.Retired != "" {`},
	} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range calls {
			if !strings.Contains(string(b), s) {
				t.Errorf("%s no longer has %s", file, s)
			}
		}
	}
}
