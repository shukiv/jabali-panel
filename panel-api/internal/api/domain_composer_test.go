package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/composerrun"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type composerTestDomains struct {
	repository.DomainRepository
	byID map[string]*models.Domain
}

func (f *composerTestDomains) FindByID(_ context.Context, id string) (*models.Domain, error) {
	if d, ok := f.byID[id]; ok {
		return d, nil
	}
	return nil, repository.ErrNotFound
}

type composerTestUsers struct {
	repository.UserRepository
	byID map[string]*models.User
}

func (f *composerTestUsers) FindByID(_ context.Context, id string) (*models.User, error) {
	if u, ok := f.byID[id]; ok {
		return u, nil
	}
	return nil, repository.ErrNotFound
}

type composerTestPackages struct {
	repository.PackageRepository
	byID map[string]*models.HostingPackage
}

func (f *composerTestPackages) FindByID(_ context.Context, id string) (*models.HostingPackage, error) {
	if p, ok := f.byID[id]; ok {
		return p, nil
	}
	return nil, repository.ErrNotFound
}

type composerTestRuns struct {
	repository.ComposerRunRepository
	runs map[string]*models.ComposerRun
}

// CreateUnlessRunning follows the repository: one run in progress per domain,
// and per folder among the user's domains.
func (f *composerTestRuns) CreateUnlessRunning(_ context.Context, run *models.ComposerRun) error {
	for _, r := range f.runs {
		if r.UserID == run.UserID && r.State == models.ComposerRunRunning &&
			(r.DomainID == run.DomainID || r.WorkDir == run.WorkDir) {
			return repository.ErrComposerRunActive
		}
	}
	cp := *run
	f.runs[run.ID] = &cp
	return nil
}

func (f *composerTestRuns) Save(_ context.Context, run *models.ComposerRun) error {
	cp := *run
	f.runs[run.ID] = &cp
	return nil
}

func (f *composerTestRuns) FindByID(_ context.Context, id string) (*models.ComposerRun, error) {
	if r, ok := f.runs[id]; ok {
		cp := *r
		return &cp, nil
	}
	return nil, repository.ErrNotFound
}

func (f *composerTestRuns) ListByDomain(_ context.Context, domainID string, _, _ int) ([]models.ComposerRun, int64, error) {
	out := []models.ComposerRun{}
	for _, r := range f.runs {
		if r.DomainID == domainID {
			out = append(out, *r)
		}
	}
	return out, int64(len(out)), nil
}

type composerTestProjectDirs struct {
	byDomain map[string]string
}

func (f *composerTestProjectDirs) Get(_ context.Context, domainID string) (*models.DomainComposerSettings, error) {
	if dir, ok := f.byDomain[domainID]; ok {
		return &models.DomainComposerSettings{DomainID: domainID, ProjectDir: dir}, nil
	}
	return nil, repository.ErrNotFound
}

func (f *composerTestProjectDirs) Put(_ context.Context, row *models.DomainComposerSettings) error {
	f.byDomain[row.DomainID] = row.ProjectDir
	return nil
}

func (f *composerTestProjectDirs) Delete(_ context.Context, domainID string) error {
	delete(f.byDomain, domainID)
	return nil
}

type composerTestAgent struct {
	calls   []string
	params  []any
	answers map[string]any
	errs    map[string]error
}

func (f *composerTestAgent) Call(_ context.Context, command string, params any) (json.RawMessage, error) {
	f.calls = append(f.calls, command)
	f.params = append(f.params, params)
	if err := f.errs[command]; err != nil {
		return nil, err
	}
	return json.Marshal(f.answers[command])
}

// composerTestEnv: alice owns example.com and shop.com (on package "pkg"),
// bob owns other.com (no package).
type composerTestEnv struct {
	router   *gin.Engine
	agent    *composerTestAgent
	runs     *composerTestRuns
	settings *composerTestProjectDirs
	pkg      *models.HostingPackage
	alice    *models.User
	callerID string
	admin    bool
}

