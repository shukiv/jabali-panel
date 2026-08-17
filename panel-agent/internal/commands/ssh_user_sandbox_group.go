package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// ssh.user.join_sandbox_group / ssh.user.leave_sandbox_group — manage
// membership in jabali-ssh-sandbox.
//
// Members of jabali-ssh-sandbox are allowed to sudo-exec
// /usr/local/bin/jabali-nspawn-enter (see /etc/sudoers.d/jabali-nspawn).
// Reconciler keeps the group in sync with package.ssh_enabled — only
// SSH-shell users get this privilege.

const sandboxGroupName = "jabali-ssh-sandbox"

type sshUserSandboxGroupParams struct {
	Username string `json:"username"`
}

type sshUserSandboxGroupResponse struct {
	Username      string `json:"username"`
	Joined        bool   `json:"joined,omitempty"`
	Left          bool   `json:"left,omitempty"`
	AlreadyMember bool   `json:"already_member,omitempty"`
	NotMember     bool   `json:"not_member,omitempty"`
}

func sshUserJoinSandboxGroupHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p sshUserSandboxGroupParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	isMember, gErr := isUserInGroup(ctx, p.Username, sandboxGroupName)
	if gErr != nil {
		return nil, gErr
	}
	if isMember {
		return &sshUserSandboxGroupResponse{Username: p.Username, AlreadyMember: true}, nil
	}
	cmd := execCommandContext(ctx, "usermod", "-aG", sandboxGroupName, p.Username)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("usermod -aG %s %s: %v (stderr: %s)", sandboxGroupName, p.Username, err, stderr.String()),
		}
	}
	return &sshUserSandboxGroupResponse{Username: p.Username, Joined: true}, nil
}

func sshUserLeaveSandboxGroupHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p sshUserSandboxGroupParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	isMember, gErr := isUserInGroup(ctx, p.Username, sandboxGroupName)
	if gErr != nil {
		return nil, gErr
	}
	if !isMember {
		return &sshUserSandboxGroupResponse{Username: p.Username, NotMember: true}, nil
	}
	cmd := execCommandContext(ctx, "gpasswd", "-d", p.Username, sandboxGroupName)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("gpasswd -d %s %s: %v (stderr: %s)", p.Username, sandboxGroupName, err, stderr.String()),
		}
	}
	return &sshUserSandboxGroupResponse{Username: p.Username, Left: true}, nil
}

func init() {
	Default.Register("ssh.user.join_sandbox_group", sshUserJoinSandboxGroupHandler)
	Default.Register("ssh.user.leave_sandbox_group", sshUserLeaveSandboxGroupHandler)
}
