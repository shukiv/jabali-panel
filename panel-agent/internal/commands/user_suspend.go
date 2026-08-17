package commands

// user.suspend / user.unsuspend — OS-side complement to the panel
// suspend cascade. Suspend pulls the user out of the jabali-sftp
// group so SFTP connections are refused (Match Group jabali-sftp in
// /etc/ssh/sshd_config.d/jabali-sftp.conf gates the chroot block) +
// runs `usermod -L` so password-auth SSH/su is rejected as well.
// Unsuspend reverses both.
//
// Idempotent: both handlers tolerate the target user already being
// in the desired state.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// sftpGroupName lives in ssh_user_join_sftp_group.go.

type userSuspendParams struct {
	Username string `json:"username"`
}

type userSuspendResponse struct {
	Username        string `json:"username"`
	SFTPGroupRemoved bool   `json:"sftp_group_removed,omitempty"`
	PasswordLocked   bool   `json:"password_locked,omitempty"`
	SFTPGroupAdded   bool   `json:"sftp_group_added,omitempty"`
	PasswordUnlocked bool   `json:"password_unlocked,omitempty"`
	Note             string `json:"note,omitempty"`
}

func userSuspendHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p userSuspendParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	if !looksLikeUnixUsername(p.Username) {
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInvalidArgument, Message: "invalid username",
		}
	}
	resp := &userSuspendResponse{Username: p.Username}

	// 1. Remove from jabali-sftp. gpasswd -d returns non-zero when not
	// a member, which is fine — we treat both as "OK, not a member".
	inSFTP, gErr := isUserInGroup(ctx, p.Username, sftpGroupName)
	if gErr != nil {
		// isUserInGroup errs only when getent fails — record + keep going.
		resp.Note = fmt.Sprintf("group lookup failed: %v", gErr)
	}
	if inSFTP {
		cmd := execCommandContext(ctx, "gpasswd", "-d", p.Username, sftpGroupName)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("gpasswd -d %s %s: %v (stderr: %s)", p.Username, sftpGroupName, err, stderr.String()),
			}
		}
		resp.SFTPGroupRemoved = true
	}

	// 2. usermod -L locks the password (prefixes shadow hash with !).
	// Reversible via -U on unsuspend. Idempotent: locking a locked
	// account is a no-op.
	cmd := execCommandContext(ctx, "usermod", "-L", p.Username)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("usermod -L %s: %v (stderr: %s)", p.Username, err, stderr.String()),
		}
	}
	resp.PasswordLocked = true
	return resp, nil
}

func userUnsuspendHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p userSuspendParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	if !looksLikeUnixUsername(p.Username) {
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInvalidArgument, Message: "invalid username",
		}
	}
	resp := &userSuspendResponse{Username: p.Username}

	// 1. Add back to jabali-sftp. usermod -aG is idempotent.
	cmd := execCommandContext(ctx, "usermod", "-aG", sftpGroupName, p.Username)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("usermod -aG %s %s: %v (stderr: %s)", sftpGroupName, p.Username, err, stderr.String()),
		}
	}
	resp.SFTPGroupAdded = true

	// 2. usermod -U unlocks (strips the leading !).
	cmd = execCommandContext(ctx, "usermod", "-U", p.Username)
	stderr.Reset()
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("usermod -U %s: %v (stderr: %s)", p.Username, err, stderr.String()),
		}
	}
	resp.PasswordUnlocked = true
	return resp, nil
}

func init() {
	Default.Register("user.suspend", userSuspendHandler)
	Default.Register("user.unsuspend", userUnsuspendHandler)
}
