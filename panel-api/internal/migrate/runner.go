package migrate

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Runner walks one migration_jobs row through the four-stage
// pipeline (analyze → fix_perms → validate → restore) and
// dispatches per-stage callbacks supplied by the caller.
//
// Stage state advances ONLY via IsValidJobTransition — illegal
// next-states return ErrIllegalTransition + leave the job in
// whatever state it was in. Resume after a mid-run failure picks
// up from the last 'failed' / 'pending' stage.
type Runner struct {
	Jobs  repository.MigrationJobRepository
	Agent agent.AgentInterface
	// StageCallbacks maps a stage name (analyze / fix_perms /
	// validate / restore) to the function that runs it. Per-source
	// importer code (cpanel, directadmin, ...) provides these.
	// Each callback receives the job + an opaque context payload
	// (the manifest, the parsed tarball, the target user, ...) the
	// caller stuffed into the runner via WithContext.
	StageCallbacks map[string]StageCallback
	// payload is opaque-to-Runner state forwarded to every callback.
	// Per-source importer fills with whatever its callbacks need
	// (parsed tarball, target_user_id, target_username, etc.).
	payload any
}

// StageCallback is the per-stage worker. Returns the bytes-processed
// counter (drives the UI progress bar via migration_stages) + a
// best-effort warnings slice. A non-nil error transitions the job
// to MigrationStateFailed; a nil error transitions the job through
// the next legal state.
type StageCallback func(ctx context.Context, job *models.MigrationJob, payload any) (bytesProcessed int64, warnings []string, err error)

// WithContext stashes opaque payload into the runner. Per-source
// importer code calls this before Run to forward the manifest +
// target user + parsed tarball to its callbacks.
func (r *Runner) WithContext(payload any) *Runner {
	r.payload = payload
	return r
}

