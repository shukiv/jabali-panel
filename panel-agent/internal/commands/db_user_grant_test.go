package commands

import (
	"context"
	"encoding/json"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func TestDBUserGrantHandler(t *testing.T) {
	tests := []struct {
		name      string
		input     dbUserGrantParams
		wantError bool
		wantCode  string
	}{
		{
			name: "invalid: empty db_name",
			input: dbUserGrantParams{
				DBName:     "",
				DBUserName: "testuser",
				GrantLevel: "rw",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: db_name starts with digit",
			input: dbUserGrantParams{
				DBName:     "1testdb",
				DBUserName: "testuser",
				GrantLevel: "rw",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: db_name contains semicolon",
			input: dbUserGrantParams{
				DBName:     "test;db",
				DBUserName: "testuser",
				GrantLevel: "rw",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: empty username",
			input: dbUserGrantParams{
				DBName:     "testdb",
				DBUserName: "",
				GrantLevel: "rw",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: username starts with digit",
			input: dbUserGrantParams{
				DBName:     "testdb",
				DBUserName: "1user",
				GrantLevel: "rw",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: grant level empty",
			input: dbUserGrantParams{
				DBName:     "testdb",
				DBUserName: "testuser",
				GrantLevel: "",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: grant level not rw or ro and no privileges",
			input: dbUserGrantParams{
				DBName:     "testdb",
				DBUserName: "testuser",
				GrantLevel: "invalid",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: privileges with invalid token",
			input: dbUserGrantParams{
				DBName:     "testdb",
				DBUserName: "testuser",
				Privileges: []string{"INVALID"},
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, _ := json.Marshal(tt.input)

			_, err := dbUserGrantHandler(context.Background(), params)

			if (err != nil) != tt.wantError {
				t.Errorf("dbUserGrantHandler: expected error = %v, got %v", tt.wantError, err)
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
