package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// writeBackupDestinationCreds dispatches the credential-file write to the Agent
// (root) and returns the on-disk path the Agent reports in its reply. The Agent
// is the authoritative owner of that path: panel-api runs as the jabali user and
// cannot see /etc/jabali-panel/, so the row's CredentialsRef must record what the
// Agent actually wrote, not a path panel-api guessed locally. This mirrors the
// REST handler's writeCreds (internal/api/backup_destinations.go), so both callers
// materialize the same reference under a binary-version skew between panel and
// Agent (JAB-310 AC5).
//
// One step further than REST's writeCreds: when the Agent replies without a
// usable path (unparseable reply, or an empty "path"), the file is already on
// disk but the row can never reference it — an orphan-in-waiting. The write is
// idempotently compensated (creds_delete, ENOENT-tolerant on the Agent) before the
// error returns, so a malformed reply leaves no unreferenced secrets file behind.
// A failed compensation is surfaced on stderr, never silently swallowed (JAB-275);
// bringing REST's writeCreds to this same standard is a named follow-up.
func writeBackupDestinationCreds(ctx context.Context, call agentCaller, destID string, env map[string]string) (string, error) {
	raw, err := call(ctx, "backup.dest.creds_write", map[string]any{
		"dest_id": destID,
		"env":     env,
	})
	if err != nil {
		// Treated as not written: no compensation, matching REST's writeCreds.
		// A reply lost after the Agent already completed the write is not
		// distinguishable from a true write failure at this layer, so a rare
		// orphan is possible on this path; it is out of scope for this change.
		return "", err
	}
	var resp struct {
		Path string `json:"path"`
	}
	if perr := json.Unmarshal(raw, &resp); perr != nil || resp.Path == "" {
		if _, derr := call(ctx, "backup.dest.creds_delete", map[string]any{"dest_id": destID}); derr != nil {
			fmt.Fprintf(os.Stderr, "warning: credential file cleanup failed (%v); remove %s manually\n", derr, filepath.Join(credsDir, destID+".env"))
		}
		if perr != nil {
			return "", fmt.Errorf("parse creds_write reply: %w", perr)
		}
		return "", errors.New("creds_write reply had no path")
	}
	return resp.Path, nil
}

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
		path, err := writeBackupDestinationCreds(ctx, call, d.ID, env)
		if err != nil {
			return fmt.Errorf("write credentials: %w", err)
		}
		d.CredentialsRef = &path
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
// `jabali destination update`. It persists the mutated row and reconciles the
// on-disk Agent credential file to the row that actually committed (JAB-310):
//
//   - orphan compensation (persist FAILS): the surviving row is the unchanged
//     original, so the on-disk file should exist iff origHadCredsFile. When
//     origHadCredsFile == false, any root:root 0600 secrets file (SSHPASS / cloud
//     keys) on disk was written by THIS call — either the reference still points
//     at it (a plain creds_write) or a later --clear-creds in the same command
//     nil'd the reference (clearedCredsFile, e.g. --sftp-password ...
//     --clear-creds). Both are orphans behind a row that never got the reference,
//     and both are removed. A PRE-EXISTING file (origHadCredsFile true) is
//     deliberately LEFT in place: the surviving row still references it
//     (deterministic path per dest_id), so deleting it would break the
//     destination that remains in the DB.
//
//   - clear-creds reap (persist SUCCEEDS): when --clear-creds dropped the
//     reference (clearedCredsFile == true and d.CredentialsRef ends nil), the
//     on-disk file is removed AFTER the row that no longer references it commits.
//     The RunE must NOT delete the file before this call: on a failed persist that
//     would leave the surviving row pointing at an already-deleted file — a
//     dangling reference, the reverse of the orphan leak. The reap is skipped when
//     d.CredentialsRef is non-nil, meaning a creds_write later in the same update
//     re-created the file at the deterministic path (--clear-creds --env): the row
//     references it, so it must stay.
//
// The invariant the core holds: after it returns, the on-disk credential file
// exists iff the PERSISTED row references it — on failure the persisted row is the
// unchanged original (file iff origHadCredsFile), on success it is d (file iff
// d.CredentialsRef != nil). d.CredentialsRef only goes nil -> non-nil inside a
// creds_write branch, so under !origHadCredsFile a reference that is set OR was
// cleared this call both imply a file this call wrote.
//
// sharedAgent is *agent.Client (a pointer), so the call value is safe to build
// even on DB-only update paths where the agent was never initialised: both agent
// branches are reached only after a creds_write / --clear-creds this call, which
// required the credential agent. Both cleanups are best-effort but surfaced,
// never silently swallowed (JAB-275), and neither is fatal to a committed update.
func updateBackupDestinationDirect(ctx context.Context, call agentCaller, repo repository.BackupDestinationRepository, d *models.BackupDestination, origHadCredsFile, clearedCredsFile bool) error {
	if err := repo.Update(ctx, d); err != nil {
		// Orphan compensation. Under !origHadCredsFile the surviving (unchanged)
		// row references no file, so any credential file on disk was written by
		// THIS call and is now an orphan — whether the reference still points at it
		// (a plain creds_write) or a later --clear-creds in the same command nil'd
		// the reference (clearedCredsFile: e.g. --sftp-password ... --clear-creds).
		// Both must be removed.
		if !origHadCredsFile && (d.CredentialsRef != nil || clearedCredsFile) {
			if _, derr := call(ctx, "backup.dest.creds_delete", map[string]any{"dest_id": d.ID}); derr != nil {
				fmt.Fprintf(os.Stderr, "warning: credential file cleanup failed (%v); remove %s manually\n", derr, filepath.Join(credsDir, d.ID+".env"))
			}
		}
		return fmt.Errorf("update destination: %w", err)
	}
	if clearedCredsFile && d.CredentialsRef == nil {
		if _, derr := call(ctx, "backup.dest.creds_delete", map[string]any{"dest_id": d.ID}); derr != nil {
			fmt.Fprintf(os.Stderr, "warning: credential file cleanup after --clear-creds failed (%v); remove %s manually\n", derr, filepath.Join(credsDir, d.ID+".env"))
		}
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
