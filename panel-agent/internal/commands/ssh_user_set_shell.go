package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"os/user"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// ssh.user.set_shell — idempotent chsh wrapper.
//
// Reconciler calls this every sweep with the wrapper path
// /usr/local/bin/jabali-ssh-shell so every hosting user (SFTP and SSH)
// has the wrapper as their login shell. SFTP users still hit
// ForceCommand internal-sftp from the Match block; the wrapper is
// defense-in-depth for them. SSH users land in the wrapper which then
// reads /etc/jabali/ssh-sandbox-mode and either bwrap's or sudo's into
// systemd-nspawn.
//
// Skips chsh when the user's current shell already matches.

type sshUserSetShellParams struct {
	Username string `json:"username"`
	Shell    string `json:"shell"`
}

type sshUserSetShellResponse struct {
	Username  string `json:"username"`
	Shell     string `json:"shell"`
	Changed   bool   `json:"changed"`
	WasShell  string `json:"was_shell,omitempty"`
}

func sshUserSetShellHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p sshUserSetShellParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	if p.Username == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "username is required",
		}
	}
	if !strings.HasPrefix(p.Shell, "/") {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "shell must be an absolute path",
		}
	}

	// GH #658: never chsh a protected system account (root, the service
	// user) into the tenant sandbox shell. A mis-created "root" panel user
	// (GH #429) drove exactly this — jabali-ssh-shell then refused root's
	// non-/home home and fell back to nologin, locking the operator out of
	// SSH. Shells of system accounts are the OS's business, not the panel's.
	if protectedUsers[p.Username] {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodePermissionDenied,
			Message: fmt.Sprintf("refusing to change the login shell of protected system user %q", p.Username),
		}
	}

	// Look up current shell from /etc/passwd. os/user.Lookup doesn't
	// expose the shell field; fall back to getent.
	cur, err := getentShell(ctx, p.Username)
	if err != nil {
		return nil, err
	}
	if cur == p.Shell {
		return &sshUserSetShellResponse{
			Username: p.Username,
			Shell:    p.Shell,
			Changed:  false,
			WasShell: cur,
		}, nil
	}

	// Verify the user actually exists before chsh — chsh emits a
	// distinguishable error otherwise but we can fail earlier with our
	// own code.
	if _, err := user.Lookup(p.Username); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeNotFound,
			Message: fmt.Sprintf("user %q not found: %v", p.Username, err),
		}
	}

	cmd := exec.CommandContext(ctx, "chsh", "-s", p.Shell, p.Username)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("chsh -s %s %s: %v (stderr: %s)", p.Shell, p.Username, err, stderr.String()),
		}
	}
	return &sshUserSetShellResponse{
		Username: p.Username,
		Shell:    p.Shell,
		Changed:  true,
		WasShell: cur,
	}, nil
}

// getentShell returns the seventh field of the user's passwd row.
func getentShell(ctx context.Context, username string) (string, *agentwire.AgentError) {
	cmd := exec.CommandContext(ctx, "getent", "passwd", username)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", &agentwire.AgentError{
			Code:    agentwire.CodeNotFound,
			Message: fmt.Sprintf("getent passwd %s: %v (stderr: %s)", username, err, stderr.String()),
		}
	}
	line := strings.TrimRight(stdout.String(), "\n")
	parts := strings.Split(line, ":")
	if len(parts) < 7 {
		return "", &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("malformed passwd row for %s", username),
		}
	}
	return parts[6], nil
}

func init() {
	Default.Register("ssh.user.set_shell", sshUserSetShellHandler)
}
