package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/user"
	"strconv"
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
//
// The wrapper is the only shell it sets, and only for a hosting user: a
// plain account name, a uid in the tenant range, and not an FTP subaccount
// alias (those keep nologin). The agent checks this itself rather than
// trusting the caller.

// sshTenantShell is the one login shell the panel gives a hosting user.
const sshTenantShell = "/usr/local/bin/jabali-ssh-shell"

type sshUserSetShellParams struct {
	Username string `json:"username"`
	Shell    string `json:"shell"`
}

type sshUserSetShellResponse struct {
	Username string `json:"username"`
	Shell    string `json:"shell"`
	Changed  bool   `json:"changed"`
	WasShell string `json:"was_shell,omitempty"`
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
	if !usernameRegex.MatchString(p.Username) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid username %q", p.Username),
		}
	}
	if p.Shell != sshTenantShell {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("shell must be %s", sshTenantShell),
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

	u, err := user.Lookup(p.Username)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeNotFound,
			Message: fmt.Sprintf("user %q not found: %v", p.Username, err),
		}
	}
	if aerr := sshShellAccount(u); aerr != nil {
		return nil, aerr
	}

	// Look up current shell from /etc/passwd. os/user.Lookup doesn't
	// expose the shell field; fall back to getent.
	cur, gerr := getentShell(ctx, p.Username)
	if gerr != nil {
		return nil, gerr
	}
	if cur == p.Shell {
		return &sshUserSetShellResponse{
			Username: p.Username,
			Shell:    p.Shell,
			Changed:  false,
			WasShell: cur,
		}, nil
	}

	cmd := execCommandContext(ctx, "chsh", "-s", p.Shell, p.Username)
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

// sshShellAccount refuses an account that isn't a hosting user: a uid
// outside the tenant range (root, system and service accounts, nobody), or
// an FTP subaccount alias, which shares or sits next to a tenant's uid but
// must keep nologin.
func sshShellAccount(u *user.User) *agentwire.AgentError {
	uid, err := strconv.Atoi(u.Uid)
	if err != nil || uid < minTenantUID || uid >= 60000 {
		return &agentwire.AgentError{
			Code:    agentwire.CodePermissionDenied,
			Message: fmt.Sprintf("refusing to change the login shell of %q: uid %s is not a hosting user's", u.Username, u.Uid),
		}
	}
	if _, alias := ftpMarkerOwner(u.Name); alias {
		return &agentwire.AgentError{
			Code:    agentwire.CodePermissionDenied,
			Message: fmt.Sprintf("refusing to change the login shell of FTP account %q", u.Username),
		}
	}
	return nil
}

// getentShell returns the seventh field of the user's passwd row.
func getentShell(ctx context.Context, username string) (string, *agentwire.AgentError) {
	cmd := execCommandContext(ctx, "getent", "passwd", username)
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
