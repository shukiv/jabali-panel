package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/gitdeploy"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type gitTestDeployments struct {
	repository.DomainGitDeploymentRepository
	runs     map[string]*models.DomainGitDeployment
	docRoots []string
}

func (f *gitTestDeployments) CreateUnlessRunning(_ context.Context, run *models.DomainGitDeployment) error {
	for _, r := range f.runs {
		if r.DomainID == run.DomainID && r.State == models.GitDeployRunning {
			return repository.ErrGitDeploymentActive
		}
	}
	cp := *run
	f.runs[run.ID] = &cp
	return nil
}

func (f *gitTestDeployments) Save(_ context.Context, run *models.DomainGitDeployment) error {
	cp := *run
	f.runs[run.ID] = &cp
	return nil
}

func (f *gitTestDeployments) FindByID(_ context.Context, id string) (*models.DomainGitDeployment, error) {
	if r, ok := f.runs[id]; ok {
		cp := *r
		return &cp, nil
	}
	return nil, repository.ErrNotFound
}

func (f *gitTestDeployments) ListByDomain(_ context.Context, domainID, userID string, offset, limit int) ([]models.DomainGitDeployment, int64, error) {
	var out []models.DomainGitDeployment
	for _, r := range f.runs {
		if r.DomainID == domainID && r.UserID == userID {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	total := int64(len(out))
	if offset > len(out) {
		offset = len(out)
	}
	out = out[offset:]
	if len(out) > limit {
		out = out[:limit]
	}
	return out, total, nil
}

func (f *gitTestDeployments) SetDocRoot(_ context.Context, domainID, userID, docRoot string) (bool, error) {
	f.docRoots = append(f.docRoots, domainID+" "+userID+" "+docRoot)
	return true, nil
}

type gitTestSched struct{ scheduled []string }

func (s *gitTestSched) Schedule(domainID string) { s.scheduled = append(s.scheduled, domainID) }

// configure stores settings for example.com through the API.
func (e *gitTestEnv) configure(t *testing.T, body string) {
	t.Helper()
	if code, _, raw := e.do(t, http.MethodPut, "/domains/d1/git", body); code != http.StatusOK {
		t.Fatalf("PUT = %d %s", code, raw)
	}
}

// seed stores a finished run of example.com.
func (e *gitTestEnv) seed(id, userID, domainID, kind, state string) {
	now := time.Now().UTC()
	e.deploys.runs[id] = &models.DomainGitDeployment{
		ID: id, UserID: userID, DomainID: domainID, OSUser: "alice", Kind: kind, ReleaseID: id,
		Branch: "main", CommitSHA: "2f1d6a2c3b4e5f60718293a4b5c6d7e8f9a0b1c2", State: state,
		StartedAt: now, CreatedAt: now, UpdatedAt: now,
	}
}

// What a deployment does is saved with the repository, and a save that
// doesn't name it keeps it.
func TestDomainGit_SavesWhatADeploymentDoes(t *testing.T) {
	e := newGitTestEnv(t)
	e.configure(t, `{"repo_url":"https://gitlab.com/acme/shop.git","web_dir":"public",`+
		`"shared_paths":[".env","storage"],"deploy_commands":"php artisan migrate --force\r\n\r\n# cache\nphp artisan config:cache"}`)
	row := e.repos.rows["d1"]
	if row.WebDir != "public" || row.SharedPaths != ".env\nstorage" || row.DeployCommands != "php artisan migrate --force\nphp artisan config:cache" {
		t.Fatalf("stored = %+v", row)
	}
	e.configure(t, `{"repo_url":"https://gitlab.com/acme/shop.git","branch":"dev"}`)
	row = e.repos.rows["d1"]
	if row.Branch != "dev" || row.WebDir != "public" || row.SharedPaths != ".env\nstorage" || row.DeployCommands == "" {
		t.Fatalf("a save that didn't name them lost them: %+v", row)
	}
	code, out, _ := e.do(t, http.MethodGet, "/domains/d1/git", "")
	if code != http.StatusOK || out["web_dir"] != "public" || len(out["shared_paths"].([]any)) != 2 {
		t.Errorf("GET = %d %v", code, out)
	}
	// Named empty, they are cleared.
	e.configure(t, `{"repo_url":"https://gitlab.com/acme/shop.git","web_dir":"","shared_paths":[],"deploy_commands":""}`)
	if row = e.repos.rows["d1"]; row.WebDir != "" || row.SharedPaths != "" || row.DeployCommands != "" {
		t.Errorf("not cleared: %+v", row)
	}
}

func TestDomainGit_DeploySettingsRefusals(t *testing.T) {
	for name, body := range map[string]string{
		"web dir outside":           `"web_dir":"../public"`,
		"dotfile served":            `"shared_paths":[".env"]`,
		"dotfile in the web dir":    `"web_dir":"public","shared_paths":["public/.env"]`,
		"the web dir shared":        `"web_dir":"public","shared_paths":["public"]`,
		"a shell":                   `"deploy_commands":"sh -c id"`,
		"an inline php":             `"deploy_commands":"php -r 'system(1);'"`,
		"an absolute file":          `"deploy_commands":"php /etc/passwd"`,
		"a php option before files": `"deploy_commands":"php -n artisan"`,
	} {
		e := newGitTestEnv(t)
		code, out, raw := e.do(t, http.MethodPut, "/domains/d1/git", `{"repo_url":"https://gitlab.com/acme/shop.git",`+body+`}`)
		if code != http.StatusUnprocessableEntity || !strings.HasPrefix(out["error"].(string), "invalid_deploy_") {
			t.Errorf("%s: PUT = %d %s", name, code, raw)
		}
		if e.repos.puts != 0 || len(e.agent.calls) != 0 {
			t.Errorf("%s: stored %d, agent asked %v", name, e.repos.puts, e.agent.calls)
		}
	}
}

// A deployment fetches the stored branch with the stored token, as the
// domain's owner, and lands at domains/<domain>/current/<web dir>.
func TestDomainGit_DeployStartsWithTheStoredSettings(t *testing.T) {
	e := newGitTestEnv(t)
	e.configure(t, `{"repo_url":"https://gitlab.com/acme/shop.git","token":"`+gitTestToken+`","web_dir":"public",`+
		`"shared_paths":[".env"],"deploy_commands":"php artisan migrate --force"}`)
	e.agent.answers["git.deploy_start"] = gitdeploy.DeployStartResponse{Unit: "u", PHPVersion: "8.3"}
	e.hosts = nil
	e.pkg.ComposerEnabled = true

	code, out, raw := e.do(t, http.MethodPost, "/domains/d1/git/deployments", "")
	if code != http.StatusAccepted {
		t.Fatalf("POST = %d %s", code, raw)
	}
	if strings.Contains(raw, gitTestToken) {
		t.Fatalf("the answer carries the token: %s", raw)
	}
	if len(e.hosts) != 1 || e.hosts[0] != "gitlab.com" {
		t.Errorf("host checks = %v", e.hosts)
	}
	req := e.agent.params[len(e.agent.params)-1].(gitdeploy.DeployStartRequest)
	id := out["id"].(string)
	want := gitdeploy.DeployStartRequest{
		RunID: id, Kind: "deploy", OSUser: "alice", Domain: "example.com",
		RepoURL: "https://gitlab.com/acme/shop.git", Branch: "main", Token: gitTestToken,
		WebDir: "public", SharedPaths: []string{".env"}, Commands: "php artisan migrate --force", Composer: true,
	}
	if req.RunID != want.RunID || req.Kind != want.Kind || req.OSUser != want.OSUser || req.Domain != want.Domain ||
		req.RepoURL != want.RepoURL || req.Branch != want.Branch || req.Token != want.Token || req.WebDir != want.WebDir ||
		strings.Join(req.SharedPaths, ",") != ".env" || req.Commands != want.Commands || !req.Composer || req.Release != "" {
		t.Errorf("start = %+v", req)
	}
	run := e.deploys.runs[id]
	if run.ReleaseID != id || run.DocRoot != "/home/alice/domains/example.com/current/public" ||
		run.State != models.GitDeployRunning || run.PHPVersion != "8.3" || run.OSUser != "alice" {
		t.Errorf("run = %+v", run)
	}

	// One at a time.
	if code, out, _ := e.do(t, http.MethodPost, "/domains/d1/git/deployments", ""); code != http.StatusConflict || out["error"] != "git_deployment_active" {
		t.Errorf("second POST = %d %v", code, out)
	}
}

// Composer runs during a deployment only when the package allows them.
func TestDomainGit_DeployComposerFollowsThePackage(t *testing.T) {
	e := newGitTestEnv(t)
	e.configure(t, `{"repo_url":"https://gitlab.com/acme/shop.git"}`)
	e.agent.answers["git.deploy_start"] = gitdeploy.DeployStartResponse{Unit: "u"}
	if code, _, raw := e.do(t, http.MethodPost, "/domains/d1/git/deployments", ""); code != http.StatusAccepted {
		t.Fatalf("POST = %d %s", code, raw)
	}
	if req := e.agent.params[len(e.agent.params)-1].(gitdeploy.DeployStartRequest); req.Composer {
		t.Error("composer on a package without Composer runs")
	}
}

func TestDomainGit_DeployRefusals(t *testing.T) {
	e := newGitTestEnv(t)
	if code, out, _ := e.do(t, http.MethodPost, "/domains/d1/git/deployments", ""); code != http.StatusNotFound || out["error"] != "git_not_configured" {
		t.Errorf("not configured: %d %v", code, out)
	}
	e.configure(t, `{"repo_url":"https://gitlab.com/acme/shop.git"}`)
	e.hostErr = errors.New("the server won't connect to gitlab.com")
	if code, out, _ := e.do(t, http.MethodPost, "/domains/d1/git/deployments", ""); code != http.StatusUnprocessableEntity || out["error"] != "repo_host_not_allowed" {
		t.Errorf("host refused: %d %v", code, out)
	}
	e.hostErr = nil
	e.pkg.GitDeployEnabled = false
	if code, out, _ := e.do(t, http.MethodPost, "/domains/d1/git/deployments", ""); code != http.StatusForbidden || out["error"] != "git_deploy_not_in_package" {
		t.Errorf("package off: %d %v", code, out)
	}
	for _, c := range e.agent.calls {
		if c == "git.deploy_start" {
			t.Fatal("a refused deployment reached the agent")
		}
	}
	if len(e.deploys.runs) != 0 {
		t.Errorf("runs stored: %v", e.deploys.runs)
	}
}

// Only the release of a deployment of the domain's owner that succeeded can
// be made live again: never a release a run left half-built.
func TestDomainGit_RollbackOnlyToACompletedDeployment(t *testing.T) {
	e := newGitTestEnv(t)
	e.configure(t, `{"repo_url":"https://gitlab.com/acme/shop.git","web_dir":"public"}`)
	e.agent.answers["git.deploy_start"] = gitdeploy.DeployStartResponse{Unit: "u"}
	e.seed("01JBX000000000000000000001", "u-alice", "d1", models.GitDeployKindDeploy, models.GitDeploySucceeded)
	e.seed("01JBX000000000000000000002", "u-alice", "d1", models.GitDeployKindDeploy, models.GitDeployFailed)
	e.seed("01JBX000000000000000000003", "u-bob", "d2", models.GitDeployKindDeploy, models.GitDeploySucceeded)
	e.seed("01JBX000000000000000000004", "u-old", "d1", models.GitDeployKindDeploy, models.GitDeploySucceeded)
	e.seed("01JBX000000000000000000006", "u-alice", "d1", models.GitDeployKindRollback, models.GitDeploySucceeded)

	for release, want := range map[string]int{
		"01JBX000000000000000000002": http.StatusUnprocessableEntity, // failed
		"01JBX000000000000000000003": http.StatusNotFound,            // another domain's
		"01JBX000000000000000000004": http.StatusNotFound,            // the previous owner's
		"01JBX000000000000000000006": http.StatusUnprocessableEntity, // a rollback, not a release
		"01JBX00000000000000000000X": http.StatusNotFound,
	} {
		if code, _, raw := e.do(t, http.MethodPost, "/domains/d1/git/releases/"+release+"/activate", ""); code != want {
			t.Errorf("activate %s = %d %s, want %d", release, code, raw, want)
		}
	}
	for _, c := range e.agent.calls {
		if c == "git.deploy_start" {
			t.Fatal("a refused rollback reached the agent")
		}
	}

	code, out, raw := e.do(t, http.MethodPost, "/domains/d1/git/releases/01JBX000000000000000000001/activate", "")
	if code != http.StatusAccepted {
		t.Fatalf("activate = %d %s", code, raw)
	}
	req := e.agent.params[len(e.agent.params)-1].(gitdeploy.DeployStartRequest)
	if req.Kind != "rollback" || req.Release != "01JBX000000000000000000001" || req.WebDir != "public" || req.Token != "" || req.RepoURL != "" {
		t.Errorf("start = %+v", req)
	}
	if out["kind"] != "rollback" || out["release_id"] != "01JBX000000000000000000001" || out["doc_root"] != "/home/alice/domains/example.com/current/public" {
		t.Errorf("run = %v", out)
	}
}

// A domain's new owner doesn't see the previous owner's runs.
func TestDomainGit_PreviousOwnersRunsAreHidden(t *testing.T) {
	e := newGitTestEnv(t)
	e.seed("01JBX000000000000000000004", "u-old", "d1", models.GitDeployKindDeploy, models.GitDeploySucceeded)
	if code, _, _ := e.do(t, http.MethodGet, "/domains/d1/git/deployments/01JBX000000000000000000004", ""); code != http.StatusNotFound {
		t.Errorf("GET = %d", code)
	}
	code, out, _ := e.do(t, http.MethodGet, "/domains/d1/git/deployments", "")
	if code != http.StatusOK || out["total"] != float64(0) {
		t.Errorf("list = %d %v", code, out)
	}
}

// Reading a run that finished stores it, moves the document root and has
// the vhost re-applied.
func TestDomainGit_ReadingAFinishedRunCompletesIt(t *testing.T) {
	e := newGitTestEnv(t)
	e.seed("01JBX000000000000000000001", "u-alice", "d1", models.GitDeployKindDeploy, models.GitDeployRunning)
	e.deploys.runs["01JBX000000000000000000001"].DocRoot = "/home/alice/domains/example.com/current/public"
	e.agent.answers["git.deploy_status"] = gitdeploy.DeployStatusResponse{
		State: "succeeded", Log: "jabali-git-deploy: live 01JBX000000000000000000001\n",
	}
	e.agent.answers["git.deploy_stop"] = gitdeploy.DeployStopResponse{}
	code, out, raw := e.do(t, http.MethodGet, "/domains/d1/git/deployments/01JBX000000000000000000001", "")
	if code != http.StatusOK || out["state"] != "succeeded" || !strings.Contains(out["log"].(string), "live") {
		t.Fatalf("GET = %d %s", code, raw)
	}
	if len(e.deploys.docRoots) != 1 || e.deploys.docRoots[0] != "d1 u-alice /home/alice/domains/example.com/current/public" {
		t.Errorf("document roots = %v", e.deploys.docRoots)
	}
	if len(e.sched.scheduled) != 1 || e.sched.scheduled[0] != "d1" {
		t.Errorf("scheduled = %v", e.sched.scheduled)
	}
}

func TestDomainGit_Releases(t *testing.T) {
	e := newGitTestEnv(t)
	e.seed("01JBX000000000000000000001", "u-alice", "d1", models.GitDeployKindDeploy, models.GitDeploySucceeded)
	e.seed("01JBX000000000000000000004", "u-old", "d1", models.GitDeployKindDeploy, models.GitDeploySucceeded)
	e.agent.answers["git.releases"] = gitdeploy.ReleasesResponse{
		Releases: []string{"01JBX000000000000000000004", "01JBX000000000000000000001"}, Live: "01JBX000000000000000000001",
	}
	code, out, raw := e.do(t, http.MethodGet, "/domains/d1/git/releases", "")
	if code != http.StatusOK {
		t.Fatalf("GET = %d %s", code, raw)
	}
	rels := out["releases"].([]any)
	first, second := rels[0].(map[string]any), rels[1].(map[string]any)
	if first["live"] != false || first["deployment"] != nil {
		t.Errorf("the previous owner's release = %v", first)
	}
	if second["live"] != true || second["deployment"].(map[string]any)["commit_sha"] == "" {
		t.Errorf("the live release = %v", second)
	}
	if req := e.agent.params[len(e.agent.params)-1].(gitdeploy.ReleasesRequest); req.OSUser != "alice" || req.Domain != "example.com" {
		t.Errorf("releases request = %+v", req)
	}
}
