package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// createBackupDestinationDirect is the CLI testable core for `jabali destination
// create`. It writes the destination's Agent credential file (when the caller
// supplied env credentials), persists the row, and — on ANY persistence failure
// — removes the just-written credential file so a transient DB error never
// orphans a root:root 0600 secrets file (SSHPASS / cloud keys) on the box.
//
// This mirrors the REST handler's unconditional rollback
// (internal/api/backup_destinations.go): the CLI previously deleted the file
// only on a name-conflict, leaking it on every other create failure (JAB-310
// AC2). The returned error preserves the wrapped cause, so the RunE can still
// select the conflict-specific message via errors.Is(err, repository.ErrConflict).
func createBackupDestinationDirect(ctx context.Context, call agentCaller, repo repository.BackupDestinationRepository, d *models.BackupDestination, env map[string]string) error {
	if len(env) > 0 {
		if _, err := call(ctx, "backup.dest.creds_write", map[string]any{
			"dest_id": d.ID,
			"env":     env,
		}); err != nil {
			return fmt.Errorf("write credentials: %w", err)
		}
		ref := filepath.Join(credsDir, d.ID+".env")
		d.CredentialsRef = &ref
	}
	if err := repo.Create(ctx, d); err != nil {
		// Compensate on EVERY failure, not just a name-conflict. Best-effort, but
		// surface a failed cleanup so the operator isn't blind to a leaked secrets
		// file (never silently swallowed).
		if d.CredentialsRef != nil {
			if _, derr := call(ctx, "backup.dest.creds_delete", map[string]any{"dest_id": d.ID}); derr != nil {
				fmt.Fprintf(os.Stderr, "warning: credential file cleanup failed (%v); remove %s manually\n", derr, *d.CredentialsRef)
			}
		}
		return fmt.Errorf("create destination: %w", err)
	}
	return nil
}

// updateBackupDestinationDirect is the CLI testable core for the persist step of
// `jabali destination update`. It persists the mutated row and — when this update
// wrote a BRAND-NEW credential file — removes that just-written file on ANY
// persistence failure, so a transient DB error can't leave an orphaned root:root
// 0600 secrets file (SSHPASS / cloud keys) behind a row that never got the
// reference (JAB-310).
//
// The gate rests on an invariant of the update RunE: d.CredentialsRef only goes
// nil -> non-nil inside a creds_write branch. So origHadCredsFile == false (the
// DB row had no credential file before this call) together with
// d.CredentialsRef != nil means exactly "we wrote a new file this call, and the
// row that survives the failed persist references none" — a true orphan.
//
// A PRE-EXISTING credential file (origHadCredsFile true) is deliberately LEFT in
// place on failure: the surviving row still references it (the path is
// deterministic per dest_id), so deleting it would break the destination that
// remains in the DB. That reverse case — a --clear-creds followed by a failed
// persist, leaving the row pointing at a now-missing file — is a separate class
// (not a leak) and out of scope here.
//
// sharedAgent is *agent.Client (a pointer), so the call value is safe to build
// even on the DB-only update paths where the agent was never initialised: the
// compensation branch is the only caller and it is reached only after a
// creds_write this call, which required the credential agent. Cleanup is
// best-effort but a failed cleanup is surfaced, never silently swallowed
// (JAB-275).
func updateBackupDestinationDirect(ctx context.Context, call agentCaller, repo repository.BackupDestinationRepository, d *models.BackupDestination, origHadCredsFile bool) error {
	if err := repo.Update(ctx, d); err != nil {
		if !origHadCredsFile && d.CredentialsRef != nil {
			if _, derr := call(ctx, "backup.dest.creds_delete", map[string]any{"dest_id": d.ID}); derr != nil {
				fmt.Fprintf(os.Stderr, "warning: credential file cleanup failed (%v); remove %s manually\n", derr, *d.CredentialsRef)
			}
		}
		return fmt.Errorf("update destination: %w", err)
	}
	return nil
}

// deleteBackupDestinationDirect is the CLI testable core for the persist step of
// `jabali destination delete`. It removes the row, then removes the destination's
// Agent credential file. The credential removal is best-effort — the row is gone
// regardless — but a failed cleanup is surfaced to errOut, never silently
// swallowed (JAB-275), so an operator isn't blind to a leaked root:root 0600
// secrets file (SSHPASS / cloud keys). This was the last swallowed creds_delete;
// the create/update cores in this file already surface theirs.
//
// The creds_delete call is unconditional, matching the pre-existing RunE: the
// Agent handler is idempotent (os.Remove tolerates a missing file), so a
// destination that never had a credential file produces no spurious warning.
// errOut is injected — its sibling cores hard-code os.Stderr — because the
// surface, the warning on a cleanup failure, is the behavior under test here.
// call is never a nil-pointer method value: the delete RunE runs under
// requireDBAndAgent, so sharedAgent is initialised before the core is reached.
func deleteBackupDestinationDirect(ctx context.Context, call agentCaller, repo repository.BackupDestinationRepository, d *models.BackupDestination, errOut io.Writer) error {
	if err := repo.Delete(ctx, d.ID); err != nil {
		return fmt.Errorf("delete destination: %w", err)
	}
	if _, derr := call(ctx, "backup.dest.creds_delete", map[string]any{"dest_id": d.ID}); derr != nil {
		fmt.Fprintf(errOut, "warning: credential file cleanup failed for destination %s (%v); remove %s manually\n",
			d.ID, derr, filepath.Join(credsDir, d.ID+".env"))
	}
	return nil
}
