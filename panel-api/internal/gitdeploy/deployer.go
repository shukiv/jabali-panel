package gitdeploy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ids"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// Refused is why a deployment can't start, found before anything is
// stored: the status, error code and detail the API answers with.
type Refused struct {
	Status int
	Code   string
	Detail string
}

func (e *Refused) Error() string {
	if e.Detail == "" {
		return e.Code
	}
	return e.Code + ": " + e.Detail
}

// Allowed returns the owner's hosting package when it allows Git deploy, and
// if it does not, why: the error code the API answers with.
func Allowed(ctx context.Context, packages repository.PackageRepository, owner *models.User) (*models.HostingPackage, string) {
	if owner.Username == nil || *owner.Username == "" {
		return nil, "no_account"
	}
	if owner.Suspended {
		return nil, "account_suspended"
	}
	if owner.PackageID == nil {
		return nil, "git_deploy_requires_package"
	}
	pkg, err := packages.FindByID(ctx, *owner.PackageID)
	if err != nil || pkg == nil {
		return nil, "git_deploy_requires_package"
	}
	if !pkg.GitDeployEnabled {
		return nil, "git_deploy_not_in_package"
	}
	return pkg, ""
}

// Remote is the stored repository's URL and token. When either can't be
// used it returns a *Refused.
func Remote(key *ssokey.Key, row *models.DomainGitRepo) (*RepoURL, string, error) {
	repoURL, err := ParseRepoURL(row.RepoURL)
	if err != nil {
		return nil, "", &Refused{Status: http.StatusUnprocessableEntity, Code: "invalid_repo_url", Detail: err.Error()}
	}
	if len(row.TokenEnc) == 0 {
		return repoURL, "", nil
	}
	if key == nil {
		return nil, "", &Refused{Status: http.StatusServiceUnavailable, Code: "sso_key_unavailable", Detail: "the panel's sso.key is not configured, so the stored token can't be read"}
	}
	plain, err := key.Open(row.TokenEnc)
	if err != nil {
		slog.Error("git: open the stored token", "domain", row.DomainID, "error", err)
		return nil, "", &Refused{Status: http.StatusUnprocessableEntity, Code: "token_unreadable", Detail: "the stored token can't be read: save it again"}
	}
	return repoURL, string(plain), nil
}

// NewRun is a manual run of dom, as osUser, making release live. A
// deployment's release is the run's own ID.
func NewRun(dom *models.Domain, osUser, kind, release string) *models.DomainGitDeployment {
	now := time.Now().UTC()
	id := ids.NewULID()
	if kind == models.GitDeployKindDeploy {
		release = id
	}
	return &models.DomainGitDeployment{
		ID: id, UserID: dom.UserID, DomainID: dom.ID, OSUser: osUser,
		Kind: kind, Source: models.GitDeploySourceManual, ReleaseID: release,
		StartedAt: now, CreatedAt: now, UpdatedAt: now,
	}
}

// Deployer starts deployments of a domain's stored branch: for the API, for
// a webhook delivery, and for a push that waited for a run to end.
type Deployer struct {
	Agent       agent.AgentInterface
	Packages    repository.PackageRepository
	Repos       repository.DomainGitRepoRepository
	Deployments repository.DomainGitDeploymentRepository
	SSOKey      *ssokey.Key
	// CheckHost refuses a repository host the server must not connect to.
	// nil uses CheckHost; tests replace it.
	CheckHost func(ctx context.Context, hostname string) error
}

// Deploy starts a deployment of dom's stored branch as owner, the domain's
// owner. It returns a *Refused, with nothing stored, when the deployment
// can't start; repository.ErrGitDeploymentActive when the domain has a run
// in progress; and otherwise what Start returns, with the run.
func (d *Deployer) Deploy(ctx context.Context, dom *models.Domain, owner *models.User, source string) (*models.DomainGitDeployment, error) {
	pkg, reason := Allowed(ctx, d.Packages, owner)
	if pkg == nil {
		return nil, &Refused{Status: http.StatusForbidden, Code: reason}
	}
	row, err := d.Repos.Get(ctx, dom.ID)
	if errors.Is(err, repository.ErrNotFound) || (err == nil && row.UserID != dom.UserID) {
		// No settings, or the previous owner's (GH #1238).
		return nil, &Refused{Status: http.StatusNotFound, Code: "git_not_configured"}
	}
	if err != nil {
		return nil, fmt.Errorf("load the Git settings: %w", err)
	}
	repoURL, token, err := Remote(d.SSOKey, row)
	if err != nil {
		return nil, err
	}
	check := d.CheckHost
	if check == nil {
		check = CheckHost
	}
	if err := check(ctx, repoURL.Hostname); err != nil {
		return nil, &Refused{Status: http.StatusUnprocessableEntity, Code: "repo_host_not_allowed", Detail: err.Error()}
	}
	run := NewRun(dom, *owner.Username, models.GitDeployKindDeploy, "")
	run.Source = source
	run.Branch = row.Branch
	run.DocRoot = DocRoot(run.OSUser, dom.Name, row.WebDir)
	return run, Start(ctx, d.Agent, d.Deployments, run, DeployStartRequest{
		Domain: dom.Name, RepoURL: repoURL.Raw, Branch: row.Branch,
		Username: row.Username, Token: token,
		WebDir: row.WebDir, SharedPaths: row.SharedPathList(), Commands: row.DeployCommands,
		Composer: pkg.ComposerEnabled,
	})
}
