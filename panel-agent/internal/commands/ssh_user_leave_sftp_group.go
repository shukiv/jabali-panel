package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// ssh.user.leave_sftp_group — opposite of ssh.user.join_sftp_group.
// Removes the user from the jabali-sftp group so the Match Group block
// in /etc/ssh/sshd_config.d/jabali-sftp.conf no longer applies and the
// user gets a normal shell session on connect. Used by the SSH-keys
// reconciler when the user's hosting package has ssh_enabled=true.

type sshUserLeaveSFTPGroupParams struct {
	Username string `json:"username"`
}

type sshUserLeaveSFTPGroupResponse struct {
	Username  string `json:"username"`
	Left      bool   `json:"left,omitempty"`
	NotMember bool   `json:"not_member,omitempty"`
}

func sshUserLeaveSFTPGroupHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p sshUserLeaveSFTPGroupParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	isMember, err := isUserInGroup(ctx, p.Username, sftpGroupName)
	if err != nil {
		return nil, err
	}
	if !isMember {
		return &sshUserLeaveSFTPGroupResponse{Username: p.Username, NotMember: true}, nil
	}

	// gpasswd -d <user> <group> removes a single supplementary group
	// without disturbing the user's other memberships. usermod -G would
	// require listing every other group the user must keep — fragile and
	// racy on multi-group accounts.
	cmd := execCommandContext(ctx, "gpasswd", "-d", p.Username, sftpGroupName)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to remove %q from %q: %v (stderr: %s)", p.Username, sftpGroupName, err, stderr.String()),
		}
	}
	return &sshUserLeaveSFTPGroupResponse{Username: p.Username, Left: true}, nil
}

func init() {
	Default.Register("ssh.user.leave_sftp_group", sshUserLeaveSFTPGroupHandler)
}
