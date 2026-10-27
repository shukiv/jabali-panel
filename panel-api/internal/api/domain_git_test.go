package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/gitdeploy"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// gitTestToken is a made-up token.
const gitTestToken = "git-token-abc_DEF-123"

type gitTestRepos struct {
	repository.DomainGitRepoRepository
	rows map[string]*models.DomainGitRepo
	puts int
}

func (f *gitTestRepos) Get(_ context.Context, domainID string) (*models.DomainGitRepo, error) {
	if r, ok := f.rows[domainID]; ok {
		cp := *r
		return &cp, nil
	}
	return nil, repository.ErrNotFound
}

func (f *gitTestRepos) Put(_ context.Context, row *models.DomainGitRepo) error {
	cp := *row
	f.rows[row.DomainID] = &cp
	f.puts++
	return nil
}

func (f *gitTestRepos) Delete(_ context.Context, domainID string) error {
	if _, ok := f.rows[domainID]; !ok {
		return repository.ErrNotFound
	}
	delete(f.rows, domainID)
	return nil
}

// gitTestEnv: alice owns example.com on package "pkg" (Git deploy on); bob
// owns other.com with no package.
type gitTestEnv struct {
	router  *gin.Engine
	agent   *composerTestAgent
	repos   *gitTestRepos
	pkg     *models.HostingPackage
	alice   *models.User
	key     *ssokey.Key
	hosts   []string
	hostErr error
	claims  *auth.AccessClaims
	// deploys and sched back the deployment routes (domain_git_deploy_test.go).
	deploys *gitTestDeployments
	sched   *gitTestSched
}

func newGitTestEnv(t *testing.T) *gitTestEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	aliceName, bobName, pkgID := "alice", "bob", "pkg"
	key := ssokey.Key{1, 2, 3}
	e := &gitTestEnv{
		agent: &composerTestAgent{answers: map[string]any{
			"git.ls_remote": gitdeploy.LsRemoteResponse{DefaultBranch: "main", Branches: []string{"dev", "main"}},
		}, errs: map[string]error{}},
		repos:   &gitTestRepos{rows: map[string]*models.DomainGitRepo{}},
		pkg:     &models.HostingPackage{ID: pkgID, GitDeployEnabled: true},
		alice:   &models.User{ID: "u-alice", Username: &aliceName, PackageID: &pkgID},
		key:     &key,
		claims:  &auth.AccessClaims{UserID: "u-alice"},
		deploys: &gitTestDeployments{runs: map[string]*models.DomainGitDeployment{}},
		sched:   &gitTestSched{},
	}
	bob := &models.User{ID: "u-bob", Username: &bobName}
	r := gin.New()
	r.Use(func(c *gin.Context) { ginctx.SetClaims(c, e.claims); c.Next() })
	RegisterDomainGitRoutes(r.Group(""), DomainGitHandlerConfig{
		Domains: &composerTestDomains{byID: map[string]*models.Domain{
			"d1": {ID: "d1", UserID: "u-alice", Name: "example.com"},
			"d2": {ID: "d2", UserID: "u-bob", Name: "other.com"},
		}},
		Users:    &composerTestUsers{byID: map[string]*models.User{"u-alice": e.alice, "u-bob": bob}},
		Packages: &composerTestPackages{byID: map[string]*models.HostingPackage{pkgID: e.pkg}},
		Repos:    e.repos,
		Agent:    e.agent,
		SSOKey:   e.key,
		CheckHost: func(_ context.Context, host string) error {
			e.hosts = append(e.hosts, host)
			return e.hostErr
		},
		Deployments: e.deploys,
		Reconciler:  e.sched,
	})
	e.router = r
	return e
}

func (e *gitTestEnv) do(t *testing.T, method, path, body string) (int, map[string]any, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out, w.Body.String()
}

// lastLsRemote is the request of the agent's last git.ls_remote call.
func (e *gitTestEnv) lastLsRemote(t *testing.T) gitdeploy.LsRemoteRequest {
	t.Helper()
	if len(e.agent.params) == 0 {
		t.Fatal("the agent was not asked")
	}
	return e.agent.params[len(e.agent.params)-1].(gitdeploy.LsRemoteRequest)
}

