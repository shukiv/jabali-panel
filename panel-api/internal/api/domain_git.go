package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/gitdeploy"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// GH #2277 (ADR-0178): the Git repository a domain deploys from.
//
// The token is handled as a tenant's Cloudflare token is
// (user_cloudflare_token.go): sealed with the sso key, written only here,
// never returned or logged. Saving reads the repository's branches with the
// new settings first, so settings that can't read the repository are never
// stored. A stored token is bound to the repository's host: saving another
// host needs the token again, so nobody who can edit the settings can send
// the stored token somewhere else.

// DomainGitHandlerConfig wires the Git repository routes of a domain.
type DomainGitHandlerConfig struct {
	Domains  repository.DomainRepository
	Users    repository.UserRepository
	Packages repository.PackageRepository
	Repos    repository.DomainGitRepoRepository
	Agent    agent.AgentInterface
	SSOKey   *ssokey.Key
	// RemoteRateLimit bounds the routes that make the server connect to a
	// Git host. Wire from rl.StrictPerActor(); nil disables it.
	RemoteRateLimit gin.HandlerFunc
	// CheckHost refuses a repository host the server must not connect to.
	// nil uses gitdeploy.CheckHost; tests replace it.
	CheckHost func(ctx context.Context, hostname string) error
	// Deployments stores the domain's deployments and rollbacks.
	Deployments repository.DomainGitDeploymentRepository
	// Reconciler re-applies a domain's vhost once a deployment has moved its
	// document root. nil skips that (tests).
	Reconciler gitdeploy.Scheduler
}

// RegisterDomainGitRoutes adds the Git repository endpoints of a domain:
//   - GET    /domains/:id/git           may the owner use Git deploy, and the repository settings (never the token)
//   - PUT    /domains/:id/git           save the settings, after reading the repository's branches with them
//   - DELETE /domains/:id/git           forget the settings and the token
//   - GET    /domains/:id/git/branches  the repository's branches, read with the stored settings
//   - GET    /domains/:id/git/deployments                    the domain's deployments and rollbacks, newest first
//   - POST   /domains/:id/git/deployments                    deploy the stored branch
//   - GET    /domains/:id/git/deployments/:runId             one run, with its log
//   - POST   /domains/:id/git/deployments/:runId/cancel      end a run in progress
//   - GET    /domains/:id/git/releases                       the releases on the server, and the live one
//   - POST   /domains/:id/git/releases/:release/activate     roll back (or forward) to a release
//
// The domain's owner and admins may use them. Everything but reading and
// removing the settings and reading the runs needs the owner's hosting
// package to allow Git deploy (GH #282: a user with no package can't).
func RegisterDomainGitRoutes(g *gin.RouterGroup, cfg DomainGitHandlerConfig) {
	if cfg.Domains == nil || cfg.Users == nil || cfg.Packages == nil || cfg.Repos == nil || cfg.Agent == nil || cfg.Deployments == nil {
		panic("api.RegisterDomainGitRoutes: Domains, Users, Packages, Repos, Agent and Deployments are required")
	}
	if cfg.CheckHost == nil {
		cfg.CheckHost = gitdeploy.CheckHost
	}
	h := &domainGitHandler{cfg: cfg}
	limit := cfg.RemoteRateLimit
	if limit == nil {
		limit = func(c *gin.Context) { c.Next() }
	}
	g.GET("/domains/:id/git", h.get)
	g.PUT("/domains/:id/git", limit, h.put)
	g.DELETE("/domains/:id/git", h.delete)
	g.GET("/domains/:id/git/branches", limit, h.branches)
	g.GET("/domains/:id/git/deployments", h.listDeployments)
	g.POST("/domains/:id/git/deployments", limit, h.deploy)
	g.GET("/domains/:id/git/deployments/:runId", h.getDeployment)
	g.POST("/domains/:id/git/deployments/:runId/cancel", h.cancelDeployment)
	g.GET("/domains/:id/git/releases", h.releases)
	g.POST("/domains/:id/git/releases/:release/activate", limit, h.activate)
}

type domainGitHandler struct {
	cfg DomainGitHandlerConfig
}

