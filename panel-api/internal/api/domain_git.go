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
}

// RegisterDomainGitRoutes adds the Git repository endpoints of a domain:
//   - GET    /domains/:id/git           may the owner use Git deploy, and the repository settings (never the token)
//   - PUT    /domains/:id/git           save the settings, after reading the repository's branches with them
//   - DELETE /domains/:id/git           forget the settings and the token
//   - GET    /domains/:id/git/branches  the repository's branches, read with the stored settings
//
// The domain's owner and admins may use them. Saving and reading branches
// need the owner's hosting package to allow Git deploy (GH #282: a user with
// no package can't); removing the settings does not.
func RegisterDomainGitRoutes(g *gin.RouterGroup, cfg DomainGitHandlerConfig) {
	if cfg.Domains == nil || cfg.Users == nil || cfg.Packages == nil || cfg.Repos == nil || cfg.Agent == nil {
		panic("api.RegisterDomainGitRoutes: Domains, Users, Packages, Repos and Agent are required")
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

// allowed says whether the owner may use Git deploy, and if not, why: the
// error code the API answers with.
func (h *domainGitHandler) allowed(ctx context.Context, o *gitOwner) (bool, string) {
	if o.user.Username == nil || *o.user.Username == "" {
		return false, "no_account"
	}
	if o.user.Suspended {
		return false, "account_suspended"
	}
	if o.user.PackageID == nil {
		return false, "git_deploy_requires_package"
	}
	pkg, err := h.cfg.Packages.FindByID(ctx, *o.user.PackageID)
	if err != nil || pkg == nil {
		return false, "git_deploy_requires_package"
	}
	if !pkg.GitDeployEnabled {
		return false, "git_deploy_not_in_package"
	}
	return true, ""
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
	// Remote is the repository's branches, after a save.
	Remote *gitdeploy.LsRemoteResponse `json:"remote,omitempty"`
}

func newDomainGitView(row *models.DomainGitRepo) domainGitView {
	if row == nil {
		return domainGitView{}
	}
	verified := row.VerifiedAt
	return domainGitView{
		Configured: true, RepoURL: row.RepoURL, Branch: row.Branch, Username: row.Username,
		HasToken: len(row.TokenEnc) > 0, VerifiedAt: &verified,
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
	view.Enabled, view.Reason = h.allowed(c.Request.Context(), o)
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
}

func (h *domainGitHandler) put(c *gin.Context) {
	o, ok := h.loadDomain(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if allowed, reason := h.allowed(ctx, o); !allowed {
		c.JSON(http.StatusForbidden, gin.H{"error": reason})
		return
	}
	var req domainGitPutRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_json", "detail": "the body must be JSON: repo_url, branch, username, token, clear_token"})
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
		Username: username, TokenEnc: sealed, VerifiedAt: time.Now().UTC(),
	}
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
	ctx := c.Request.Context()
	if allowed, reason := h.allowed(ctx, o); !allowed {
		c.JSON(http.StatusForbidden, gin.H{"error": reason})
		return
	}
	row, ok := h.stored(c, o)
	if !ok {
		return
	}
	if row == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "git_not_configured"})
		return
	}
	repoURL, err := gitdeploy.ParseRepoURL(row.RepoURL)
	if err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "invalid_repo_url", "detail": err.Error()})
		return
	}
	token := ""
	if len(row.TokenEnc) > 0 {
		if h.cfg.SSOKey == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "sso_key_unavailable", "detail": "the panel's sso.key is not configured, so the stored token can't be read"})
			return
		}
		plain, err := h.cfg.SSOKey.Open(row.TokenEnc)
		if err != nil {
			slog.ErrorContext(ctx, "git: open the stored token", "domain", o.domain.ID, "error", err)
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "token_unreadable", "detail": "the stored token can't be read: save it again"})
			return
		}
		token = string(plain)
	}
	remote, ok := h.lsRemote(c, o, repoURL, row.Username, token)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, remote)
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
