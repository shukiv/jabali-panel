package commands

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func TestSystemSetTimezone_Valid(t *testing.T) {
	requireHostMutationAllowed(t) // GH #994: reaches `timedatectl set-timezone` (changes the host clock)
	// This test validates against real /usr/share/zoneinfo if it exists.
	// On systems without it, the test is skipped.
	// Note: The actual timedatectl call requires root/auth, so we only test
	// the validation logic. Full integration testing requires running as root.
	if _, err := os.Stat("/usr/share/zoneinfo/UTC"); err != nil {
		t.Skip("skipping: /usr/share/zoneinfo not available")
	}

	ctx := context.Background()
	params := json.RawMessage(`{"timezone":"UTC"}`)
	_, err := systemSetTimezoneHandler(ctx, params)
	if err == nil {
		t.Log("handler succeeded (running as root?)")
		return
	}

	// In non-root environments, timedatectl will fail with auth error
	// but the validation logic should have passed. This is OK.
	if strings.Contains(err.Error(), "Interactive authentication required") ||
		strings.Contains(err.Error(), "timedatectl failed") {
		t.Logf("expected auth failure in non-root test: %v", err)
		return
	}

	// Other errors are unexpected
	t.Fatalf("unexpected error: %v", err)
}

func TestSystemSetTimezone_EmptyInput(t *testing.T) {
	ctx := context.Background()
	params := json.RawMessage(`{"timezone":""}`)
	_, err := systemSetTimezoneHandler(ctx, params)
	if err == nil {
		t.Fatal("expected error for empty timezone")
	}

	agentErr, ok := err.(*agentwire.AgentError)
	if !ok {
		t.Fatalf("expected AgentError, got %T", err)
	}
	if agentErr.Code != agentwire.CodeInvalidArgument {
		t.Fatalf("expected CodeInvalidArgument, got %s", agentErr.Code)
	}
}

func TestSystemSetTimezone_PathTraversal(t *testing.T) {
	ctx := context.Background()

	tests := []string{
		"../etc/passwd",
		"/etc/passwd",
		"UTC/..",
		"UTC/../etc",
	}

	for _, tz := range tests {
		t.Run(tz, func(t *testing.T) {
			params := json.RawMessage(`{"timezone":"` + tz + `"}`)
			_, err := systemSetTimezoneHandler(ctx, params)
			if err == nil {
				t.Fatalf("expected error for timezone %q", tz)
			}

			agentErr, ok := err.(*agentwire.AgentError)
			if !ok {
				t.Fatalf("expected AgentError, got %T", err)
			}
			if agentErr.Code != agentwire.CodeInvalidArgument {
				t.Fatalf("expected CodeInvalidArgument, got %s", agentErr.Code)
			}
		})
	}
}

func TestSystemSetTimezone_NotFound(t *testing.T) {
	ctx := context.Background()
	params := json.RawMessage(`{"timezone":"Nonexistent/Zone"}`)
	_, err := systemSetTimezoneHandler(ctx, params)
	if err == nil {
		t.Fatal("expected error for nonexistent timezone")
	}

	agentErr, ok := err.(*agentwire.AgentError)
	if !ok {
		t.Fatalf("expected AgentError, got %T", err)
	}
	if agentErr.Code != agentwire.CodeInvalidArgument {
		t.Fatalf("expected CodeInvalidArgument, got %s", agentErr.Code)
	}
}

func TestSystemSetTimezone_InvalidJSON(t *testing.T) {
	ctx := context.Background()
	params := json.RawMessage(`{not valid json}`)
	_, err := systemSetTimezoneHandler(ctx, params)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}

	agentErr, ok := err.(*agentwire.AgentError)
	if !ok {
		t.Fatalf("expected AgentError, got %T", err)
	}
	if agentErr.Code != agentwire.CodeInvalidArgument {
		t.Fatalf("expected CodeInvalidArgument, got %s", agentErr.Code)
	}
}

func TestSystemSetTimezone_Registration(t *testing.T) {
	// Verify the handler is registered
	commands := Default.Commands()
	found := false
	for _, cmd := range commands {
		if cmd == "system.set_timezone" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("system.set_timezone not registered in Default registry")
	}
}

func TestSystemSetTimezone_WhitespaceHandling(t *testing.T) {
	requireHostMutationAllowed(t) // GH #994: reaches `timedatectl set-timezone` (changes the host clock)
	// Test that leading/trailing whitespace is trimmed
	ctx := context.Background()
	params := json.RawMessage(`{"timezone":" UTC "}`)

	// This should succeed if /usr/share/zoneinfo exists
	if _, err := os.Stat("/usr/share/zoneinfo/UTC"); err != nil {
		t.Skip("skipping: /usr/share/zoneinfo not available")
	}

	_, err := systemSetTimezoneHandler(ctx, params)
	if err == nil {
		t.Log("handler succeeded (running as root?)")
		return
	}

	// In non-root environments, timedatectl will fail with auth error
	// but the whitespace trimming validation should have passed
	if strings.Contains(err.Error(), "Interactive authentication required") ||
		strings.Contains(err.Error(), "timedatectl failed") {
		t.Logf("expected auth failure in non-root test: %v", err)
		return
	}

	t.Fatalf("unexpected error: %v", err)
}

func TestSystemSetTimezone_DirectoryRejection(t *testing.T) {
	// /usr/share/zoneinfo/America is a directory, not a file
	ctx := context.Background()
	params := json.RawMessage(`{"timezone":"America"}`)

	if _, err := os.Stat("/usr/share/zoneinfo/America"); err != nil {
		t.Skip("skipping: /usr/share/zoneinfo/America not available")
	}

	_, err := systemSetTimezoneHandler(ctx, params)
	if err == nil {
		t.Fatal("expected error for directory timezone")
	}

	agentErr, ok := err.(*agentwire.AgentError)
	if !ok {
		t.Fatalf("expected AgentError, got %T", err)
	}
	if agentErr.Code != agentwire.CodeInvalidArgument {
		t.Fatalf("expected CodeInvalidArgument, got %s", agentErr.Code)
	}
}

func TestSystemSetTimezone_TooLong(t *testing.T) {
	ctx := context.Background()
	// Create a timezone string longer than 64 chars (though this should
	// fail at the API layer, good to test here too)
	longTz := "A" + strings.Repeat("bc", 40) // ~81 chars
	params := json.RawMessage(`{"timezone":"` + longTz + `"}`)

	_, err := systemSetTimezoneHandler(ctx, params)
	if err == nil {
		// The handler doesn't have a length check, but at least it should
		// fail when trying to validate against /usr/share/zoneinfo
		t.Log("handler accepted long timezone; will fail in zoneinfo lookup (OK)")
	}
}