// gitOwner is the domain a request is about and the user who owns it.
type gitOwner struct {
	domain *models.Domain
	user   *models.User
}

func (h *domainGitHandler) loadDomain(c *gin.Context) (*gitOwner, bool) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return nil, false
	}
	ctx := c.Request.Context()
	dom, err := h.cfg.Domains.FindByID(ctx, c.Param("id"))
	if errors.Is(err, repository.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
		return nil, false
	}
	if err != nil {
		slog.ErrorContext(ctx, "git: load domain", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, false
	}
	if !claims.IsAdmin && dom.UserID != claims.UserID {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return nil, false
	}
	owner, err := h.cfg.Users.FindByID(ctx, dom.UserID)
	if err != nil || owner == nil {
		slog.ErrorContext(ctx, "git: load the domain's owner", "domain", dom.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, false
	}
	c.Set("audit_target", "domain_git:"+dom.Name)
	return &gitOwner{domain: dom, user: owner}, true
}

// allowed returns the owner's hosting package when it allows Git deploy, and
// if it does not, why: the error code the API answers with.
func (h *domainGitHandler) allowed(ctx context.Context, o *gitOwner) (*models.HostingPackage, string) {
	if o.user.Username == nil || *o.user.Username == "" {
		return nil, "no_account"
	}
	if o.user.Suspended {
		return nil, "account_suspended"
	}
	if o.user.PackageID == nil {
		return nil, "git_deploy_requires_package"
	}
	pkg, err := h.cfg.Packages.FindByID(ctx, *o.user.PackageID)
	if err != nil || pkg == nil {
		return nil, "git_deploy_requires_package"
	}
	if !pkg.GitDeployEnabled {
		return nil, "git_deploy_not_in_package"
	}
	return pkg, ""
}

// requireAllowed answers the request itself when the owner may not use Git
// deploy.
func (h *domainGitHandler) requireAllowed(c *gin.Context, o *gitOwner) (*models.HostingPackage, bool) {
	pkg, reason := h.allowed(c.Request.Context(), o)
	if pkg == nil {
		c.JSON(http.StatusForbidden, gin.H{"error": reason})
		return nil, false
	}
	return pkg, true
}

// domainGitView is the settings on the wire. It never carries the token.
type domainGitView struct {
	// Enabled: the owner's hosting package allows Git deploy.
	Enabled bool `json:"enabled"`
	// Reason is why not: no_account, account_suspended,
	// git_deploy_requires_package or git_deploy_not_in_package.
	Reason     string     `json:"reason,omitempty"`
	Configured bool       `json:"configured"`
	RepoURL    string     `json:"repo_url,omitempty"`
	Branch     string     `json:"branch,omitempty"`
	Username   string     `json:"username,omitempty"`
	HasToken   bool       `json:"has_token"`
	VerifiedAt *time.Time `json:"verified_at,omitempty"`
	// WebDir is the directory the site serves, relative to the repository.
	WebDir string `json:"web_dir"`
	// SharedPaths are kept across releases in domains/<domain>/shared.
	SharedPaths []string `json:"shared_paths"`
	// DeployCommands run in a new release before it goes live, one per line.
	DeployCommands string `json:"deploy_commands"`
	// Remote is the repository's branches, after a save.
	Remote *gitdeploy.LsRemoteResponse `json:"remote,omitempty"`
	// ActiveDeployment is the run in progress, if any.
	ActiveDeployment *gitDeploymentDTO `json:"active_deployment"`
}

func newDomainGitView(row *models.DomainGitRepo) domainGitView {
	if row == nil {
		return domainGitView{SharedPaths: []string{}}
	}
	verified := row.VerifiedAt
	return domainGitView{
		Configured: true, RepoURL: row.RepoURL, Branch: row.Branch, Username: row.Username,
		HasToken: len(row.TokenEnc) > 0, VerifiedAt: &verified,
		WebDir: row.WebDir, SharedPaths: row.SharedPathList(), DeployCommands: row.DeployCommands,
	}
}

// stored loads the domain's settings; nil when it has none.
func (h *domainGitHandler) stored(c *gin.Context, o *gitOwner) (*models.DomainGitRepo, bool) {
	ctx := c.Request.Context()
	row, err := h.cfg.Repos.Get(ctx, o.domain.ID)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, true
	}
	if err != nil {
		slog.ErrorContext(ctx, "git: load the settings", "domain", o.domain.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, false
	}
	if row.UserID != o.domain.UserID {
		// The domain changed owner (GH #1238) and the settings, token
		// included, are the previous owner's. TransferOwner deletes them;
		// this covers a row left behind any other way.
		if derr := h.cfg.Repos.Delete(ctx, o.domain.ID); derr != nil && !errors.Is(derr, repository.ErrNotFound) {
			slog.ErrorContext(ctx, "git: delete the previous owner's settings", "domain", o.domain.ID, "error", derr)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return nil, false
		}
		return nil, true
	}
	return row, true
}

func (h *domainGitHandler) get(c *gin.Context) {
	o, ok := h.loadDomain(c)
	if !ok {
		return
	}
	row, ok := h.stored(c, o)
	if !ok {
		return
	}
	view := newDomainGitView(row)
	pkg, reason := h.allowed(c.Request.Context(), o)
	view.Enabled, view.Reason = pkg != nil, reason
	active, ok := h.activeDeployment(c, o)
	if !ok {
		return
	}
	view.ActiveDeployment = active
	c.JSON(http.StatusOK, view)
}

type domainGitPutRequest struct {
	RepoURL  string `json:"repo_url"`
	Branch   string `json:"branch"`
	Username string `json:"username"`
	// Token replaces the stored token. Absent or "" keeps it, as long as the
	// repository stays on the same host.
	Token string `json:"token"`
	// ClearToken removes the stored token: the repository is public.
	ClearToken bool `json:"clear_token"`
	// WebDir, SharedPaths and DeployCommands: absent keeps the stored ones.
	WebDir         *string   `json:"web_dir"`
	SharedPaths    *[]string `json:"shared_paths"`
	DeployCommands *string   `json:"deploy_commands"`
}

func (h *domainGitHandler) put(c *gin.Context) {
	o, ok := h.loadDomain(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if _, ok := h.requireAllowed(c, o); !ok {
		return
	}
	var req domainGitPutRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json", "detail": "the body must be JSON: repo_url, branch, username, token, clear_token, web_dir, shared_paths, deploy_commands"})
		return
	}
	repoURL, err := gitdeploy.ParseRepoURL(strings.TrimSpace(req.RepoURL))
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_repo_url", "detail": err.Error()})
		return
	}
	username := strings.TrimSpace(req.Username)
	if err := gitdeploy.ValidateUsername(username); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_username", "detail": err.Error()})
		return
	}
	branch := strings.TrimSpace(req.Branch)
	if branch != "" {
		if err := gitdeploy.ValidateBranch(branch); err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_branch", "detail": err.Error()})
			return
		}
	}
	if req.Token != "" {
		if req.ClearToken {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_token", "detail": "send a token or clear_token, not both"})
			return
		}
		if err := gitdeploy.ValidateToken(req.Token); err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_token", "detail": err.Error()})
			return
		}
	}

	old, ok := h.stored(c, o)
	if !ok {
		return
	}
	// What a deployment does: as sent, else as stored.
	webDir, shared, commands := "", []string{}, ""
	if old != nil {
		webDir, shared, commands = old.WebDir, old.SharedPathList(), old.DeployCommands
	}
	if req.WebDir != nil {
		webDir = strings.TrimSpace(*req.WebDir)
	}
	if req.SharedPaths != nil {
		shared = make([]string, 0, len(*req.SharedPaths))
		for _, p := range *req.SharedPaths {
			shared = append(shared, strings.TrimSpace(p))
		}
	}
	if err := gitdeploy.ValidateLayout(webDir, shared); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_deploy_layout", "detail": err.Error()})
		return
	}
	if req.DeployCommands != nil {
		normalized, err := gitdeploy.ValidateCommands(*req.DeployCommands)
		if err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_deploy_commands", "detail": err.Error()})
			return
		}
		commands = normalized
	}
	// The token this save stores: a new one, none, or the stored one, which
	// stays only on the host it was given for.
	token, sealed := req.Token, []byte(nil)
	if token == "" && !req.ClearToken && old != nil && len(old.TokenEnc) > 0 {
		oldURL, perr := gitdeploy.ParseRepoURL(old.RepoURL)
		if perr != nil || oldURL.Host != repoURL.Host {
			c.JSON(http.StatusUnprocessableEntity, gin.H{
				"error":  "token_required_for_new_host",
				"detail": "the stored token was given for another host: send the token again, or clear_token for a public repository",
			})
			return
		}
		if h.cfg.SSOKey == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "sso_key_unavailable", "detail": "the panel's sso.key is not configured, so the stored token can't be read"})
			return
		}
		plain, oerr := h.cfg.SSOKey.Open(old.TokenEnc)
		if oerr != nil {
			slog.ErrorContext(ctx, "git: open the stored token", "domain", o.domain.ID, "error", oerr)
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "token_unreadable", "detail": "the stored token can't be read: send it again"})
			return
		}
		token, sealed = string(plain), old.TokenEnc
	}
	if token != "" && sealed == nil {
		if h.cfg.SSOKey == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "sso_key_unavailable", "detail": "the panel's sso.key is not configured, so the token can't be stored securely"})
			return
		}
		if sealed, err = h.cfg.SSOKey.Seal([]byte(token)); err != nil {
			slog.ErrorContext(ctx, "git: seal the token", "domain", o.domain.ID, "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
			return
		}
	}

	remote, ok := h.lsRemote(c, o, repoURL, username, token)
	if !ok {
		return
	}
	if branch == "" {
		branch = remote.DefaultBranch
	}
	if branch == "" || len(remote.Branches) == 0 {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "repository_empty", "detail": "the repository has no branches yet"})
		return
	}
	// A list cut at the agent's cap may not name the branch; it is checked
	// again when a deployment fetches it.
	if !remote.HasBranch(branch) && !remote.Truncated {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "branch_not_found", "detail": "the repository has no branch " + branch, "remote": remote})
		return
	}

	row := &models.DomainGitRepo{
		DomainID: o.domain.ID, UserID: o.domain.UserID, RepoURL: repoURL.Raw, Branch: branch,
		Username: username, TokenEnc: sealed, WebDir: webDir, DeployCommands: commands,
		VerifiedAt: time.Now().UTC(),
	}
	row.SetSharedPathList(shared)
	if old != nil {
		row.CreatedAt = old.CreatedAt
	}
	if err := h.cfg.Repos.Put(ctx, row); err != nil {
		slog.ErrorContext(ctx, "git: store the settings", "domain", o.domain.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	view := newDomainGitView(row)
	view.Enabled = true
	view.Remote = remote
	c.JSON(http.StatusOK, view)
}

// lsRemote reads the repository's branches as the domain's owner, answering
// the request itself when it can't.
func (h *domainGitHandler) lsRemote(c *gin.Context, o *gitOwner, repoURL *gitdeploy.RepoURL, username, token string) (*gitdeploy.LsRemoteResponse, bool) {
	ctx := c.Request.Context()
	if err := h.cfg.CheckHost(ctx, repoURL.Hostname); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "repo_host_not_allowed", "detail": err.Error()})
		return nil, false
	}
	remote, err := gitdeploy.LsRemote(ctx, h.cfg.Agent, gitdeploy.LsRemoteRequest{
		OSUser: *o.user.Username, RepoURL: repoURL.Raw, Username: username, Token: token,
	})
	if err != nil {
		var ae *agent.AgentError
		if errors.As(err, &ae) && (ae.Code == agent.CodeFailedPrecondition || ae.Code == agent.CodeInvalidArgument) {
			// git's own words, for the user. They hold no credentials: the
			// URL has none.
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "git_remote_failed", "detail": ae.Message})
			return nil, false
		}
		respondAgentErr(c, "git_remote_failed", err)
		return nil, false
	}
	return remote, true
}