// Saving reads the branches as the domain's owner with the given token,
// stores the token sealed, and never answers with it.
func TestDomainGit_SaveSealsTheTokenAndNeverReturnsIt(t *testing.T) {
	e := newGitTestEnv(t)
	code, out, raw := e.do(t, http.MethodPut, "/domains/d1/git",
		`{"repo_url":"https://gitlab.com/acme/shop.git","token":"`+gitTestToken+`"}`)
	if code != http.StatusOK {
		t.Fatalf("PUT = %d %s", code, raw)
	}
	if strings.Contains(raw, gitTestToken) {
		t.Fatalf("the answer carries the token: %s", raw)
	}
	if out["has_token"] != true || out["branch"] != "main" || out["configured"] != true {
		t.Errorf("answer = %v", out)
	}
	got := e.lastLsRemote(t)
	if got.OSUser != "alice" || got.RepoURL != "https://gitlab.com/acme/shop.git" || got.Token != gitTestToken || got.Username != "" {
		t.Errorf("ls_remote request = %+v", got)
	}
	row := e.repos.rows["d1"]
	if row == nil || strings.Contains(string(row.TokenEnc), gitTestToken) {
		t.Fatalf("stored row = %+v", row)
	}
	if plain, err := e.key.Open(row.TokenEnc); err != nil || string(plain) != gitTestToken {
		t.Errorf("stored token opens to %q, %v", plain, err)
	}
	if e.hosts[0] != "gitlab.com" {
		t.Errorf("host checked = %q", e.hosts)
	}

	code, _, raw = e.do(t, http.MethodGet, "/domains/d1/git", "")
	if code != http.StatusOK || strings.Contains(raw, gitTestToken) || strings.Contains(raw, "token_enc") {
		t.Errorf("GET = %d %s", code, raw)
	}
}

// Changing the branch or username on the same host keeps the stored token.
func TestDomainGit_SameHostKeepsTheToken(t *testing.T) {
	e := newGitTestEnv(t)
	if code, _, raw := e.do(t, http.MethodPut, "/domains/d1/git", `{"repo_url":"https://gitlab.com/acme/shop.git","token":"`+gitTestToken+`"}`); code != http.StatusOK {
		t.Fatalf("first PUT = %d %s", code, raw)
	}
	sealed := e.repos.rows["d1"].TokenEnc
	code, out, raw := e.do(t, http.MethodPut, "/domains/d1/git", `{"repo_url":"https://gitlab.com/acme/other.git","branch":"dev","username":"deployer"}`)
	if code != http.StatusOK {
		t.Fatalf("second PUT = %d %s", code, raw)
	}
	if got := e.lastLsRemote(t); got.Token != gitTestToken || got.Username != "deployer" {
		t.Errorf("ls_remote request = %+v, want the stored token", got)
	}
	row := e.repos.rows["d1"]
	if string(row.TokenEnc) != string(sealed) || row.Branch != "dev" || out["has_token"] != true {
		t.Errorf("stored = %+v, answer = %v", row, out)
	}
}

// A stored token never goes to another host: a new host needs the token
// again (or clear_token), and nothing reaches the agent or the store before.
func TestDomainGit_NewHostNeedsTheTokenAgain(t *testing.T) {
	e := newGitTestEnv(t)
	if code, _, raw := e.do(t, http.MethodPut, "/domains/d1/git", `{"repo_url":"https://gitlab.com/acme/shop.git","token":"`+gitTestToken+`"}`); code != http.StatusOK {
		t.Fatalf("first PUT = %d %s", code, raw)
	}
	calls, puts := len(e.agent.calls), e.repos.puts
	for _, url := range []string{"https://attacker.example/acme/shop.git", "https://gitlab.com:8443/acme/shop.git"} {
		code, out, _ := e.do(t, http.MethodPut, "/domains/d1/git", `{"repo_url":"`+url+`"}`)
		if code != http.StatusUnprocessableEntity || out["error"] != "token_required_for_new_host" {
			t.Errorf("%s: PUT = %d %v", url, code, out)
		}
	}
	if len(e.agent.calls) != calls || e.repos.puts != puts {
		t.Fatalf("a refused save reached the agent (%d calls) or the store (%d puts)", len(e.agent.calls)-calls, e.repos.puts-puts)
	}

	// clear_token: the new host is read without any token.
	code, out, raw := e.do(t, http.MethodPut, "/domains/d1/git", `{"repo_url":"https://github.com/acme/shop.git","clear_token":true}`)
	if code != http.StatusOK || out["has_token"] != false {
		t.Fatalf("PUT clear_token = %d %s", code, raw)
	}
	if got := e.lastLsRemote(t); got.Token != "" {
		t.Errorf("clear_token sent a token: %+v", got)
	}
	if e.repos.rows["d1"].TokenEnc != nil {
		t.Error("clear_token left the token stored")
	}
}

