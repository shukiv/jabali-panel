package main

import (
	"context"
	"fmt"
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
