package reconciler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/pyframeworks"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// scaffoldStubAgent captures the app.python.scaffold call and returns a fixed
// generated secret, mimicking the agent's Django SECRET_KEY minting.
type scaffoldStubAgent struct {
	method string
	params map[string]any
}

func (a *scaffoldStubAgent) Call(_ context.Context, method string, params interface{}) (json.RawMessage, error) {
	a.method = method
	a.params, _ = params.(map[string]any)
	return json.Marshal(map[string]any{
		"scaffolded":    true,
		"generated_env": map[string]string{"DJANGO_SECRET_KEY": "generated-secret-xyz"},
	})
}

// stubPyRepo implements just the repository methods scaffoldPythonApp touches;
// the embedded nil interface satisfies the rest (never called here).
type stubPyRepo struct {
	repository.PythonAppRepository
	env      []*models.PythonAppEnv
	replaced []models.PythonAppEnv
	marked   string
}

func (s *stubPyRepo) ListEnv(context.Context, string) ([]*models.PythonAppEnv, error) {
	return s.env, nil
}
func (s *stubPyRepo) ReplaceEnv(_ context.Context, _ string, env []models.PythonAppEnv) error {
	s.replaced = env
	return nil
}
func (s *stubPyRepo) MarkScaffolded(_ context.Context, id string) error { s.marked = id; return nil }

// stubDomainRepo returns a fixed domain name for FindByID.
type stubDomainRepo struct {
	repository.DomainRepository
	name string
}

func (s *stubDomainRepo) FindByID(context.Context, string) (*models.Domain, error) {
	return &models.Domain{Name: s.name}, nil
}

