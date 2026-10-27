package reconciler

import (
	"context"
	"errors"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/gitdeploy"
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

// reconcileGitDeployments stores the result of deployments that finished
// while nobody was reading them: it points the domain's document root at
// the release a deployment made live, and releases the run's unit, which
// removes the run's credential file when the run could not.
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
}