func (h *domainGitHandler) branches(c *gin.Context) {
	o, ok := h.loadDomain(c)
	if !ok {
		return
	}
	if _, ok := h.requireAllowed(c, o); !ok {
		return
	}
	row, ok := h.configured(c, o)
	if !ok {
		return
	}
	repoURL, token, ok := h.remoteOf(c, o, row)
	if !ok {
		return
	}
	remote, ok := h.lsRemote(c, o, repoURL, row.Username, token)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, remote)
}

// configured loads the domain's settings, answering 404 when it has none.
func (h *domainGitHandler) configured(c *gin.Context, o *gitOwner) (*models.DomainGitRepo, bool) {
	row, ok := h.stored(c, o)
	if !ok {
		return nil, false
	}
	if row == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "git_not_configured"})
		return nil, false
	}
	return row, true
}

// remoteOf is the stored repository's URL and token, answering the request
// itself when either can't be used.
func (h *domainGitHandler) remoteOf(c *gin.Context, o *gitOwner, row *models.DomainGitRepo) (*gitdeploy.RepoURL, string, bool) {
	repoURL, err := gitdeploy.ParseRepoURL(row.RepoURL)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_repo_url", "detail": err.Error()})
		return nil, "", false
	}
	if len(row.TokenEnc) == 0 {
		return repoURL, "", true
	}
	if h.cfg.SSOKey == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "sso_key_unavailable", "detail": "the panel's sso.key is not configured, so the stored token can't be read"})
		return nil, "", false
	}
	plain, err := h.cfg.SSOKey.Open(row.TokenEnc)
	if err != nil {
		slog.ErrorContext(c.Request.Context(), "git: open the stored token", "domain", o.domain.ID, "error", err)
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "token_unreadable", "detail": "the stored token can't be read: save it again"})
		return nil, "", false
	}
	return repoURL, string(plain), true
}