// TestScaffoldPythonApp_Django checks the reconciler's one-shot scaffold: the
// cmd-scaffold argv + Django hardening reach the agent, the generated secret and
// domain-defaulted ALLOWED_HOSTS are persisted, and the app is marked scaffolded.
func TestScaffoldPythonApp_Django(t *testing.T) {
	cat, errs := pyframeworks.LoadDir("../../../install/py-frameworks")
	if len(errs) != 0 {
		t.Fatalf("catalog load errors: %v", errs)
	}
	ag := &scaffoldStubAgent{}
	repo := &stubPyRepo{}
	r := &Reconciler{
		agent:        ag,
		pythonApps:   repo,
		pyFrameworks: cat,
		domains:      &stubDomainRepo{name: "app.example.com"},
		log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	app := &models.PythonApp{
		ID: "01APPDJANGO", UserID: "01USER", DomainID: "01DOM",
		Framework: "django", AppRoot: "/home/tenant/app", PythonVersion: "3.12",
	}

	if ok := r.scaffoldPythonApp(context.Background(), app, "tenant"); !ok {
		t.Fatal("scaffoldPythonApp returned false")
	}

	// Agent got the scaffold call with the Django cmd argv + hardening.
	if ag.method != "app.python.scaffold" {
		t.Fatalf("agent method = %q, want app.python.scaffold", ag.method)
	}
	if ag.params["scaffold_kind"] != "cmd" {
		t.Errorf("scaffold_kind = %v, want cmd", ag.params["scaffold_kind"])
	}
	if ag.params["harden_django"] != true {
		t.Errorf("harden_django = %v, want true", ag.params["harden_django"])
	}
	if ag.params["server"] != "gunicorn" {
		t.Errorf("server = %v, want gunicorn", ag.params["server"])
	}
	if _, ok := ag.params["scaffold_argv"].([]string); !ok {
		t.Errorf("scaffold_argv missing/typed wrong: %T", ag.params["scaffold_argv"])
	}
	if ag.params["allowed_hosts"] != "app.example.com" {
		t.Errorf("allowed_hosts = %v, want the mounted domain", ag.params["allowed_hosts"])
	}

	// Generated secret + domain-defaulted ALLOWED_HOSTS persisted to the app env.
	got := map[string]string{}
	for _, e := range repo.replaced {
		got[e.Key] = e.Value
	}
	if got["DJANGO_SECRET_KEY"] != "generated-secret-xyz" {
		t.Errorf("DJANGO_SECRET_KEY not persisted from the agent: %q", got["DJANGO_SECRET_KEY"])
	}
	if got["DJANGO_ALLOWED_HOSTS"] != "app.example.com" {
		t.Errorf("DJANGO_ALLOWED_HOSTS = %q, want the mounted domain", got["DJANGO_ALLOWED_HOSTS"])
	}
	if repo.marked != app.ID {
		t.Errorf("app not marked scaffolded (marked=%q)", repo.marked)
	}
}

// TestScaffoldPythonApp_DoesNotClobberOperatorEnv verifies an operator-set value
// survives the framework env merge.
func TestScaffoldPythonApp_DoesNotClobberOperatorEnv(t *testing.T) {
	cat, _ := pyframeworks.LoadDir("../../../install/py-frameworks")
	repo := &stubPyRepo{env: []*models.PythonAppEnv{
		{ID: "e1", Key: "DJANGO_ALLOWED_HOSTS", Value: "operator-set.example"},
	}}
	r := &Reconciler{
		agent:        &scaffoldStubAgent{},
		pythonApps:   repo,
		pyFrameworks: cat,
		domains:      &stubDomainRepo{name: "app.example.com"},
		log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	app := &models.PythonApp{ID: "01APP", Framework: "django", AppRoot: "/home/t/app", PythonVersion: "3.12"}
	if ok := r.scaffoldPythonApp(context.Background(), app, "tenant"); !ok {
		t.Fatal("scaffold returned false")
	}
	got := map[string]string{}
	for _, e := range repo.replaced {
		got[e.Key] = e.Value
	}
	if got["DJANGO_ALLOWED_HOSTS"] != "operator-set.example" {
		t.Errorf("operator ALLOWED_HOSTS clobbered: %q", got["DJANGO_ALLOWED_HOSTS"])
	}
}

// TestScaffoldPythonApp_PatchScript covers the JAB-164 patch-script path
// (Wagtail/Channels): an entry that ships a patch.py must dispatch patch_script
// + env + generate (NOT harden_django), with the settings module, static root
// and domain-defaulted ALLOWED_HOSTS in the env, and mint the secret.
func TestScaffoldPythonApp_PatchScript(t *testing.T) {
	cat, errs := pyframeworks.LoadDir("../../../install/py-frameworks")
	if len(errs) != 0 {
		t.Fatalf("catalog load errors: %v", errs)
	}
	ag := &scaffoldStubAgent{}
	repo := &stubPyRepo{}
	r := &Reconciler{
		agent:        ag,
		pythonApps:   repo,
		pyFrameworks: cat,
		domains:      &stubDomainRepo{name: "cms.example.com"},
		log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	app := &models.PythonApp{
		ID: "01APPWAG", UserID: "01U", DomainID: "01D",
		Framework: "wagtail", AppRoot: "/home/t/wag", PythonVersion: "3.12",
	}
	if ok := r.scaffoldPythonApp(context.Background(), app, "t"); !ok {
		t.Fatal("scaffoldPythonApp returned false")
	}

	if _, ok := ag.params["patch_script"].(string); !ok || ag.params["patch_script"].(string) == "" {
		t.Fatalf("patch_script must be sent for a patch-script framework")
	}
	if ag.params["harden_django"] != nil {
		t.Errorf("harden_django must NOT be sent when a patch script is used")
	}
	gen, _ := ag.params["generate"].([]string)
	if len(gen) != 1 || gen[0] != "DJANGO_SECRET_KEY" {
		t.Errorf("generate = %v, want [DJANGO_SECRET_KEY]", ag.params["generate"])
	}
	env, _ := ag.params["env"].(map[string]string)
	if env["DJANGO_SETTINGS_MODULE"] != "config.settings.production" {
		t.Errorf("env DJANGO_SETTINGS_MODULE = %q, want config.settings.production", env["DJANGO_SETTINGS_MODULE"])
	}
	if env["DJANGO_ALLOWED_HOSTS"] != "cms.example.com" {
		t.Errorf("env DJANGO_ALLOWED_HOSTS = %q, want the mounted domain", env["DJANGO_ALLOWED_HOSTS"])
	}
	if env["JAB_STATIC_ROOT"] == "" {
		t.Errorf("env JAB_STATIC_ROOT must be set for the patch to place STATIC_ROOT")
	}
	if repo.marked != app.ID {
		t.Errorf("app not marked scaffolded")
	}
}