func newComposerTestEnv(t *testing.T) *composerTestEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	alice, bob := "alice", "bob"
	pkgID := "pkg"
	env := &composerTestEnv{
		agent: &composerTestAgent{answers: map[string]any{
			"composer.detect":     composerrun.DetectResponse{Dirs: []composerrun.DetectDir{{Dir: "", HasLock: true}}, PHPVersion: "8.4"},
			"composer.run_start":  composerrun.StartResponse{Unit: "u", PHPVersion: "8.4"},
			"composer.run_status": composerrun.StatusResponse{State: "running", Log: "Loading composer repositories"},
			"composer.run_stop":   composerrun.StopResponse{},
		}},
		runs:     &composerTestRuns{runs: map[string]*models.ComposerRun{}},
		settings: &composerTestProjectDirs{byDomain: map[string]string{}},
		pkg:      &models.HostingPackage{ID: pkgID, ComposerEnabled: true},
		alice:    &models.User{ID: "u-alice", Username: &alice, PackageID: &pkgID},
		callerID: "u-alice",
	}
	domains := &composerTestDomains{byID: map[string]*models.Domain{
		"d-example": {ID: "d-example", UserID: "u-alice", Name: "example.com", DocRoot: "/home/alice/domains/example.com/public_html"},
		"d-shop":    {ID: "d-shop", UserID: "u-alice", Name: "shop.com", DocRoot: "/home/alice/domains/shop.com/public_html"},
		"d-other":   {ID: "d-other", UserID: "u-bob", Name: "other.com", DocRoot: "/home/bob/domains/other.com/public_html"},
	}}
	users := &composerTestUsers{byID: map[string]*models.User{
		"u-alice": env.alice,
		"u-bob":   {ID: "u-bob", Username: &bob},
	}}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: env.callerID, IsAdmin: env.admin})
		c.Next()
	})
	RegisterDomainComposerRoutes(r.Group(""), DomainComposerHandlerConfig{
		Domains: domains, Users: users,
		Packages: &composerTestPackages{byID: map[string]*models.HostingPackage{pkgID: env.pkg}},
		Runs:     env.runs, Settings: env.settings, Agent: env.agent,
	})
	env.router = r
	return env
}

func (e *composerTestEnv) do(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func (e *composerTestEnv) started() int {
	n := 0
	for _, c := range e.agent.calls {
		if c == "composer.run_start" {
			n++
		}
	}
	return n
}

func TestComposerRoutes_StartARun(t *testing.T) {
	env := newComposerTestEnv(t)
	code, body := env.do(t, http.MethodPost, "/domains/d-example/composer/runs", `{"command":"install","no_dev":true}`)
	if code != http.StatusAccepted || body["state"] != "running" || body["command"] != "install" {
		t.Fatalf("start = %d %v", code, body)
	}
	if env.started() != 1 {
		t.Fatalf("agent calls = %q", env.agent.calls)
	}
	req := env.agent.params[0].(composerrun.StartRequest)
	if req.OSUser != "alice" || req.Domain != "example.com" || !req.NoDev || req.RunID != body["id"] ||
		req.WorkDir != "domains/example.com" || body["work_dir"] != "domains/example.com" {
		t.Fatalf("run_start params = %+v, run = %v", req, body)
	}

	// One run at a time per domain.
	code, body = env.do(t, http.MethodPost, "/domains/d-example/composer/runs", `{"command":"update"}`)
	if code != http.StatusConflict || body["error"] != "composer_run_active" {
		t.Fatalf("second start = %d %v", code, body)
	}
	if env.started() != 1 {
		t.Fatalf("a second run reached the agent: %q", env.agent.calls)
	}

	// The run in progress shows on the domain's Composer status, with
	// where its composer.json is.
	code, body = env.do(t, http.MethodGet, "/domains/d-example/composer", "")
	if code != http.StatusOK || body["enabled"] != true || body["active_run"] == nil || body["detect"] == nil {
		t.Fatalf("status = %d %v", code, body)
	}
}

// Composer runs a user's composer.json scripts with the exec functions
// allowed, so only a package that includes it may start one. A user with no
// package can't (GH #282: a privileged feature).
func TestComposerRoutes_StartNeedsThePackage(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(e *composerTestEnv)
		want  string
	}{
		{"package without Composer", func(e *composerTestEnv) { e.pkg.ComposerEnabled = false }, "composer_not_in_package"},
		{"no package", func(e *composerTestEnv) { e.alice.PackageID = nil }, "composer_requires_package"},
		{"suspended", func(e *composerTestEnv) { e.alice.Suspended = true }, "account_suspended"},
		{"no Linux account", func(e *composerTestEnv) { e.alice.Username = nil }, "no_account"},
		{"admin, owner's package without Composer", func(e *composerTestEnv) { e.pkg.ComposerEnabled = false; e.admin = true; e.callerID = "u-admin" }, "composer_not_in_package"},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := newComposerTestEnv(t)
			c.setup(env)
			code, body := env.do(t, http.MethodPost, "/domains/d-example/composer/runs", `{"command":"install"}`)
			if code != http.StatusForbidden || body["error"] != c.want {
				t.Fatalf("start = %d %v, want 403 %s", code, body, c.want)
			}
			if len(env.agent.calls) != 0 {
				t.Fatalf("agent calls = %q", env.agent.calls)
			}
			code, body = env.do(t, http.MethodGet, "/domains/d-example/composer", "")
			if code != http.StatusOK || body["enabled"] != false || body["reason"] != c.want || body["detect"] != nil {
				t.Fatalf("status = %d %v", code, body)
			}
		})
	}
}

