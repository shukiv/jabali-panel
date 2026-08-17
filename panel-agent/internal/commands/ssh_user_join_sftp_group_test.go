package commands

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func TestSSHUserJoinSFTPGroup_InvalidUser(t *testing.T) {
	withRealExec(t) // GH #994: needs the real read-only command (stub returns empty output)
	ctx := context.Background()

	params, _ := json.Marshal(sshUserJoinSFTPGroupParams{
		Username: "nonexistent-user-xyz-12345",
	})

	_, err := sshUserJoinSFTPGroupHandler(ctx, params)
	require.Error(t, err, "should reject nonexistent user")
}

func TestSSHUserJoinSFTPGroup_InvalidJSON(t *testing.T) {
	ctx := context.Background()

	_, err := sshUserJoinSFTPGroupHandler(ctx, []byte("invalid json"))
	require.Error(t, err, "should reject invalid JSON")
}

func TestSSHUserJoinSFTPGroup_RefusesRoot(t *testing.T) {
	ctx := context.Background()

	// Adding root (uid 0) to jabali-sftp applies sshd's Match Group
	// ForceCommand internal-sftp + ChrootDirectory to root logins and bricks
	// host SSH. The handler must refuse it outright, before any usermod.
	params, _ := json.Marshal(sshUserJoinSFTPGroupParams{Username: "root"})
	_, err := sshUserJoinSFTPGroupHandler(ctx, params)
	require.Error(t, err, "root must never be added to jabali-sftp")
	require.Contains(t, err.(*agentwire.AgentError).Message, "root")
}

func TestSSHUserJoinSFTPGroup_RefusesEmpty(t *testing.T) {
	ctx := context.Background()
	params, _ := json.Marshal(sshUserJoinSFTPGroupParams{Username: ""})
	_, err := sshUserJoinSFTPGroupHandler(ctx, params)
	require.Error(t, err, "empty username must be rejected")
}
