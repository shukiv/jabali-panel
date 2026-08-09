package commands

import (
	"context"
	"encoding/json"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func TestValidateDocrootPath(t *testing.T) {
	requireHostMutationAllowed(t)
	tests := []struct {
		name    string
		osUser  string
		docroot string
		wantErr bool
	}{
		{
			name:    "valid: standard docroot",
			osUser:  "alice",
			docroot: "/home/alice/domains/example.com/public_html",
			wantErr: false,
		},
		{
			name:    "valid: relative path within domains",
			osUser:  "bob",
			docroot: "/home/bob/domains/test.local",
			wantErr: false,
		},
		{
			name:    "invalid: path traversal with ..",
			osUser:  "charlie",
			docroot: "/home/charlie/domains/../../etc/passwd",
			wantErr: true,
		},
		{
			name:    "invalid: outside home directory",
			osUser:  "dave",
			docroot: "/etc/wordpress",
			wantErr: true,
		},
		{
			name:    "invalid: different user",
			osUser:  "eve",
			docroot: "/home/frank/domains/site.com",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDocrootPath(tt.osUser, tt.docroot)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateDocrootPath: expected error = %v, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestWordPressInstallHandler_InvalidInput(t *testing.T) {
	requireHostMutationAllowed(t)
	tests := []struct {
		name      string
		input     wordpressInstallReq
		wantError bool
		wantCode  string
	}{
		{
			name: "invalid: missing os_user",
			input: wordpressInstallReq{
				OSUser:     "",
				Docroot:    "/home/alice/domains/test.com/public_html",
				DBName:     "wp_test",
				DBUser:     "wp_user",
				DBPassword: "password123",
				DBHost:     "localhost",
				SiteURL:    "https://test.com",
				SiteTitle:  "Test Site",
				AdminUser:  "admin",
				AdminPass:  "admin123",
				AdminEmail: "admin@test.com",
				Locale:     "en_US",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: missing docroot",
			input: wordpressInstallReq{
				OSUser:     "alice",
				Docroot:    "",
				DBName:     "wp_test",
				DBUser:     "wp_user",
				DBPassword: "password123",
				DBHost:     "localhost",
				SiteURL:    "https://test.com",
				SiteTitle:  "Test Site",
				AdminUser:  "admin",
				AdminPass:  "admin123",
				AdminEmail: "admin@test.com",
				Locale:     "en_US",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: missing db_password",
			input: wordpressInstallReq{
				OSUser:     "alice",
				Docroot:    "/home/alice/domains/test.com/public_html",
				DBName:     "wp_test",
				DBUser:     "wp_user",
				DBPassword: "",
				DBHost:     "localhost",
				SiteURL:    "https://test.com",
				SiteTitle:  "Test Site",
				AdminUser:  "admin",
				AdminPass:  "admin123",
				AdminEmail: "admin@test.com",
				Locale:     "en_US",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: missing admin_pass",
			input: wordpressInstallReq{
				OSUser:     "alice",
				Docroot:    "/home/alice/domains/test.com/public_html",
				DBName:     "wp_test",
				DBUser:     "wp_user",
				DBPassword: "password123",
				DBHost:     "localhost",
				SiteURL:    "https://test.com",
				SiteTitle:  "Test Site",
				AdminUser:  "admin",
				AdminPass:  "",
				AdminEmail: "admin@test.com",
				Locale:     "en_US",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: path traversal in docroot",
			input: wordpressInstallReq{
				OSUser:     "alice",
				Docroot:    "/home/alice/domains/../../../etc/passwd",
				DBName:     "wp_test",
				DBUser:     "wp_user",
				DBPassword: "password123",
				DBHost:     "localhost",
				SiteURL:    "https://test.com",
				SiteTitle:  "Test Site",
				AdminUser:  "admin",
				AdminPass:  "admin123",
				AdminEmail: "admin@test.com",
				Locale:     "en_US",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, _ := json.Marshal(tt.input)
			_, err := wordpressInstallHandler(context.Background(), params)

			if (err != nil) != tt.wantError {
				t.Errorf("wordpressInstallHandler: expected error = %v, got %v", tt.wantError, err)
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