func (h *domainGitHandler) delete(c *gin.Context) {
	o, ok := h.loadDomain(c)
	if !ok {
		return
	}
	err := h.cfg.Repos.Delete(c.Request.Context(), o.domain.ID)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		slog.ErrorContext(c.Request.Context(), "git: delete the settings", "domain", o.domain.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	c.Status(http.StatusNoContent)
}

// --- deployments (ADR-0178) ---

// gitDeploymentDTO is a run on the wire, with its log when one run is read.
type gitDeploymentDTO struct {
	models.DomainGitDeployment
	Log string `json:"log,omitempty"`
}

func newGitDeploymentDTO(run *models.DomainGitDeployment, withLog bool) *gitDeploymentDTO {
	d := &gitDeploymentDTO{DomainGitDeployment: *run}
	if withLog {
		d.Log = run.Log
	}
	return d
}

// refreshDeployment brings a running run up to date from the agent. When
// the agent can't be asked, the run is answered as stored.
func (h *domainGitHandler) refreshDeployment(c *gin.Context, run *models.DomainGitDeployment) {
	if err := gitdeploy.Refresh(c.Request.Context(), h.cfg.Agent, h.cfg.Deployments, h.cfg.Reconciler, run); err != nil {
		slog.WarnContext(c.Request.Context(), "git: refresh a deployment", "run", run.ID, "error", err)
	}
}

// activeDeployment is the domain's run in progress, nil when none.
func (h *domainGitHandler) activeDeployment(c *gin.Context, o *gitOwner) (*gitDeploymentDTO, bool) {
	runs, _, err := h.cfg.Deployments.ListByDomain(c.Request.Context(), o.domain.ID, o.domain.UserID, 0, 1)
	if err != nil {
		slog.ErrorContext(c.Request.Context(), "git: list deployments", "domain", o.domain.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, false
	}
	if len(runs) == 1 && runs[0].State == models.GitDeployRunning {
		h.refreshDeployment(c, &runs[0])
		if runs[0].State == models.GitDeployRunning {
			return newGitDeploymentDTO(&runs[0], false), true
		}
	}
	return nil, true
}

func (h *domainGitHandler) listDeployments(c *gin.Context) {
	o, ok := h.loadDomain(c)
	if !ok {
		return
	}
	page, pageSize := parsePagination(c)
	runs, total, err := h.cfg.Deployments.ListByDomain(c.Request.Context(), o.domain.ID, o.domain.UserID, (page-1)*pageSize, pageSize)
	if err != nil {
		slog.ErrorContext(c.Request.Context(), "git: list deployments", "domain", o.domain.ID, "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	data := make([]*gitDeploymentDTO, 0, len(runs))
	for i := range runs {
		h.refreshDeployment(c, &runs[i])
		data = append(data, newGitDeploymentDTO(&runs[i], false))
	}
	c.JSON(http.StatusOK, gin.H{"data": data, "total": total, "page": page, "page_size": pageSize})
}

// startDeployment stores run and starts it on the agent, answering the
// request with the result.
func (h *domainGitHandler) startDeployment(c *gin.Context, run *models.DomainGitDeployment, req gitdeploy.DeployStartRequest) {
	err := gitdeploy.Start(c.Request.Context(), h.cfg.Agent, h.cfg.Deployments, run, req)
	switch {
	case err == nil:
		c.JSON(http.StatusAccepted, newGitDeploymentDTO(run, false))
	case errors.Is(err, repository.ErrGitDeploymentActive):
		c.JSON(http.StatusConflict, gin.H{"error": "git_deployment_active", "detail": "a deployment is already in progress for this domain"})
	case errors.Is(err, repository.ErrNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "domain_not_found"})
	default:
		if ae, refused := gitdeploy.Refusal(err); refused {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": ae.Code, "detail": ae.Message, "run": newGitDeploymentDTO(run, false)})
			return
		}
		logAgentError(c, "git_deploy_start_failed", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "git_deploy_start_failed", "detail": "the server could not start the deployment", "run": newGitDeploymentDTO(run, false)})
	}
}

// newRun is a run of the domain, as its owner, making release live.
func newRun(o *gitOwner, kind, release string) *models.DomainGitDeployment {
	now := time.Now().UTC()
	id := ids.NewULID()
	if kind == models.GitDeployKindDeploy {
		release = id
	}
	return &models.DomainGitDeployment{
		ID: id, UserID: o.domain.UserID, DomainID: o.domain.ID, OSUser: *o.user.Username,
		Kind: kind, ReleaseID: release, StartedAt: now, CreatedAt: now, UpdatedAt: now,
	}
}

// deploy fetches the stored branch into a new release and makes it live.
func (h *domainGitHandler) deploy(c *gin.Context) {
	o, ok := h.loadDomain(c)
	if !ok {
		return
	}
	pkg, ok := h.requireAllowed(c, o)
	if !ok {
		return
	}
	row, ok := h.configured(c, o)
	if !ok {
		return
	}
	repoURL, token, ok := h.remoteOf(c, o, row)
	if !ok {
		return
	}
	if err := h.cfg.CheckHost(c.Request.Context(), repoURL.Hostname); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "repo_host_not_allowed", "detail": err.Error()})
		return
	}
	run := newRun(o, models.GitDeployKindDeploy, "")
	run.Branch = row.Branch
	run.DocRoot = gitdeploy.DocRoot(run.OSUser, o.domain.Name, row.WebDir)
	h.startDeployment(c, run, gitdeploy.DeployStartRequest{
		Domain: o.domain.Name, RepoURL: repoURL.Raw, Branch: row.Branch,
		Username: row.Username, Token: token,
		WebDir: row.WebDir, SharedPaths: row.SharedPathList(), Commands: row.DeployCommands,
		Composer: pkg.ComposerEnabled,
	})
}

