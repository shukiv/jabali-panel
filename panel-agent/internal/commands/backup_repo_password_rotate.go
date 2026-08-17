// M30.2 — per-destination restic password rotation.
//
// Restic stores N keys per repository, each unlocking the same
// master key. Rotation is therefore add-then-remove rather than
// re-encrypt: on-disk data never changes. Sequence:
//
//   1. restic key list --json (with old password) → snapshot key set
//   2. restic key add --new-password-file <new>   → add new key
//   3. restic key list --json (with new password) → confirm new key
//      is present (verifies the new password works end-to-end)
//   4. restic key remove <old_key_id> (with new password) → drop the
//      old key. Old key id is the one marked "current" in the
//      pre-rotation listing.
//
// Failure modes: if step 2 succeeds but step 4 fails, the repository
// has both keys until the next successful rotation. That's safe
// (operator can still unlock with either password). The handler
// returns the old key id and a warning so panel-api can surface a
// "repo has stale keys" notice.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

type backupRepoPasswordRotateParams struct {
	RepoURL        string `json:"repo_url"`
	CredentialsRef string `json:"credentials_ref,omitempty"`
	OldPassword    string `json:"old_password"`
	NewPassword    string `json:"new_password"`
}

type backupRepoPasswordRotateResult struct {
	NewKeyID   string `json:"new_key_id"`
	OldKeyID   string `json:"old_key_id"`
	OldRemoved bool   `json:"old_removed"`
	Warning    string `json:"warning,omitempty"`
}

type resticKey struct {
	ID       string `json:"id"`
	Current  bool   `json:"current"`
	UserName string `json:"username"`
	HostName string `json:"hostname"`
	Created  string `json:"created"`
}

func backupRepoPasswordRotateHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p backupRepoPasswordRotateParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("malformed JSON: %v", err),
		}
	}
	if p.RepoURL == "" {
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInvalidArgument, Message: "repo_url required",
		}
	}
	if p.OldPassword == "" || p.NewPassword == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "old_password and new_password required",
		}
	}
	if p.OldPassword == p.NewPassword {
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInvalidArgument, Message: "new_password must differ from old_password",
		}
	}
	if _, err := bkResticBin(); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}

	oldFile, err := writePasswordTemp(p.OldPassword)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	defer os.Remove(oldFile)
	newFile, err := writePasswordTemp(p.NewPassword)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	defer os.Remove(newFile)

	cfgWithOld, cfgErr := bkResticConfigWithPassword(p.RepoURL, p.CredentialsRef, oldFile, nil)
	if cfgErr != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("restic config (old): %v", cfgErr),
		}
	}
	cfgWithNew, cfgErr := bkResticConfigWithPassword(p.RepoURL, p.CredentialsRef, newFile, nil)
	if cfgErr != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("restic config (new): %v", cfgErr),
		}
	}

	beforeKeys, err := resticKeyList(ctx, cfgWithOld)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeFailedPrecondition,
			Message: fmt.Sprintf("verify old password: %v", err),
		}
	}
	beforeIDs := keyIDSet(beforeKeys)

	if err := resticKeyAdd(ctx, cfgWithOld, newFile); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("restic key add: %v", err),
		}
	}

	afterKeys, err := resticKeyList(ctx, cfgWithNew)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("verify new password: %v", err),
		}
	}
	var newKeyID string
	for _, k := range afterKeys {
		if !beforeIDs[k.ID] {
			newKeyID = k.ID
			break
		}
	}
	if newKeyID == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "new key not visible in second listing",
		}
	}

	var oldKeyID string
	for _, k := range beforeKeys {
		if k.Current {
			oldKeyID = k.ID
			break
		}
	}

	out := backupRepoPasswordRotateResult{NewKeyID: newKeyID, OldKeyID: oldKeyID}
	if oldKeyID == "" {
		out.Warning = "old key id not identifiable; left in place"
		return out, nil
	}

	if err := resticKeyRemove(ctx, cfgWithNew, oldKeyID); err != nil {
		out.Warning = fmt.Sprintf("new key %s active, removing old key %s failed: %v",
			newKeyID, oldKeyID, err)
		return out, nil
	}
	out.OldRemoved = true
	return out, nil
}

func writePasswordTemp(pw string) (string, error) {
	f, err := os.CreateTemp("", "jabali-restic-pw-*")
	if err != nil {
		return "", fmt.Errorf("tmpfile: %w", err)
	}
	if _, err := f.WriteString(pw); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	if err := os.Chmod(f.Name(), 0o600); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func resticBinary(cfg backup.ResticConfig) string {
	if cfg.Bin != "" {
		return cfg.Bin
	}
	return "restic"
}

func resticEnv(cfg backup.ResticConfig) []string {
	return append(os.Environ(), cfg.ExtraEnv...)
}

func resticKeyList(ctx context.Context, cfg backup.ResticConfig) ([]resticKey, error) {
	cmd := execCommandContext(ctx, resticBinary(cfg), "--repo", cfg.Repo,
		"--password-file", cfg.PasswordFile, "key", "list", "--json")
	cmd.Env = resticEnv(cfg)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%v: %s", err, stderrFromExitErr(err))
	}
	var keys []resticKey
	if err := json.Unmarshal(out, &keys); err != nil {
		return nil, fmt.Errorf("parse key list: %w", err)
	}
	return keys, nil
}

func resticKeyAdd(ctx context.Context, cfg backup.ResticConfig, newPasswordFile string) error {
	cmd := execCommandContext(ctx, resticBinary(cfg), "--repo", cfg.Repo,
		"--password-file", cfg.PasswordFile, "key", "add",
		"--new-password-file", newPasswordFile,
		"--user", "jabali", "--host", "jabali-panel")
	cmd.Env = resticEnv(cfg)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func resticKeyRemove(ctx context.Context, cfg backup.ResticConfig, keyID string) error {
	cmd := execCommandContext(ctx, resticBinary(cfg), "--repo", cfg.Repo,
		"--password-file", cfg.PasswordFile, "key", "remove", keyID)
	cmd.Env = resticEnv(cfg)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func keyIDSet(keys []resticKey) map[string]bool {
	out := make(map[string]bool, len(keys))
	for _, k := range keys {
		out[k.ID] = true
	}
	return out
}

func stderrFromExitErr(err error) string {
	if e, ok := err.(*exec.ExitError); ok {
		return strings.TrimSpace(string(e.Stderr))
	}
	return ""
}

func init() {
	Default.Register("backup.repo.password.rotate", backupRepoPasswordRotateHandler)
}
