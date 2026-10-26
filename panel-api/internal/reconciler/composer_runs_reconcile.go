package reconciler

import (
	"context"
	"errors"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/composerrun"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// composerRunsPerTick bounds the runs one pass asks the agent about. A domain
// has at most one run in progress, so this is many domains' worth.
const composerRunsPerTick = 50

// WithComposerRuns wires the Composer runs repository (GH #2276, ADR-0176).
func (r *Reconciler) WithComposerRuns(repo repository.ComposerRunRepository) *Reconciler {
	r.composerRuns = repo
	return r
}

// reconcileComposerRuns stores the result of runs that finished while nobody
// was reading them. The agent keeps a run's unit loaded until the panel has
// its result, so without this pass the unit, and the row's running state,
// would stay until someone opened the run.
func (r *Reconciler) reconcileComposerRuns(ctx context.Context) {
	if r.agent == nil || r.composerRuns == nil {
		return
	}
	runs, err := r.composerRuns.ListRunning(ctx, composerRunsPerTick)
	if err != nil {
		r.log.Warn("composer: list runs in progress", "error", err)
		return
	}
	for i := range runs {
		if err := composerrun.Refresh(ctx, r.agent, r.composerRuns, &runs[i]); err != nil {
			var ae *agent.AgentError
			if errors.As(err, &ae) && ae.Code == agent.CodeUnknownCommand {
				// An agent from before Composer runs (mid-update).
				return
			}
			r.log.Warn("composer: collect a run", "run", runs[i].ID, "error", err)
		}
	}
}
