package reconciler

import (
	"context"
	"errors"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/gitdeploy"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// gitDeploymentsPerTick bounds the runs one pass asks the agent about. A
// domain has at most one run in progress, so this is many domains' worth.
const gitDeploymentsPerTick = 50

// WithGitDeployments wires the Git deployments repository (GH #2277,
// ADR-0178).
func (r *Reconciler) WithGitDeployments(repo repository.DomainGitDeploymentRepository) *Reconciler {
	r.gitDeployments = repo
	return r
}

// WithGitWebhooks wires the deploy-on-push webhooks, and what starts the
// deployment a push waited for (GH #2277).
func (r *Reconciler) WithGitWebhooks(repo repository.DomainGitWebhookRepository, deployer *gitdeploy.Deployer) *Reconciler {
	r.gitWebhooks, r.gitDeployer = repo, deployer
	return r
}

// reconcileGitDeployments stores the result of deployments that finished
// while nobody was reading them: it points the domain's document root at
// the release a deployment made live, and releases the run's unit, which
// removes the run's credential file when the run could not. Then it starts
// the deployments pushes waited for.
func (r *Reconciler) reconcileGitDeployments(ctx context.Context) {
	if r.agent == nil || r.gitDeployments == nil {
		return
	}
	runs, err := r.gitDeployments.ListRunning(ctx, gitDeploymentsPerTick)
	if err != nil {
		r.log.Warn("git deploy: list runs in progress", "error", err)
		return
	}
	for i := range runs {
		if err := gitdeploy.Refresh(ctx, r.agent, r.gitDeployments, r, &runs[i]); err != nil {
			var ae *agent.AgentError
			if errors.As(err, &ae) && ae.Code == agent.CodeUnknownCommand {
				// An agent from before Git deployments (mid-update).
				return
			}
			r.log.Warn("git deploy: collect a run", "run", runs[i].ID, "error", err)
		}
	}
	r.startQueuedGitDeployments(ctx)
}

// startQueuedGitDeployments starts one deployment for each domain a push
// waits on, once the domain has no run in progress. Pushes that arrived
// while it waited are in it: it fetches the branch as it is now.
func (r *Reconciler) startQueuedGitDeployments(ctx context.Context) {
	if r.gitWebhooks == nil || r.gitDeployer == nil {
		return
	}
	hooks, err := r.gitWebhooks.ListQueued(ctx, gitDeploymentsPerTick)
	if err != nil {
		r.log.Warn("git deploy: list waiting pushes", "error", err)
		return
	}
	for _, hook := range hooks {
		taken, err := r.gitWebhooks.Unqueue(ctx, hook.DomainID)
		if err != nil {
			r.log.Warn("git deploy: take a waiting push", "domain", hook.DomainID, "error", err)
			continue
		}
		if !taken {
			continue
		}
		result := r.startQueuedGitDeployment(ctx, hook.DomainID)
		// The delivery's time stays; only what came of it changes.
		if err := r.gitWebhooks.RecordDelivery(ctx, hook.DomainID, time.Time{}, result); err != nil {
			r.log.Warn("git deploy: record a waiting push's result", "domain", hook.DomainID, "error", err)
		}
	}
}

// startQueuedGitDeployment starts a deployment of the domain's branch, as
// its owner, and returns what came of it (models.GitWebhook*).
func (r *Reconciler) startQueuedGitDeployment(ctx context.Context, domainID string) string {
	dom, err := r.domains.FindByID(ctx, domainID)
	if err != nil {
		r.log.Warn("git deploy: load a waiting push's domain", "domain", domainID, "error", err)
		return models.GitWebhookFailed
	}
	owner, err := r.users.FindByID(ctx, dom.UserID)
	if err != nil || owner == nil {
		r.log.Warn("git deploy: load a waiting push's owner", "domain", domainID, "error", err)
		return models.GitWebhookFailed
	}
	_, err = r.gitDeployer.Deploy(ctx, dom, owner, models.GitDeploySourceWebhook)
	var refused *gitdeploy.Refused
	switch {
	case err == nil:
		return models.GitWebhookStarted
	case errors.Is(err, repository.ErrGitDeploymentActive):
		// Still running (more than one pass's worth of runs, or one started
		// meanwhile): wait for the next pass.
		if qerr := r.gitWebhooks.Queue(ctx, domainID, time.Now().UTC()); qerr != nil {
			r.log.Warn("git deploy: queue a push again", "domain", domainID, "error", qerr)
			return models.GitWebhookFailed
		}
		return models.GitWebhookQueued
	case errors.As(err, &refused):
		return models.GitWebhookRefused + refused.Code
	default:
		if ae, ok := gitdeploy.Refusal(err); ok {
			return models.GitWebhookRefused + ae.Code
		}
		r.log.Warn("git deploy: start a waiting push's deployment", "domain", domainID, "error", err)
		return models.GitWebhookFailed
	}
}