// loadDeployment loads the run named by param, which must be the domain's
// and its current owner's.
func (h *domainGitHandler) loadDeployment(c *gin.Context, o *gitOwner, param string) (*models.DomainGitDeployment, bool) {
	run, err := h.cfg.Deployments.FindByID(c.Request.Context(), c.Param(param))
	if errors.Is(err, repository.ErrNotFound) || (err == nil && (run.DomainID != o.domain.ID || run.UserID != o.domain.UserID)) {
		c.JSON(http.StatusNotFound, gin.H{"error": "deployment_not_found"})
		return nil, false
	}
	if err != nil {
		slog.ErrorContext(c.Request.Context(), "git: load deployment", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return nil, false
	}
	return run, true
}

func (h *domainGitHandler) getDeployment(c *gin.Context) {
	o, ok := h.loadDomain(c)
	if !ok {
		return
	}
	run, ok := h.loadDeployment(c, o, "runId")
	if !ok {
		return
	}
	h.refreshDeployment(c, run)
	c.JSON(http.StatusOK, newGitDeploymentDTO(run, true))
}

func (h *domainGitHandler) cancelDeployment(c *gin.Context) {
	o, ok := h.loadDomain(c)
	if !ok {
		return
	}
	run, ok := h.loadDeployment(c, o, "runId")
	if !ok {
		return
	}
	if run.State != models.GitDeployRunning {
		c.JSON(http.StatusConflict, gin.H{"error": "git_deployment_finished", "run": newGitDeploymentDTO(run, true)})
		return
	}
	err := gitdeploy.Cancel(c.Request.Context(), h.cfg.Agent, h.cfg.Deployments, h.cfg.Reconciler, run)
	switch {
	case err == nil:
		c.JSON(http.StatusOK, newGitDeploymentDTO(run, true))
	case errors.Is(err, gitdeploy.ErrNotRunning):
		c.JSON(http.StatusConflict, gin.H{"error": "git_deployment_finished", "run": newGitDeploymentDTO(run, true)})
	default:
		logAgentError(c, "git_deploy_cancel_failed", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "git_deploy_cancel_failed", "detail": "the server could not stop the deployment"})
	}
}

