package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// ssh.user.join_forward_group / ssh.user.leave_forward_group — manage membership
// in jabali-ssh-forward (GH #1229).
//
// A hosting user in jabali-ssh-forward is EXCLUDED from the JAB-352 forwarding
// lockdown (Match Group jabali-ssh-sandbox,!jabali-ssh-forward) and instead gets
// local/dynamic loopback TCP forwarding, enough for VS Code Remote-SSH to reach
// its own VS Code Server. The sensitive loopback services stay firewall-blocked
// per-uid, so this never re-opens the tunneling vector. Opt-in, operator-driven.
//
// No sshd reload is needed: sshd evaluates Match Group against the user's current
// group membership at connection time, so the next SSH connection picks it up.

const forwardGroupName = "jabali-ssh-forward"

type sshUserForwardGroupParams struct {
	Username string `json:"username"`
}

type sshUserForwardGroupResponse struct {
	Username      string `json:"username"`
	Joined        bool   `json:"joined,omitempty"`
	Left          bool   `json:"left,omitempty"`
	AlreadyMember bool   `json:"already_member,omitempty"`
	NotMember     bool   `json:"not_member,omitempty"`
}

func sshUserJoinForwardGroupHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p sshUserForwardGroupParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("failed to parse params: %v", err)}
	}
	isMember, gErr := isUserInGroup(ctx, p.Username, forwardGroupName)
	if gErr != nil {
		return nil, gErr
	}
	if isMember {
		return &sshUserForwardGroupResponse{Username: p.Username, AlreadyMember: true}, nil
	}
	cmd := execCommandContext(ctx, "usermod", "-aG", forwardGroupName, p.Username)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("usermod -aG %s %s: %v (stderr: %s)", forwardGroupName, p.Username, err, stderr.String())}
	}
	return &sshUserForwardGroupResponse{Username: p.Username, Joined: true}, nil
}

func sshUserLeaveForwardGroupHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p sshUserForwardGroupParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("failed to parse params: %v", err)}
	}
	isMember, gErr := isUserInGroup(ctx, p.Username, forwardGroupName)
	if gErr != nil {
		return nil, gErr
	}
	if !isMember {
		return &sshUserForwardGroupResponse{Username: p.Username, NotMember: true}, nil
	}
	cmd := execCommandContext(ctx, "gpasswd", "-d", p.Username, forwardGroupName)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("gpasswd -d %s %s: %v (stderr: %s)", p.Username, forwardGroupName, err, stderr.String())}
	}
	return &sshUserForwardGroupResponse{Username: p.Username, Left: true}, nil
}

func init() {
	Default.Register("ssh.user.join_forward_group", sshUserJoinForwardGroupHandler)
	Default.Register("ssh.user.leave_forward_group", sshUserLeaveForwardGroupHandler)
}
