package commands

import (
	"context"
	"encoding/json"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func TestDBUserRotatePasswordHandler(t *testing.T) {
	tests := []struct {
		name      string
		input     dbUserRotatePasswordParams
		wantError bool
		wantCode  string
	}{
		{
			name: "invalid: empty username",
			input: dbUserRotatePasswordParams{
				DBUserName:  "",
				NewPassword: "NewSecurePassword123!",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: username starts with digit",
			input: dbUserRotatePasswordParams{
				DBUserName:  "1user",
				NewPassword: "NewSecurePassword123!",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: username contains space",
			input: dbUserRotatePasswordParams{
				DBUserName:  "test user",
				NewPassword: "NewSecurePassword123!",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: empty password",
			input: dbUserRotatePasswordParams{
				DBUserName:  "testuser",
				NewPassword: "",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: username too long",
			input: dbUserRotatePasswordParams{
				DBUserName:  "a1234567890123456789012345678901234567890123456789012345678901234",
				NewPassword: "NewSecurePassword123!",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, _ := json.Marshal(tt.input)

			_, err := dbUserRotatePasswordHandler(context.Background(), params)

			if (err != nil) != tt.wantError {
				t.Errorf("dbUserRotatePasswordHandler: expected error = %v, got %v", tt.wantError, err)
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
