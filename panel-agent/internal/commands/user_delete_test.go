package commands

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func TestUserDeleteHandler_InvalidUsername(t *testing.T) {
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
			params := userDeleteParams{
				Username:   tt.username,
				RemoveHome: false,
			}
			paramsJSON, _ := json.Marshal(params)

			_, err := userDeleteHandler(context.Background(), paramsJSON)
			require.NotNil(t, err)

			var aerr *agentwire.AgentError
			require.ErrorAs(t, err, &aerr)
			assert.Equal(t, agentwire.CodeInvalidArgument, aerr.Code)
		})
	}
}

func TestUserDeleteHandler_ProtectedUsers(t *testing.T) {
	requireHostMutationAllowed(t)
	t.Parallel()

	tests := []string{
		"root",
		"jabali",
	}

	for _, username := range tests {
		t.Run(username, func(t *testing.T) {
			params := userDeleteParams{
				Username:   username,
				RemoveHome: false,
			}
			paramsJSON, _ := json.Marshal(params)

			_, err := userDeleteHandler(context.Background(), paramsJSON)
			require.NotNil(t, err)

			var aerr *agentwire.AgentError
			require.ErrorAs(t, err, &aerr)
			assert.Equal(t, agentwire.CodePermissionDenied, aerr.Code)
			assert.Contains(t, aerr.Message, "protected")
		})
	}
}

func TestUserDeleteHandler_InvalidParams(t *testing.T) {
	requireHostMutationAllowed(t)
	t.Parallel()

	_, err := userDeleteHandler(context.Background(), []byte("not json"))
	require.NotNil(t, err)

	var aerr *agentwire.AgentError
	require.ErrorAs(t, err, &aerr)
	assert.Equal(t, agentwire.CodeInvalidArgument, aerr.Code)
}

func TestUserDeleteHandler_ValidUsername(t *testing.T) {
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

// TestUserDeleteHandler_Integration tests the full flow with actual system commands.
// This test is skipped by default since it requires root and modifies the system.
func TestUserDeleteHandler_Integration(t *testing.T) {
	requireHostMutationAllowed(t)
	t.Parallel()

	t.Skip("Integration test skipped: requires root and deletes system users")

	// This would be a real test that creates and then deletes a user:
	// First create a user, then delete it.
	// params := userDeleteParams{
	//     Username:   "test_jabali_user",
	//     RemoveHome: true,
	// }
	// paramsJSON, _ := json.Marshal(params)
	// data, err := userDeleteHandler(context.Background(), paramsJSON)
	// require.NoError(t, err)
	// var resp userDeleteResponse
	// require.NoError(t, json.Unmarshal(data.([]byte), &resp))
	// assert.Equal(t, "test_jabali_user", resp.Username)
	// assert.True(t, resp.RemovedHome)
}

// TODO: TestUserDeleteHandler_SliceRemoveInvoked
// This test would verify that user.slice.remove is invoked BEFORE userdel.
// It requires mocking exec.CommandContext and userSliceRemoveHandler to avoid
// actual system modifications and to verify call ordering. The test would:
// 1. Mock the id command to return success (user exists)
// 2. Mock userSliceRemoveHandler to verify it's called with the correct username
// 3. Mock userdel to verify it's called AFTER slice-remove
// 4. Test the failure path: mock slice-remove to fail and verify userdel is NOT called
// This is left as a TODO because it requires deeper integration with the exec
// mock pattern and call-ordering verification (see user_slice_remove_test.go).
// For now, integration tests at the system level (requires root) are the primary
// verification mechanism.