func TestComposerRoutes_OnlyTheOwnerOrAnAdmin(t *testing.T) {
	env := newComposerTestEnv(t)
	for _, req := range [][2]string{
		{http.MethodGet, "/domains/d-other/composer"},
		{http.MethodGet, "/domains/d-other/composer/runs"},
		{http.MethodPost, "/domains/d-other/composer/runs"},
	} {
		if code, _ := env.do(t, req[0], req[1], `{"command":"install"}`); code != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403", req[0], req[1], code)
		}
	}
	if code, _ := env.do(t, http.MethodGet, "/domains/nope/composer", ""); code != http.StatusNotFound {
		t.Errorf("unknown domain = %d", code)
	}
	if len(env.agent.calls) != 0 {
		t.Fatalf("agent calls = %q", env.agent.calls)
	}

	env.admin, env.callerID = true, "u-admin"
	if code, body := env.do(t, http.MethodPost, "/domains/d-example/composer/runs", `{"command":"install"}`); code != http.StatusAccepted {
		t.Fatalf("admin start = %d %v", code, body)
	}
}

// A run is checked before it reaches the agent.
func TestComposerRoutes_RefusesABadRun(t *testing.T) {
	env := newComposerTestEnv(t)
	for _, body := range []string{
		`{"command":"exec"}`,
		`{"command":"require"}`,
		`{"command":"require","packages":["--dev"]}`,
		`{"command":"install","app_dir":"../../etc"}`,
		`{"command":"validate","no_dev":true}`,
	} {
		code, resp := env.do(t, http.MethodPost, "/domains/d-example/composer/runs", body)
		if code != http.StatusUnprocessableEntity || resp["error"] != "invalid_run" {
			t.Errorf("%s = %d %v, want 422 invalid_run", body, code, resp)
		}
	}
	if code, _ := env.do(t, http.MethodPost, "/domains/d-example/composer/runs", `{`); code != http.StatusBadRequest {
		t.Errorf("malformed JSON = %d", code)
	}
	if len(env.agent.calls) != 0 {
		t.Fatalf("agent calls = %q", env.agent.calls)
	}
}

// What the agent says the domain lacks reaches the user; what went wrong
// inside the server stays in its log (JAB-114).
func TestComposerRoutes_AgentFailures(t *testing.T) {
	env := newComposerTestEnv(t)
	env.agent.errs = map[string]error{"composer.run_start": &agent.AgentError{Code: agent.CodeFailedPrecondition, Message: "example.com does not run PHP: enable PHP on the domain first"}}
	code, body := env.do(t, http.MethodPost, "/domains/d-example/composer/runs", `{"command":"install"}`)
	if code != http.StatusUnprocessableEntity || !strings.Contains(body["detail"].(string), "does not run PHP") {
		t.Fatalf("refused start = %d %v", code, body)
	}

	env = newComposerTestEnv(t)
	env.agent.errs = map[string]error{"composer.run_start": &agent.AgentError{Code: agent.CodeInternal, Message: "systemd-run: exit status 1: /run/systemd/secret-path"}}
	code, body = env.do(t, http.MethodPost, "/domains/d-example/composer/runs", `{"command":"install"}`)
	raw, _ := json.Marshal(body)
	if code != http.StatusInternalServerError || strings.Contains(string(raw), "systemd") {
		t.Fatalf("internal failure = %d %s", code, raw)
	}
}