func TestDomainGit_Refusals(t *testing.T) {
	for _, c := range []struct {
		name, body, code string
	}{
		{"token in the URL", `{"repo_url":"https://tok@github.com/a/b.git"}`, "invalid_repo_url"},
		{"http", `{"repo_url":"http://github.com/a/b.git"}`, "invalid_repo_url"},
		{"ssh", `{"repo_url":"git@github.com:a/b.git"}`, "invalid_repo_url"},
		{"percent", `{"repo_url":"https://github.com/a/b%2e.git"}`, "invalid_repo_url"},
		{"bad branch", `{"repo_url":"https://github.com/a/b.git","branch":"-x"}`, "invalid_branch"},
		{"bad username", `{"repo_url":"https://github.com/a/b.git","username":"a b"}`, "invalid_username"},
		{"token and clear", `{"repo_url":"https://github.com/a/b.git","token":"t","clear_token":true}`, "invalid_token"},
		{"token with space", `{"repo_url":"https://github.com/a/b.git","token":"a b"}`, "invalid_token"},
		{"unknown branch", `{"repo_url":"https://github.com/a/b.git","branch":"prod"}`, "branch_not_found"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newGitTestEnv(t)
			code, out, _ := e.do(t, http.MethodPut, "/domains/d1/git", c.body)
			if code != http.StatusUnprocessableEntity || out["error"] != c.code {
				t.Fatalf("PUT = %d %v, want 422 %s", code, out, c.code)
			}
			if len(e.repos.rows) != 0 {
				t.Error("a refused save was stored")
			}
		})
	}
}

// A host that resolves to a private or loopback address is refused before
// the agent connects to it.
func TestDomainGit_HostCheckComesFirst(t *testing.T) {
	e := newGitTestEnv(t)
	e.hostErr = errors.New("the server won't connect to git.internal")
	code, out, _ := e.do(t, http.MethodPut, "/domains/d1/git", `{"repo_url":"https://git.internal/a/b.git"}`)
	if code != http.StatusUnprocessableEntity || out["error"] != "repo_host_not_allowed" {
		t.Fatalf("PUT = %d %v", code, out)
	}
	if len(e.agent.calls) != 0 || len(e.repos.rows) != 0 {
		t.Errorf("agent calls %q, rows %d", e.agent.calls, len(e.repos.rows))
	}
}

// git's refusal reaches the user as 422 with its words; nothing is stored.
func TestDomainGit_RemoteFailureIsNotStored(t *testing.T) {
	e := newGitTestEnv(t)
	e.agent.errs["git.ls_remote"] = &agent.AgentError{Code: agent.CodeFailedPrecondition, Message: "the Git host wants a username and token, or refused the ones given"}
	code, out, _ := e.do(t, http.MethodPut, "/domains/d1/git", `{"repo_url":"https://github.com/a/b.git","token":"`+gitTestToken+`"}`)
	if code != http.StatusUnprocessableEntity || out["error"] != "git_remote_failed" || !strings.Contains(out["detail"].(string), "refused") {
		t.Fatalf("PUT = %d %v", code, out)
	}
	if len(e.repos.rows) != 0 {
		t.Error("settings that can't read the repository were stored")
	}
}

func TestDomainGit_Gate(t *testing.T) {
	e := newGitTestEnv(t)
	e.pkg.GitDeployEnabled = false
	code, out, _ := e.do(t, http.MethodPut, "/domains/d1/git", `{"repo_url":"https://github.com/a/b.git"}`)
	if code != http.StatusForbidden || out["error"] != "git_deploy_not_in_package" {
		t.Errorf("package without Git deploy: PUT = %d %v", code, out)
	}
	if code, out, _ := e.do(t, http.MethodGet, "/domains/d1/git", ""); code != http.StatusOK || out["enabled"] != false || out["reason"] != "git_deploy_not_in_package" {
		t.Errorf("GET = %d %v", code, out)
	}
	e.pkg.GitDeployEnabled = true
	e.alice.Suspended = true
	if code, out, _ := e.do(t, http.MethodGet, "/domains/d1/git/branches", ""); code != http.StatusForbidden || out["error"] != "account_suspended" {
		t.Errorf("suspended: branches = %d %v", code, out)
	}
	e.alice.Suspended = false

	// bob has no package; alice can't touch bob's domain; an admin can, but
	// bob's (lack of a) package still decides.
	e.claims = &auth.AccessClaims{UserID: "u-bob"}
	if code, out, _ := e.do(t, http.MethodPut, "/domains/d2/git", `{"repo_url":"https://github.com/a/b.git"}`); code != http.StatusForbidden || out["error"] != "git_deploy_requires_package" {
		t.Errorf("no package: PUT = %d %v", code, out)
	}
	e.claims = &auth.AccessClaims{UserID: "u-alice"}
	if code, _, _ := e.do(t, http.MethodGet, "/domains/d2/git", ""); code != http.StatusForbidden {
		t.Errorf("alice reading bob's domain = %d", code)
	}
	e.claims = &auth.AccessClaims{UserID: "admin", IsAdmin: true}
	if code, out, _ := e.do(t, http.MethodPut, "/domains/d2/git", `{"repo_url":"https://github.com/a/b.git"}`); code != http.StatusForbidden || out["error"] != "git_deploy_requires_package" {
		t.Errorf("admin on bob's domain: PUT = %d %v", code, out)
	}
	if len(e.agent.calls) != 0 {
		t.Errorf("a gated request reached the agent: %q", e.agent.calls)
	}
}

