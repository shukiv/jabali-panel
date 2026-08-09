// M30.2.x — per-destination password tempfile bridge.
//
// panel-api can't write 0640 root:jabali files under /run, so when a
// destination has a sealed per-row restic password we delegate the
// actual file write to the agent via backup.repo.password.write_temp.
// The helper unseals the sealed blob, asks the agent for a tempfile,
// invokes the caller-supplied closure with the resulting path, and
// guarantees cleanup via deferred backup.repo.password.cleanup_temp.
//
// When dest.PasswordEnc is nil/empty the helper invokes fn("")
// directly — the agent backup-* commands fall back to the legacy
// /etc/jabali-panel/restic-repo.password file when password_file is
// blank, preserving back-compat for destinations that haven't been
// rotated yet.
package backupwrapperhelpers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// WithDestPasswordFile invokes fn with the per-destination restic
// password file path. Empty path means "use the agent's default
// shared file"; non-empty path points at a 0640 root:jabali tempfile
// under /run/jabali/restic-pw/ that gets unlinked after fn returns.
//
// Returns the wrapped fn's error verbatim so callers can match on
// it. Wrapper errors (decrypt failure, agent unreachable) are
// returned with descriptive prefixes so panel-api's error envelope
// surfaces them.
func WithDestPasswordFile(
	ctx context.Context,
	dest *models.BackupDestination,
	agentClient agent.AgentInterface,
	ssoKey *ssokey.Key,
	fn func(passwordFile string) error,
) error {
	return withDestPasswordFile(ctx, dest, agentClient, ssoKey, true, fn)
}

// WithDestPasswordFileAsync is WithDestPasswordFile for agent calls that
// return BEFORE the work needing the password has finished — today that
// means backup.create, which spawns its orchestrator goroutine (up to 90
// minutes) and replies immediately.
//
// It does NOT unlink the tempfile when fn returns: doing so removed the
// password seconds into a long job, so every stage and the manifest
// snapshot failed to open a per-destination repo. Ownership passes to the
// agent, whose orchestrator unlinks the file when the job actually ends
// (removePasswordTempFile in backup_create.go). /run is tmpfs and the
// agent's RuntimeDirectory is preserved=no, so even a crashed job leaks
// nothing past reboot.
//
// Use WithDestPasswordFile for synchronous calls (restore, status,
// materialize) — there the deferred cleanup is correct and tighter.
func WithDestPasswordFileAsync(
	ctx context.Context,
	dest *models.BackupDestination,
	agentClient agent.AgentInterface,
	ssoKey *ssokey.Key,
	fn func(passwordFile string) error,
) error {
	return withDestPasswordFile(ctx, dest, agentClient, ssoKey, false, fn)
}

// WithOptionalDestPassword is WithDestPasswordFile for callers that may not
// have a destination at all (a legacy job with NULL destination_id) or that
// run without an SSO key. Those cases invoke fn("") — the agent then falls
// back to the shared password file — instead of erroring, so a caller can
// wrap unconditionally without repeating the nil dance.
func WithOptionalDestPassword(
	ctx context.Context,
	dest *models.BackupDestination,
	agentClient agent.AgentInterface,
	ssoKey *ssokey.Key,
	fn func(passwordFile string) error,
) error {
	if dest == nil || len(dest.PasswordEnc) == 0 || ssoKey == nil || agentClient == nil {
		return fn("")
	}
	return WithDestPasswordFile(ctx, dest, agentClient, ssoKey, fn)
}

func withDestPasswordFile(
	ctx context.Context,
	dest *models.BackupDestination,
	agentClient agent.AgentInterface,
	ssoKey *ssokey.Key,
	cleanup bool,
	fn func(passwordFile string) error,
) error {
	if dest == nil {
		return errors.New("dest required")
	}
	if len(dest.PasswordEnc) == 0 {
		// Legacy path: agent reads /etc/jabali-panel/restic-repo.password.
		return fn("")
	}
	if ssoKey == nil {
		return errors.New("ssoKey required to unseal per-destination password")
	}
	if agentClient == nil {
		return errors.New("agent required to write per-destination password tempfile")
	}
	plaintext, err := ssoKey.Open(dest.PasswordEnc)
	if err != nil {
		return fmt.Errorf("unseal destination password: %w", err)
	}
	raw, err := agentClient.Call(ctx, "backup.repo.password.write_temp", map[string]any{
		"password": string(plaintext),
	})
	if err != nil {
		return fmt.Errorf("agent write_temp: %w", err)
	}
	var resp struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil || resp.Path == "" {
		return fmt.Errorf("agent write_temp: bad response %s", string(raw))
	}
	if cleanup {
		defer func() {
			// Best-effort cleanup. Path-prefix check on the agent side
			// keeps a misbehaving caller from asking it to unlink anything
			// outside /run/jabali/restic-pw/, so the worst case here is a
			// stale tmpfs file that vanishes on reboot.
			_, _ = agentClient.Call(context.Background(),
				"backup.repo.password.cleanup_temp",
				map[string]any{"path": resp.Path})
		}()
	}
	return fn(resp.Path)
}
