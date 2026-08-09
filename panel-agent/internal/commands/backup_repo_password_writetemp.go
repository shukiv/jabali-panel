// M30.2.x — write a per-destination restic password to a temp file
// the agent owns. panel-api can't write 0640 root:jabali files under
// /run, so this command takes the plaintext over the agent socket
// and persists it to a short-lived tempfile that backup/restore
// flows pass via `password_file`.
//
// Cleanup: a sibling `backup.repo.password.cleanup_temp` removes the
// file (handler below). Caller MUST always pair write_temp with
// cleanup_temp; on agent crash the file is wiped anyway because
// /run is tmpfs and the agent's RuntimeDirectory is preserved=no.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// passwordTempDir is under /run so files vanish on reboot. Mode
// 0750 root:jabali so the panel-api process (jabali user) can list
// (it should never need to read; reads happen via /restic-key).
const passwordTempDir = "/run/jabali/restic-pw"

type backupRepoPasswordWriteTempParams struct {
	Password string `json:"password"`
}

type backupRepoPasswordWriteTempResult struct {
	Path string `json:"path"`
}

func backupRepoPasswordWriteTempHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p backupRepoPasswordWriteTempParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("malformed JSON: %v", err),
		}
	}
	if p.Password == "" {
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInvalidArgument, Message: "password required",
		}
	}
	if err := os.MkdirAll(passwordTempDir, 0o750); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("mkdir %s: %v", passwordTempDir, err),
		}
	}
	f, err := os.CreateTemp(passwordTempDir, "destpw-*")
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("create tempfile: %v", err),
		}
	}
	if _, err := f.WriteString(p.Password); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInternal, Message: err.Error(),
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInternal, Message: err.Error(),
		}
	}
	if err := os.Chmod(f.Name(), 0o640); err != nil {
		os.Remove(f.Name())
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInternal, Message: err.Error(),
		}
	}
	return backupRepoPasswordWriteTempResult{Path: f.Name()}, nil
}

type backupRepoPasswordCleanupTempParams struct {
	Path string `json:"path"`
}

func backupRepoPasswordCleanupTempHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p backupRepoPasswordCleanupTempParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("malformed JSON: %v", err),
		}
	}
	if err := removePasswordTempFile(p.Path); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

// removePasswordTempFile unlinks a per-destination password tempfile.
//
// Belt-and-suspenders: only allow removal of files we own underneath
// passwordTempDir. Prevents a misbehaving caller from asking the agent to
// unlink anything else on disk. A blank path is a no-op (the legacy
// shared-password path never writes a tempfile).
//
// Shared with the async backup orchestrator, which owns the file for the
// lifetime of its job — see runBackupOrchestrator.
func removePasswordTempFile(path string) *agentwire.AgentError {
	if path == "" {
		return nil
	}
	clean := filepath.Clean(path)
	if !strings.HasPrefix(clean, passwordTempDir+string(os.PathSeparator)) {
		return &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "path must be under " + passwordTempDir,
		}
	}
	if err := os.Remove(clean); err != nil && !os.IsNotExist(err) {
		return &agentwire.AgentError{
			Code: agentwire.CodeInternal, Message: err.Error(),
		}
	}
	return nil
}

func init() {
	Default.Register("backup.repo.password.write_temp", backupRepoPasswordWriteTempHandler)
	Default.Register("backup.repo.password.cleanup_temp", backupRepoPasswordCleanupTempHandler)
}
