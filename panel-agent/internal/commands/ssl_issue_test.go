package commands

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func TestSSLIssueValidation_InvalidDomain(t *testing.T) {
	tests := []struct {
		domain string
		name   string
	}{
		{"../evil", "parent dir traversal"},
		{"..", "dot dot"},
		{"", "empty"},
		{"a" + string(make([]byte, 300)), "too long"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := sslIssueParams{
				Domain:  tt.domain,
				Webroot: "/var/www/test",
				Email:   "admin@example.com",
				Staging: false,
			}
			paramsJSON, _ := json.Marshal(params)

			result, err := sslIssueHandler(context.Background(), paramsJSON)

			if err == nil {
				t.Fatal("expected error for invalid domain")
			}
			if result != nil {
				t.Fatal("expected nil result")
			}
			if agentErr, ok := err.(*agentwire.AgentError); ok {
				if agentErr.Code != agentwire.CodeInvalidArgument {
					t.Errorf("expected CodeInvalidArgument, got %s", agentErr.Code)
				}
			} else {
				t.Fatal("expected AgentError")
			}
		})
	}
}

func TestSSLIssueValidation_InvalidWebroot(t *testing.T) {
	tests := []struct {
		webroot string
		name    string
	}{
		{"relative/path", "relative path"},
		{"/etc/passwd", "outside allowed prefixes"},
		{"/root/test", "outside allowed prefixes"},
		{"", "empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := sslIssueParams{
				Domain:  "example.com",
				Webroot: tt.webroot,
				Email:   "admin@example.com",
				Staging: false,
			}
			paramsJSON, _ := json.Marshal(params)

			result, err := sslIssueHandler(context.Background(), paramsJSON)

			if err == nil {
				t.Fatal("expected error for invalid webroot")
			}
			if result != nil {
				t.Fatal("expected nil result")
			}
			if agentErr, ok := err.(*agentwire.AgentError); ok {
				if agentErr.Code != agentwire.CodeInvalidArgument {
					t.Errorf("expected CodeInvalidArgument, got %s", agentErr.Code)
				}
			} else {
				t.Fatal("expected AgentError")
			}
		})
	}
}

func TestSSLIssueValidation_InvalidEmail(t *testing.T) {
	tests := []struct {
		email string
		name  string
	}{
		{"not-an-email", "missing @"},
		{"@example.com", "missing local part"},
		{"admin@", "missing domain"},
		{"", "empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := sslIssueParams{
				Domain:  "example.com",
				Webroot: "/var/www/test",
				Email:   tt.email,
				Staging: false,
			}
			paramsJSON, _ := json.Marshal(params)

			result, err := sslIssueHandler(context.Background(), paramsJSON)

			if err == nil {
				t.Fatal("expected error for invalid email")
			}
			if result != nil {
				t.Fatal("expected nil result")
			}
			if agentErr, ok := err.(*agentwire.AgentError); ok {
				if agentErr.Code != agentwire.CodeInvalidArgument {
					t.Errorf("expected CodeInvalidArgument, got %s", agentErr.Code)
				}
			} else {
				t.Fatal("expected AgentError")
			}
		})
	}
}

func TestSSLIssueValidation_ValidParams(t *testing.T) {
	tests := []struct {
		domain  string
		webroot string
		email   string
		name    string
	}{
		{"example.com", "/var/www/example", "admin@example.com", "basic"},
		{"sub.example.com", "/home/user/www", "test@sub.example.com", "subdomain"},
		{"my-domain.co.uk", "/var/www/my-domain", "admin+tag@example.com", "hyphenated domain"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test that validation passes (params are valid, though issuer will fail)
			if !sslDomainRegex.MatchString(tt.domain) {
				t.Errorf("domain validation failed for %q", tt.domain)
			}
			if !isAllowedWebroot(tt.webroot) {
				t.Errorf("webroot validation failed for %q", tt.webroot)
			}
			if !emailRegex.MatchString(tt.email) {
				t.Errorf("email validation failed for %q", tt.email)
			}
		})
	}
}

func TestSSLIssueValidation_AllowedWebroots(t *testing.T) {
	tests := []struct {
		webroot string
		allowed bool
		name    string
	}{
		{"/home/user/www", true, "home path"},
		{"/home/test", true, "home path"},
		{"/var/www/site", true, "var/www path"},
		{"/var/www/", true, "var/www with trailing slash"},
		{"/etc/passwd", false, "etc path"},
		{"/root/test", false, "root path"},
		{"/tmp/site", false, "tmp path"},
		{"relative/path", false, "relative path"},
	}

	for _, tt := range tests {
		result := isAllowedWebroot(tt.webroot)
		if result != tt.allowed {
			t.Errorf("%s: expected %v, got %v", tt.name, tt.allowed, result)
		}
	}
}

// GH #887: a format-valid webroot that does not exist yet must return the typed
// webroot_not_ready signal (CodeFailedPrecondition) — NOT run certbot and NOT a
// generic failure — so the panel reconciler can quietly defer ACME.
func TestSSLIssue_WebrootNotReady(t *testing.T) {
	params := sslIssueParams{
		Domain:  "sub.example.com",
		Webroot: "/home/nobody-gh887/domains/sub.example.com/public_html", // does not exist
		Email:   "admin@example.com",
	}
	paramsJSON, _ := json.Marshal(params)

	result, err := sslIssueHandler(context.Background(), paramsJSON)
	if err == nil || result != nil {
		t.Fatalf("expected typed error, got result=%v err=%v", result, err)
	}
	agentErr, ok := err.(*agentwire.AgentError)
	if !ok {
		t.Fatalf("expected *agentwire.AgentError, got %T", err)
	}
	if agentErr.Code != agentwire.CodeFailedPrecondition {
		t.Errorf("code = %s, want %s", agentErr.Code, agentwire.CodeFailedPrecondition)
	}
	if !strings.Contains(agentErr.Message, WebrootNotReadyMarker) {
		t.Errorf("message %q must carry the %q marker so the reconciler can match it",
			agentErr.Message, WebrootNotReadyMarker)
	}
}

func TestSSLIssueCommandRegistered(t *testing.T) {
	handlers := Default.Commands()
	found := false
	for _, cmd := range handlers {
		if cmd == "ssl.issue" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("ssl.issue command not registered")
	}
}
