// `jabali backup destination rotate-password` (Gitea #541): restic repository
// key rotation from the CLI. Mirrors the REST handler
// (api/backup_destinations.go rotatePassword) byte-for-byte — same old-password
// resolution (sealed column wins, else legacy file via agent), same 32-byte hex
// new password, same `backup.repo.password.rotate` agent verb, same seal +
// SetPassword persist. Data-recoverability-critical: the rotate is faithful to
// the production path, not a reimplementation. Mutations are CLI-audited (#537).
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// validateSFTPOpts mirrors the REST handler's validateSFTPInputs: host+user
// and path are required; auth ∈ {key, password, ""}; key path must be absolute
// when set. Operated on the model type so CLI overlays validate identically.
func validateSFTPOpts(s *models.SFTPOptions) error {
	if s.Host == "" || s.User == "" {
		return errors.New("sftp host and user are required")
	}
	if s.Path == "" {
		return errors.New("sftp path is required")
	}
	switch s.Auth {
	case models.SFTPAuthKey:
		if s.KeyPath != "" && !strings.HasPrefix(s.KeyPath, "/") {
			return errors.New("sftp key-path must be an absolute path")
		}
	case models.SFTPAuthPassword, "":
	default:
		return errors.New("sftp auth must be 'key' or 'password'")
	}
	return nil
}

func newBackupDestinationRotatePasswordCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rotate-password <id-or-name>",
		Short: "Rotate a backup destination's restic password (revealed once)",
		Long: "Rotate the restic repository password. The agent re-keys the live " +
			"repository with the old password before the new one is persisted, so " +
			"existing snapshots stay recoverable. The new password is shown once.",
		Args:    cobra.ExactArgs(1),
		PreRunE: requireDBAndAgent,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()

			key := ssoKeyForCLI()
			if key == nil {
				return fmt.Errorf("SSO key unavailable — cannot seal the rotated password (set sso.key_path / run on the panel host)")
			}
			dest, err := resolveBackupDestination(ctx, args[0])
			if err != nil {
				return err
			}

			// Resolve the old password: sealed column wins, else the legacy
			// shared file via the agent (mirrors REST resolveOldPassword).
			var oldPassword string
			if len(dest.PasswordEnc) > 0 {
				pt, err := key.Open(dest.PasswordEnc)
				if err != nil {
					return fmt.Errorf("decrypt sealed password: %w", err)
				}
				oldPassword = string(pt)
			} else {
				raw, err := sharedAgent.Call(ctx, "backup.repo.password.read", map[string]any{})
				if err != nil {
					cliAuditErr(ctx, "backup_destination.rotate_password", "backup_destination", dest.ID, nil)
					return fmt.Errorf("agent read legacy password: %w", err)
				}
				var resp struct {
					Password string `json:"password"`
				}
				if err := json.Unmarshal(raw, &resp); err != nil {
					return fmt.Errorf("parse agent response: %w", err)
				}
				if resp.Password == "" {
					return errors.New("legacy password file is empty — no old password to re-key from")
				}
				oldPassword = resp.Password
			}

			// 32 random bytes → 64 hex chars (same as REST).
			rawBuf := make([]byte, 32)
			if _, err := rand.Read(rawBuf); err != nil {
				return fmt.Errorf("generate password: %w", err)
			}
			newPassword := hex.EncodeToString(rawBuf)

			credsRef := ""
			if dest.CredentialsRef != nil {
				credsRef = *dest.CredentialsRef
			}
			if _, err := sharedAgent.Call(ctx, "backup.repo.password.rotate", map[string]any{
				"repo_url":        dest.URL,
				"credentials_ref": credsRef,
				"old_password":    oldPassword,
				"new_password":    newPassword,
			}); err != nil {
				cliAuditErr(ctx, "backup_destination.rotate_password", "backup_destination", dest.ID, nil)
				return fmt.Errorf("agent backup.repo.password.rotate: %w", err)
			}

			sealed, err := key.Seal([]byte(newPassword))
			if err != nil {
				return fmt.Errorf("seal new password: %w", err)
			}
			now := time.Now().UTC()
			if err := backupDestinationRepoFromDB().SetPassword(ctx, dest.ID, sealed, now); err != nil {
				return fmt.Errorf("persist rotated password: %w", err)
			}
			cliAuditOK(ctx, "backup_destination.rotate_password", "backup_destination", dest.ID, nil)

			if jsonOutput {
				return printJSON(map[string]any{"id": dest.ID, "name": dest.Name, "password": newPassword, "password_rotated_at": now})
			}
			fmt.Fprintf(os.Stdout, "Rotated restic password for %s (%s)\nNew password: %s\n(Shown once — store it now; the panel keeps only an encrypted copy.)\n", dest.Name, dest.ID, newPassword)
			return nil
		},
	}
}
