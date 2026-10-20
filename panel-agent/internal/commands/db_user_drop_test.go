package commands

import (
	"context"
	"encoding/json"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func TestDBUserDropHandler(t *testing.T) {
	tests := []struct {
		name      string
		input     dbUserDropParams
		wantError bool
		wantCode  string
	}{
		{
			name: "invalid: empty username",
			input: dbUserDropParams{
				DBUserName: "",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: starts with digit",
			input: dbUserDropParams{
				DBUserName: "1user",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: contains quote",
			input: dbUserDropParams{
				DBUserName: "test'user",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: too long",
			input: dbUserDropParams{
				DBUserName: "a1234567890123456789012345678901234567890123456789012345678901234",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, _ := json.Marshal(tt.input)

			_, err := dbUserDropHandler(context.Background(), params)

			if (err != nil) != tt.wantError {
				t.Errorf("dbUserDropHandler: expected error = %v, got %v", tt.wantError, err)
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
