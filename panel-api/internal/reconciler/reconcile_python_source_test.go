package reconciler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/pyframeworks"
)

// GH #2156: sic is a "source" entry — a full upstream app fetched from a
// pinned commit tarball — that needs ImageMagick's libMagickWand. The scaffold
// call must carry the pinned archive and the library, and every apply must
// name the library again so a host that lost it (restore, distro upgrade)
// gets it back.

func loadRealCatalog(t *testing.T) *pyframeworks.Catalog {
	t.Helper()
	cat, errs := pyframeworks.LoadDir("../../../install/py-frameworks")
	if len(errs) != 0 {
		t.Fatalf("catalog load errors: %v", errs)
	}
	return cat
}

func TestScaffoldPythonApp_Source(t *testing.T) {
	cat := loadRealCatalog(t)
	entry, ok := cat.Get("sic")
	if !ok {
		t.Fatal("sic missing from the catalog")
	}
	ag := &scaffoldStubAgent{}
	repo := &stubPyRepo{}
	r := &Reconciler{
		agent:        ag,
		pythonApps:   repo,
		pyFrameworks: cat,
		domains:      &stubDomainRepo{name: "news.example.com"},
		log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	app := &models.PythonApp{
		ID: "01APPSIC", UserID: "01U", DomainID: "01D",
		Framework: "sic", AppRoot: "/home/t/sic", PythonVersion: "3.12",
	}
	if ok := r.scaffoldPythonApp(context.Background(), app, "t"); !ok {
		t.Fatal("scaffoldPythonApp returned false")
	}

	if ag.params["scaffold_kind"] != "source" {
		t.Fatalf("scaffold_kind = %v, want source", ag.params["scaffold_kind"])
	}
	if ag.params["source_url"] != entry.Scaffold.URL || ag.params["source_sha256"] != entry.Scaffold.SHA256 {
		t.Fatalf("source_url %v / source_sha256 %v, want the catalog pin", ag.params["source_url"], ag.params["source_sha256"])
	}
	if _, has := ag.params["template_files"]; has {
		t.Errorf("a source scaffold must not send template_files")
	}
	if _, has := ag.params["scaffold_argv"]; has {
		t.Errorf("a source scaffold must not send scaffold_argv")
	}
	if !reflect.DeepEqual(ag.params["system_libs"], []string{"magickwand"}) {
		t.Errorf("system_libs = %#v, want [magickwand]", ag.params["system_libs"])
	}
	if s, _ := ag.params["patch_script"].(string); s == "" {
		t.Errorf("sic's patch script must be sent")
	}
	env, _ := ag.params["env"].(map[string]string)
	if env["DJANGO_ALLOWED_HOSTS"] != "news.example.com" || env["JAB_STATIC_ROOT"] != "staticfiles" || env["JAB_PROJECT"] != "sic" {
		t.Errorf("patch env = %v", env)
	}
	gen, _ := ag.params["generate"].([]string)
	if !reflect.DeepEqual(gen, []string{"DJANGO_SECRET_KEY", "SIC_ADMIN_PASSWORD"}) {
		t.Errorf("generate = %v", gen)
	}
	if repo.marked != app.ID {
		t.Errorf("app not marked scaffolded")
	}
}

// A starter that names no library sends none.
func TestScaffoldPythonApp_NoSystemLibs(t *testing.T) {
	ag := &scaffoldStubAgent{}
	r := &Reconciler{
		agent:        ag,
		pythonApps:   &stubPyRepo{},
		pyFrameworks: loadRealCatalog(t),
		domains:      &stubDomainRepo{name: "blog.example.com"},
		log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	app := &models.PythonApp{
		ID: "01APPDJ", UserID: "01U", DomainID: "01D",
		Framework: "django", AppRoot: "/home/t/blog", PythonVersion: "3.12",
	}
	if !r.scaffoldPythonApp(context.Background(), app, "t") {
		t.Fatal("scaffoldPythonApp returned false")
	}
	if _, has := ag.params["system_libs"]; has {
		t.Errorf("django names no library but system_libs = %#v", ag.params["system_libs"])
	}
	if _, has := ag.params["source_url"]; has {
		t.Errorf("a cmd scaffold must not send source_url")
	}
}

// applyCaptureAgent records the app.python.apply params and reports the app
// running.
type applyCaptureAgent struct{ params map[string]any }

func (a *applyCaptureAgent) Call(_ context.Context, _ string, params interface{}) (json.RawMessage, error) {
	a.params, _ = params.(map[string]any)
	return json.Marshal(map[string]any{"active": true, "unit": "jabali-app-01APP.service"})
}

func TestReconcileOnePythonApp_ApplyNamesSystemLibs(t *testing.T) {
	cat := loadRealCatalog(t)
	uname := "tenant"
	done := time.Now()
	for _, tc := range []struct {
		name      string
		framework string
		catalog   *pyframeworks.Catalog
		want      any
	}{
		{"sic", "sic", cat, []string{"magickwand"}},
		{"django", "django", cat, nil},
		{"hand-configured app", "", cat, nil},
		{"no catalog loaded", "sic", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ag := &applyCaptureAgent{}
			r := &Reconciler{
				agent:        ag,
				pythonApps:   &statusCaptureRepo{},
				pyFrameworks: tc.catalog,
				users:        notActiveUsers{u: &models.User{ID: "01USER", Username: &uname}},
				log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
			}
			app := tasksLimitApp(models.PythonAppStatusRunning, nil)
			app.Framework = tc.framework
			app.ScaffoldedAt = &done
			r.reconcileOnePythonApp(context.Background(), app)
			if ag.params == nil {
				t.Fatal("app.python.apply was not called")
			}
			got, has := ag.params["system_libs"]
			if tc.want == nil {
				if has {
					t.Fatalf("system_libs = %#v, want none", got)
				}
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("system_libs = %#v, want %#v", got, tc.want)
			}
		})
	}
}