func TestComposerRoutes_ReadAndCancelARun(t *testing.T) {
	env := newComposerTestEnv(t)
	_, started := env.do(t, http.MethodPost, "/domains/d-example/composer/runs", `{"command":"update"}`)
	id := started["id"].(string)

	code, body := env.do(t, http.MethodGet, "/domains/d-example/composer/runs/"+id, "")
	if code != http.StatusOK || body["log"] != "Loading composer repositories" || body["state"] != "running" {
		t.Fatalf("get run = %d %v", code, body)
	}
	// A run is read through its own domain only.
	env.admin, env.callerID = true, "u-admin"
	if code, _ := env.do(t, http.MethodGet, "/domains/d-other/composer/runs/"+id, ""); code != http.StatusNotFound {
		t.Fatalf("run read through another domain = %d, want 404", code)
	}
	env.admin, env.callerID = false, "u-alice"

	code, body = env.do(t, http.MethodPost, "/domains/d-example/composer/runs/"+id+"/cancel", "")
	if code != http.StatusOK || body["state"] != models.ComposerRunCancelled {
		t.Fatalf("cancel = %d %v", code, body)
	}
	if code, body = env.do(t, http.MethodPost, "/domains/d-example/composer/runs/"+id+"/cancel", ""); code != http.StatusConflict || body["error"] != "composer_run_finished" {
		t.Fatalf("second cancel = %d %v", code, body)
	}

	// A finished run is no longer the domain's run in progress.
	if _, body = env.do(t, http.MethodGet, "/domains/d-example/composer", ""); body["active_run"] != nil {
		t.Fatalf("status after cancel = %v", body)
	}
	code, body = env.do(t, http.MethodGet, "/domains/d-example/composer/runs", "")
	if code != http.StatusOK || body["total"] != float64(1) {
		t.Fatalf("list = %d %v", code, body)
	}
}

// GH #2276: a domain can name its own project folder, outside its directory
// too, e.g. one composer.json and vendor/ in ~/domains/ shared by several
// domains. Detection then looks only there, and runs run there.
func TestComposerRoutes_ProjectFolder(t *testing.T) {
	env := newComposerTestEnv(t)
	if _, body := env.do(t, http.MethodGet, "/domains/d-example/composer", ""); body["project_dir"] != nil {
		t.Fatalf("a new domain has a project folder: %v", body)
	}
	if req := env.agent.params[0].(composerrun.DetectRequest); req.ProjectDir != nil {
		t.Fatalf("detect without a folder = %+v", req)
	}

	code, body := env.do(t, http.MethodPut, "/domains/d-example/composer/settings", `{"project_dir":"domains/"}`)
	if code != http.StatusOK || body["project_dir"] != "domains" {
		t.Fatalf("set = %d %v", code, body)
	}
	env.agent.calls, env.agent.params = nil, nil
	if _, body = env.do(t, http.MethodGet, "/domains/d-example/composer", ""); body["project_dir"] != "domains" {
		t.Fatalf("status = %v", body)
	}
	if req := env.agent.params[0].(composerrun.DetectRequest); req.ProjectDir == nil || *req.ProjectDir != "domains" {
		t.Fatalf("detect = %+v, want the project folder", req)
	}

	code, body = env.do(t, http.MethodPost, "/domains/d-example/composer/runs", `{"command":"install"}`)
	if code != http.StatusAccepted || body["work_dir"] != "domains" || body["app_dir"] != "" {
		t.Fatalf("start = %d %v", code, body)
	}
	if req := env.agent.params[len(env.agent.params)-1].(composerrun.StartRequest); req.WorkDir != "domains" {
		t.Fatalf("run_start = %+v, want work_dir domains", req)
	}

	// The folder is the domain's, not one picked per run.
	if code, body = env.do(t, http.MethodPost, "/domains/d-shop/composer/settings", ""); code != http.StatusNotFound {
		t.Fatalf("POST settings = %d", code)
	}
	env.settings.byDomain["d-shop"] = "domains/shared"
	if code, body = env.do(t, http.MethodPost, "/domains/d-shop/composer/runs", `{"command":"install","app_dir":"app"}`); code != http.StatusUnprocessableEntity || body["error"] != "invalid_run" {
		t.Fatalf("app_dir with a project folder = %d %v", code, body)
	}

	// Cleared, the domain finds composer.json on its own again.
	code, body = env.do(t, http.MethodPut, "/domains/d-shop/composer/settings", `{"project_dir":null}`)
	if code != http.StatusOK || body["project_dir"] != nil {
		t.Fatalf("clear = %d %v", code, body)
	}
	if _, ok := env.settings.byDomain["d-shop"]; ok {
		t.Fatal("the folder is still stored")
	}
	code, body = env.do(t, http.MethodPost, "/domains/d-shop/composer/runs", `{"command":"install","app_dir":"app"}`)
	if code != http.StatusAccepted || body["work_dir"] != "domains/shop.com/app" {
		t.Fatalf("start after clearing = %d %v", code, body)
	}
}