// gitReleaseView is a release on the server, with the deployment that made
// it when the panel has it.
type gitReleaseView struct {
	ID         string            `json:"id"`
	Live       bool              `json:"live"`
	Deployment *gitDeploymentDTO `json:"deployment"`
}

func (h *domainGitHandler) releases(c *gin.Context) {
	o, ok := h.loadDomain(c)
	if !ok {
		return
	}
	if _, ok := h.requireAllowed(c, o); !ok {
		return
	}
	ctx := c.Request.Context()
	resp, err := gitdeploy.Releases(ctx, h.cfg.Agent, *o.user.Username, o.domain.Name)
	if err != nil {
		respondAgentErr(c, "git_releases_failed", err)
		return
	}
	out := make([]gitReleaseView, 0, len(resp.Releases))
	for _, id := range resp.Releases {
		v := gitReleaseView{ID: id, Live: id == resp.Live}
		run, err := h.cfg.Deployments.FindByID(ctx, id)
		if err == nil && run.DomainID == o.domain.ID && run.UserID == o.domain.UserID {
			v.Deployment = newGitDeploymentDTO(run, false)
		}
		out = append(out, v)
	}
	c.JSON(http.StatusOK, gin.H{"releases": out, "live": resp.Live})
}

// activate makes an earlier release live again: a rollback. Only a release
// a deployment of the domain's owner completed may be made live: a release
// a run left half-built is never served.
func (h *domainGitHandler) activate(c *gin.Context) {
	o, ok := h.loadDomain(c)
	if !ok {
		return
	}
	if _, ok := h.requireAllowed(c, o); !ok {
		return
	}
	row, ok := h.configured(c, o)
	if !ok {
		return
	}
	target, ok := h.loadDeployment(c, o, "release")
	if !ok {
		return
	}
	if target.Kind != models.GitDeployKindDeploy || target.State != models.GitDeploySucceeded {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "release_not_deployed", "detail": "only the release of a deployment that succeeded can be made live"})
		return
	}
	run := newRun(o, models.GitDeployKindRollback, target.ID)
	run.Branch, run.CommitSHA = target.Branch, target.CommitSHA
	run.DocRoot = gitdeploy.DocRoot(run.OSUser, o.domain.Name, row.WebDir)
	h.startDeployment(c, run, gitdeploy.DeployStartRequest{Domain: o.domain.Name, WebDir: row.WebDir})
}
