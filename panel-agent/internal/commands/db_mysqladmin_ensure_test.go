package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func TestDBMysqladminEnsureHandler(t *testing.T) {
	tests := []struct {
		name      string
		input     dbMysqladminEnsureParams
		wantError bool
		wantCode  string
	}{
		{
			name: "invalid: empty username",
			input: dbMysqladminEnsureParams{
				PanelUsername: "",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: starts with digit",
			input: dbMysqladminEnsureParams{
				PanelUsername: "1alice",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: starts with underscore",
			input: dbMysqladminEnsureParams{
				PanelUsername: "_alice",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: contains uppercase",
			input: dbMysqladminEnsureParams{
				PanelUsername: "Alice",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: contains dot",
			input: dbMysqladminEnsureParams{
				PanelUsername: "alice.test",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: too long (33 chars)",
			input: dbMysqladminEnsureParams{
				PanelUsername: "abcdefghijklmnopqrstuvwxyz1234567",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: contains double dot",
			input: dbMysqladminEnsureParams{
				PanelUsername: "alice..test",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: contains backslash",
			input: dbMysqladminEnsureParams{
				PanelUsername: "alice\\test",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: contains single quote",
			input: dbMysqladminEnsureParams{
				PanelUsername: "alice'test",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: contains double quote",
			input: dbMysqladminEnsureParams{
				PanelUsername: "alice\"test",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: contains space",
			input: dbMysqladminEnsureParams{
				PanelUsername: "alice test",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: contains forward slash",
			input: dbMysqladminEnsureParams{
				PanelUsername: "alice/test",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: contains dot",
			input: dbMysqladminEnsureParams{
				PanelUsername: "alice.test",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: contains semicolon",
			input: dbMysqladminEnsureParams{
				PanelUsername: "alice;test",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: contains newline",
			input: dbMysqladminEnsureParams{
				PanelUsername: "alice\ntest",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: contains carriage return",
			input: dbMysqladminEnsureParams{
				PanelUsername: "alice\rtest",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, _ := json.Marshal(tt.input)

			_, err := dbMysqladminEnsureHandler(context.Background(), params)

			if (err != nil) != tt.wantError {
				t.Errorf("dbMysqladminEnsureHandler: expected error = %v, got %v", tt.wantError, err)
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

// TestDBMysqladminEnsurePasswordCharset verifies password generation uses only valid chars.
func TestDBMysqladminEnsurePasswordCharset(t *testing.T) {
	for i := 0; i < 50; i++ {
		password, err := generateMysqladminPassword()
		if err != nil {
			t.Fatalf("generateMysqladminPassword failed: %v", err)
		}

		if len(password) != 32 {
			t.Errorf("iteration %d: password length = %d, want 32", i, len(password))
		}

		for _, ch := range password {
			if !bytes.ContainsRune([]byte(mysqladminPasswordCharset), ch) {
				t.Errorf("iteration %d: password contains invalid character %q", i, ch)
			}
		}
	}
}

// TestDBMysqladminEnsurePasswordNonDeterminism verifies successive calls produce different passwords.
func TestDBMysqladminEnsurePasswordNonDeterminism(t *testing.T) {
	pw1, err1 := generateMysqladminPassword()
	pw2, err2 := generateMysqladminPassword()

	if err1 != nil || err2 != nil {
		t.Fatalf("generateMysqladminPassword failed: %v, %v", err1, err2)
	}

	if pw1 == pw2 {
		t.Errorf("two successive generateMysqladminPassword calls produced the same password: %q", pw1)
	}
}
