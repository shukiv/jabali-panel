package commands

import (
	"context"
	"encoding/json"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func TestDBUserCreateHandler(t *testing.T) {
	tests := []struct {
		name      string
		input     dbUserCreateParams
		wantError bool
		wantCode  string
	}{
		{
			name: "invalid: empty username",
			input: dbUserCreateParams{
				DBUserName: "",
				Password:   "SecurePassword123!",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: starts with digit",
			input: dbUserCreateParams{
				DBUserName: "1user",
				Password:   "SecurePassword123!",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: contains quote",
			input: dbUserCreateParams{
				DBUserName: "bad'user",
				Password:   "SecurePassword123!",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: contains dot",
			input: dbUserCreateParams{
				DBUserName: "test.user",
				Password:   "SecurePassword123!",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: empty password",
			input: dbUserCreateParams{
				DBUserName: "testuser",
				Password:   "",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: too long username",
			input: dbUserCreateParams{
				DBUserName: "a1234567890123456789012345678901234567890123456789012345678901234",
				Password:   "SecurePassword123!",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, _ := json.Marshal(tt.input)

			_, err := dbUserCreateHandler(context.Background(), params)

			if (err != nil) != tt.wantError {
				t.Errorf("dbUserCreateHandler: expected error = %v, got %v", tt.wantError, err)
			}

			if tt.wantError && tt.wantCode != "" {
				var ae *agentwire.AgentError
				if !isAgentError(err, &ae) {
					t.Errorf("expected AgentError, got %T", err)
				} else if ae.Code != tt.wantCode {
					t.Errorf("expected code %q, got %q", tt.wantCode, ae.Code)
				}
			}
		})
	}
}
