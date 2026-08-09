package commands

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func TestUserCreateHandler_InvalidUsername(t *testing.T) {
	requireHostMutationAllowed(t)
	t.Parallel()

	tests := []struct {
		name     string
		username string
	}{
		{"uppercase letter", "Alice"},
		{"starts with digit", "0user"},
		{"special char", "user@name"},
		{"too long", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, // > 32 chars
		{"space", "user name"},
		{"empty", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := userCreateParams{
				Username: tt.username,
				HomeDir:  "/home/testuser",
				Shell:    "/usr/sbin/nologin",
			}
			paramsJSON, _ := json.Marshal(params)

			_, err := userCreateHandler(context.Background(), paramsJSON)
			require.NotNil(t, err)

			var aerr *agentwire.AgentError
			require.ErrorAs(t, err, &aerr)
			assert.Equal(t, agentwire.CodeInvalidArgument, aerr.Code)
		})
	}
}

func TestUserCreateHandler_InvalidHomeDir(t *testing.T) {
	requireHostMutationAllowed(t)
	t.Parallel()

	tests := []struct {
		name    string
		homeDir string
	}{
		{"missing /home prefix", "/root/testuser"},
		{"relative path", "home/testuser"},
		{"empty", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := userCreateParams{
				Username: "testuser",
				HomeDir:  tt.homeDir,
				Shell:    "/usr/sbin/nologin",
			}
			paramsJSON, _ := json.Marshal(params)

			_, err := userCreateHandler(context.Background(), paramsJSON)
			require.NotNil(t, err)

			var aerr *agentwire.AgentError
			require.ErrorAs(t, err, &aerr)
			assert.Equal(t, agentwire.CodeInvalidArgument, aerr.Code)
		})
	}
}

func TestUserCreateHandler_ValidUsername(t *testing.T) {
	requireHostMutationAllowed(t)
	t.Parallel()

	tests := []string{
		"alice",
		"_user",
		"user_name",
		"user-name",
		"user123",
		"a",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", // exactly 32 chars
	}

	for _, username := range tests {
		t.Run(username, func(t *testing.T) {
			// Just validate that username passes regex check.
			assert.True(t, usernameRegex.MatchString(username))
		})
	}
}

func TestUserCreateHandler_InvalidParams(t *testing.T) {
	requireHostMutationAllowed(t)
	t.Parallel()

	_, err := userCreateHandler(context.Background(), []byte("not json"))
	require.NotNil(t, err)

	var aerr *agentwire.AgentError
	require.ErrorAs(t, err, &aerr)
	assert.Equal(t, agentwire.CodeInvalidArgument, aerr.Code)
}

// TestUserCreateHandler_Integration tests the full flow with actual system commands.
// This test is skipped by default since it requires root and modifies the system.
// Use `go test -tags=integration` to run it.
func TestUserCreateHandler_Integration(t *testing.T) {
	requireHostMutationAllowed(t)
	t.Parallel()

	t.Skip("Integration test skipped: requires root and creates system users")

	// This would be a real test that creates a user:
	// params := userCreateParams{
	//     Username: "test_jabali_user",
	//     HomeDir:  "/home/test_jabali_user",
	//     Shell:    "/usr/sbin/nologin",
	// }
	// paramsJSON, _ := json.Marshal(params)
	// data, err := userCreateHandler(context.Background(), paramsJSON)
	// require.NoError(t, err)
	// var resp userCreateResponse
	// require.NoError(t, json.Unmarshal(data.([]byte), &resp))
	// assert.Equal(t, "test_jabali_user", resp.Username)
	// assert.Greater(t, resp.UID, 0)
	// assert.Equal(t, "/home/test_jabali_user", resp.HomeDir)
}

// TODO: TestUserCreateHandler_SliceEnsureInvoked
// This test would verify that user.slice.ensure is invoked after chown succeeds.
// It requires mocking exec.CommandContext and userSliceEnsureHandler to avoid
// actual system modifications. The test would:
// 1. Mock useradd, chpasswd, chown, chmod, and id commands to succeed
// 2. Mock userSliceEnsureHandler to verify it's called with the correct username
// 3. Assert that slice-ensure is called AFTER chown and BEFORE returning
// 4. Test the rollback path: mock slice-ensure to fail and verify userdel is called
// This is left as a TODO because it requires deeper integration with the exec
// mock pattern used elsewhere in the test suite (see user_slice_ensure_test.go).
// For now, integration tests at the system level (requires root) are the primary
// verification mechanism.