// Two domains sharing a project folder can't run Composer in it at once;
// domains in different folders still can.
func TestComposerRoutes_OneRunPerProjectFolder(t *testing.T) {
	env := newComposerTestEnv(t)
	env.settings.byDomain["d-example"] = "domains"
	env.settings.byDomain["d-shop"] = "domains"
	if code, body := env.do(t, http.MethodPost, "/domains/d-example/composer/runs", `{"command":"install"}`); code != http.StatusAccepted {
		t.Fatalf("first = %d %v", code, body)
	}
	code, body := env.do(t, http.MethodPost, "/domains/d-shop/composer/runs", `{"command":"update"}`)
	if code != http.StatusConflict || body["error"] != "composer_run_active" || !strings.Contains(body["detail"].(string), "domains/") {
		t.Fatalf("second domain, same folder = %d %v", code, body)
	}
	if env.started() != 1 {
		t.Fatalf("a second run reached the agent: %q", env.agent.calls)
	}

	env.settings.byDomain["d-shop"] = "domains/shop"
	if code, body = env.do(t, http.MethodPost, "/domains/d-shop/composer/runs", `{"command":"install"}`); code != http.StatusAccepted {
		t.Fatalf("second domain, own folder = %d %v", code, body)
	}
}

func TestComposerRoutes_RefusesABadProjectFolder(t *testing.T) {
	env := newComposerTestEnv(t)
	for _, dir := range []string{`""`, `"."`, `".ssh"`, `"/etc"`, `"/home/alice/domains"`, `"public_html"`, `"domains/../.ssh"`, `"domains/a b"`, `"domainsx"`, `"domains//x"`} {
		code, body := env.do(t, http.MethodPut, "/domains/d-example/composer/settings", `{"project_dir":`+dir+`}`)
		if code != http.StatusUnprocessableEntity || body["error"] != "invalid_project_dir" {
			t.Errorf("%s = %d %v, want 422 invalid_project_dir", dir, code, body)
		}
	}
	if len(env.settings.byDomain) != 0 {
		t.Fatalf("stored = %v", env.settings.byDomain)
	}

	// Only the owner or an admin, and only with Composer in the package.
	if code, _ := env.do(t, http.MethodPut, "/domains/d-other/composer/settings", `{"project_dir":"domains"}`); code != http.StatusForbidden {
		t.Errorf("another user's domain = %d, want 403", code)
	}
	env.pkg.ComposerEnabled = false
	if code, body := env.do(t, http.MethodPut, "/domains/d-example/composer/settings", `{"project_dir":"domains"}`); code != http.StatusForbidden || body["error"] != "composer_not_in_package" {
		t.Errorf("package without Composer = %d %v", code, body)
	}
	if len(env.settings.byDomain) != 0 {
		t.Fatalf("stored = %v", env.settings.byDomain)
	}
}
