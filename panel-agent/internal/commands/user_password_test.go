package commands

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func TestUserPasswordHandler_RejectsInvalidUsername(t *testing.T) {
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
			params := userPasswordParams{
				Username: tt.username,
				Password: "newpassword",
			}
			paramsJSON, _ := json.Marshal(params)

			_, err := userPasswordHandler(context.Background(), paramsJSON)
			require.NotNil(t, err)

			var aerr *agentwire.AgentError
			require.ErrorAs(t, err, &aerr)
			assert.Equal(t, agentwire.CodeInvalidArgument, aerr.Code)
		})
	}
}

func TestUserPasswordHandler_RejectsProtectedUser(t *testing.T) {
	requireHostMutationAllowed(t)
	t.Parallel()

	tests := []string{
		"root",
		"jabali",
	}

	for _, username := range tests {
		t.Run(username, func(t *testing.T) {
			params := userPasswordParams{
				Username: username,
				Password: "newpassword",
			}
			paramsJSON, _ := json.Marshal(params)

			_, err := userPasswordHandler(context.Background(), paramsJSON)
			require.NotNil(t, err)

			var aerr *agentwire.AgentError
			require.ErrorAs(t, err, &aerr)
			assert.Equal(t, agentwire.CodePermissionDenied, aerr.Code)
			assert.Contains(t, aerr.Message, "protected")
		})
	}
}

func TestUserPasswordHandler_RejectsEmptyPassword(t *testing.T) {
	requireHostMutationAllowed(t)
	t.Parallel()

	params := userPasswordParams{
		Username: "alice",
		Password: "",
	}
	paramsJSON, _ := json.Marshal(params)

	_, err := userPasswordHandler(context.Background(), paramsJSON)
	require.NotNil(t, err)

	var aerr *agentwire.AgentError
	require.ErrorAs(t, err, &aerr)
	assert.Equal(t, agentwire.CodeInvalidArgument, aerr.Code)
}