// Run advances the job through every stage in AllStages order.
// Returns nil only when the final stage transitions the job to
// MigrationStateDone; any callback failure leaves the job in
// MigrationStateFailed with last_error populated.
//
// Idempotent on resume: stages already at state='done' are
// skipped. The first failed/pending stage is the resume point.
func (r *Runner) Run(ctx context.Context, jobID string) error {
	if r.Jobs == nil {
		return fmt.Errorf("runner: Jobs repo nil")
	}
	job, err := r.Jobs.FindByID(ctx, jobID)
	if err != nil {
		return fmt.Errorf("runner: load job %s: %w", jobID, err)
	}
	if job.State == models.MigrationStateDone {
		// Already done — no-op. Resume on a finished job is a
		// caller mistake but we don't crash on it.
		return nil
	}

	for _, stageName := range AllStages {
		// Skip already-done stages on resume.
		stages, err := r.Jobs.ListStages(ctx, job.ID)
		if err != nil {
			return r.fail(ctx, job, fmt.Errorf("list stages: %w", err))
		}
		var stageRow *models.MigrationStage
		for i := range stages {
			if stages[i].StageName == stageName {
				stageRow = &stages[i]
				break
			}
		}
		if stageRow != nil && stageRow.State == StageStateDone {
			continue
		}
		if stageRow == nil {
			row := &models.MigrationStage{
				JobID:     job.ID,
				StageName: stageName,
				State:     StageStatePending,
			}
			if err := r.Jobs.CreateStage(ctx, row); err != nil {
				return r.fail(ctx, job, fmt.Errorf("create stage row %s: %w", stageName, err))
			}
			stageRow = row
		}

		nextJobState := jobStateForStage(stageName)
		if !IsValidJobTransition(job.State, nextJobState) {
			return r.fail(ctx, job, fmt.Errorf("%w: %s → %s", ErrIllegalTransition, job.State, nextJobState))
		}
		if err := r.Jobs.UpdateState(ctx, job.ID, nextJobState, nil); err != nil {
			return r.fail(ctx, job, fmt.Errorf("update job state to %s: %w", nextJobState, err))
		}
		job.State = nextJobState

		// Mark stage running.
		if err := r.Jobs.UpdateStage(ctx, stageRow.ID, StageStateRunning, 0, nil); err != nil {
			return r.fail(ctx, job, fmt.Errorf("update stage running %s: %w", stageName, err))
		}

		// Dispatch callback. Stages without a registered callback
		// (e.g. fix_perms is a no-op for cPanel) record as done +
		// continue so resume doesn't re-fire them.
		cb, ok := r.StageCallbacks[stageName]
		if !ok {
			if err := r.Jobs.UpdateStage(ctx, stageRow.ID, StageStateDone, 0, nil); err != nil {
				return r.fail(ctx, job, fmt.Errorf("update stage done %s: %w", stageName, err))
			}
			continue
		}
		bytes, warnings, cbErr := cb(ctx, job, r.payload)
		if cbErr != nil {
			errMsg := cbErr.Error()
			_ = r.Jobs.UpdateStage(ctx, stageRow.ID, StageStateFailed, bytes, &errMsg)
			return r.fail(ctx, job, fmt.Errorf("stage %s: %w", stageName, cbErr))
		}
		// Fold warnings into the manifest_json so the operator sees
		// them at job-end. Best-effort: a JSON-encode failure
		// shouldn't fail the migration, just the warnings record.
		if len(warnings) > 0 && r.payload != nil {
			if marshaled, jErr := json.Marshal(warnings); jErr == nil {
				_ = r.Jobs.UpdateManifest(ctx, job.ID, string(marshaled))
			}
		}
		if err := r.Jobs.UpdateStage(ctx, stageRow.ID, StageStateDone, bytes, nil); err != nil {
			return r.fail(ctx, job, fmt.Errorf("update stage done %s: %w", stageName, err))
		}
	}

	// All stages clean — terminal transition to Done.
	if !IsValidJobTransition(job.State, models.MigrationStateDone) {
		return r.fail(ctx, job, fmt.Errorf("%w: %s → done", ErrIllegalTransition, job.State))
	}
	if err := r.Jobs.UpdateState(ctx, job.ID, models.MigrationStateDone, nil); err != nil {
		return r.fail(ctx, job, fmt.Errorf("final state update: %w", err))
	}
	// Wipe per-job source credentials immediately on terminal
	// success (ADR-0094 §"tracked risks"). Daily reaper covers
	// missed wipes; this immediate-wipe shortens credential
	// lifetime from 'up to 24 hours' to 'milliseconds post job
	// done' on the success path.
	_ = WipeJobSecret(job.ID)
	return nil
}

// fail is the terminal error path. Records last_error on the job
// row + transitions to MigrationStateFailed when the current
// state allows; passes the original error through.
func (r *Runner) fail(ctx context.Context, job *models.MigrationJob, cause error) error {
	emsg := cause.Error()
	// Best-effort terminal transition. A failed UpdateState is
	// surfaced via log not the return value — caller already has
	// the original cause.
	if IsValidJobTransition(job.State, models.MigrationStateFailed) {
		// Attach a fresh ctx with short timeout so a long-cancelled
		// parent context doesn't block the bookkeeping write.
		fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = r.Jobs.UpdateState(fctx, job.ID, models.MigrationStateFailed, &emsg)
		cancel()
	}
	// Same immediate-wipe rationale as the success path. ADR-0094.
	// Best-effort: ignore the error so we never mask the original
	// cause.
	_ = WipeJobSecret(job.ID)
	return cause
}

// jobStateForStage maps a stage name to the job-level state the
// runner enters before invoking that stage's callback. Inverse
// of StageNameForState.
func jobStateForStage(stageName string) string {
	switch stageName {
	case StageAnalyze:
		return models.MigrationStateAnalyzing
	case StageFixPerms:
		return models.MigrationStateFixPerms
	case StageValidate:
		return models.MigrationStateValidating
	case StageRestore:
		return models.MigrationStateRestoring
	default:
		// Unknown stage — caller bug. Return a state that fails
		// IsValidJobTransition for any from-state so the runner
		// surfaces it as ErrIllegalTransition rather than silently
		// running an unscheduled stage.
		return "unknown_" + stageName
	}
}
