package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// sshUserJoinSFTPGroupParams is the input shape for ssh.user.join_sftp_group.
type sshUserJoinSFTPGroupParams struct {
	Username string `json:"username"`
}

// sshUserJoinSFTPGroupResponse is the output shape for ssh.user.join_sftp_group.
type sshUserJoinSFTPGroupResponse struct {
	Username      string `json:"username"`
	Joined        bool   `json:"joined,omitempty"`
	AlreadyMember bool   `json:"already_member,omitempty"`
}

const sftpGroupName = "jabali-sftp"

func sshUserJoinSFTPGroupHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p sshUserJoinSFTPGroupParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// Never let a uid-0 / root account join jabali-sftp. sshd's
	// `Match Group jabali-sftp` block applies ForceCommand internal-sftp +
	// ChrootDirectory /home/%u; a root login then chroots into a
	// nonexistent /home/root, the session can't be set up, and the
	// connection resets right after authentication — host SSH is bricked
	// with no shell. Guarded here so no caller (however buggy) can do it.
	if gerr := refuseRootSFTP(ctx, p.Username); gerr != nil {
		return nil, gerr
	}

	// Check if user is already a member of jabali-sftp group
	isMember, err := isUserInGroup(ctx, p.Username, sftpGroupName)
	if err != nil {
		return nil, err
	}

	if isMember {
		// Already a member; idempotent success
		return &sshUserJoinSFTPGroupResponse{
			Username:      p.Username,
			AlreadyMember: true,
		}, nil
	}

	// Add user to the group via usermod -aG jabali-sftp <username>
	usermodCmd := execCommandContext(ctx, "usermod", "-aG", sftpGroupName, p.Username)
	var usermodOut, usermodErr bytes.Buffer
	usermodCmd.Stdout = &usermodOut
	usermodCmd.Stderr = &usermodErr

	if err := usermodCmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to add %q to group %q: %v (stderr: %s)", p.Username, sftpGroupName, err, usermodErr.String()),
		}
	}

	return &sshUserJoinSFTPGroupResponse{
		Username: p.Username,
		Joined:   true,
	}, nil
}

// isUserInGroup checks if a user is a member of a group.
// Uses `id -nG <username>` to get the list of group names.
func isUserInGroup(ctx context.Context, username, groupName string) (bool, *agentwire.AgentError) {
	idCmd := execCommandContext(ctx, "id", "-nG", username)
	var idOut, idErr bytes.Buffer
	idCmd.Stdout = &idOut
	idCmd.Stderr = &idErr

	if err := idCmd.Run(); err != nil {
		return false, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to check groups for user %q: %v", username, err),
		}
	}

	// id -nG returns space-separated group names
	groups := strings.Fields(idOut.String())
	for _, g := range groups {
		if g == groupName {
			return true, nil
		}
	}

	return false, nil
}

// refuseRootSFTP rejects empty, "root", or any uid-0 username from joining
// jabali-sftp (see the call site for why this bricks SSH).
func refuseRootSFTP(ctx context.Context, username string) *agentwire.AgentError {
	if username == "" {
		return &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "username required"}
	}
	if username == "root" {
		return &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "refusing to add root to " + sftpGroupName + " (would brick host SSH)"}
	}
	out, err := execCommandContext(ctx, "id", "-u", username).Output()
	if err != nil {
		return &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("failed to resolve uid for %q: %v", username, err)}
	}
	if strings.TrimSpace(string(out)) == "0" {
		return &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "refusing to add a uid-0 account to " + sftpGroupName + " (would brick host SSH)"}
	}
	return nil
}

func init() {
	Default.Register("ssh.user.join_sftp_group", sshUserJoinSFTPGroupHandler)
}