// Removing the settings needs no entitlement: a user whose package lost Git
// deploy can still delete the stored token.
func TestDomainGit_DeleteNeedsNoEntitlement(t *testing.T) {
	e := newGitTestEnv(t)
	if code, _, raw := e.do(t, http.MethodPut, "/domains/d1/git", `{"repo_url":"https://github.com/a/b.git","token":"`+gitTestToken+`"}`); code != http.StatusOK {
		t.Fatalf("PUT = %d %s", code, raw)
	}
	e.pkg.GitDeployEnabled = false
	if code, _, _ := e.do(t, http.MethodDelete, "/domains/d1/git", ""); code != http.StatusNoContent {
		t.Fatalf("DELETE = %d", code)
	}
	if len(e.repos.rows) != 0 {
		t.Error("DELETE left the settings")
	}
	if code, _, _ := e.do(t, http.MethodDelete, "/domains/d1/git", ""); code != http.StatusNoContent {
		t.Errorf("second DELETE = %d", code)
	}
}

func TestDomainGit_BranchesUseTheStoredSettings(t *testing.T) {
	e := newGitTestEnv(t)
	if code, out, _ := e.do(t, http.MethodGet, "/domains/d1/git/branches", ""); code != http.StatusNotFound || out["error"] != "git_not_configured" {
		t.Fatalf("branches before saving = %d %v", code, out)
	}
	if code, _, raw := e.do(t, http.MethodPut, "/domains/d1/git", `{"repo_url":"https://bitbucket.org/a/b.git","username":"x-token-auth","token":"`+gitTestToken+`"}`); code != http.StatusOK {
		t.Fatalf("PUT = %d %s", code, raw)
	}
	code, out, raw := e.do(t, http.MethodGet, "/domains/d1/git/branches", "")
	if code != http.StatusOK || out["default_branch"] != "main" || strings.Contains(raw, gitTestToken) {
		t.Fatalf("branches = %d %s", code, raw)
	}
	if got := e.lastLsRemote(t); got.Token != gitTestToken || got.Username != "x-token-auth" || got.RepoURL != "https://bitbucket.org/a/b.git" {
		t.Errorf("ls_remote request = %+v", got)
	}
}

// A domain that changed owner (GH #1238) never hands the previous owner's
// settings or token to the new owner: the row is forgotten, and saving the
// same host again sends no token.
func TestDomainGit_PreviousOwnersSettingsAreForgotten(t *testing.T) {
	e := newGitTestEnv(t)
	sealed, err := e.key.Seal([]byte(gitTestToken))
	if err != nil {
		t.Fatal(err)
	}
	e.repos.rows["d1"] = &models.DomainGitRepo{
		DomainID: "d1", UserID: "u-previous", RepoURL: "https://gitlab.com/acme/shop.git", Branch: "main", TokenEnc: sealed,
	}
	if code, out, _ := e.do(t, http.MethodGet, "/domains/d1/git", ""); code != http.StatusOK || out["configured"] != false || out["has_token"] != false {
		t.Fatalf("GET = %d %v", code, out)
	}
	if _, ok := e.repos.rows["d1"]; ok {
		t.Fatal("the previous owner's settings are still stored")
	}

	e.repos.rows["d1"] = &models.DomainGitRepo{
		DomainID: "d1", UserID: "u-previous", RepoURL: "https://gitlab.com/acme/shop.git", Branch: "main", TokenEnc: sealed,
	}
	if code, out, _ := e.do(t, http.MethodGet, "/domains/d1/git/branches", ""); code != http.StatusNotFound || out["error"] != "git_not_configured" {
		t.Errorf("branches = %d %v", code, out)
	}
	e.repos.rows["d1"] = &models.DomainGitRepo{
		DomainID: "d1", UserID: "u-previous", RepoURL: "https://gitlab.com/acme/shop.git", Branch: "main", TokenEnc: sealed,
	}
	code, out, raw := e.do(t, http.MethodPut, "/domains/d1/git", `{"repo_url":"https://gitlab.com/acme/shop.git"}`)
	if code != http.StatusOK || out["has_token"] != false {
		t.Fatalf("PUT = %d %s", code, raw)
	}
	if got := e.lastLsRemote(t); got.Token != "" {
		t.Errorf("the previous owner's token went to the agent: %+v", got)
	}
	if row := e.repos.rows["d1"]; row.UserID != "u-alice" || row.TokenEnc != nil {
		t.Errorf("stored = %+v", row)
	}
}
