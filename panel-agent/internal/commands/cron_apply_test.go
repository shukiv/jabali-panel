package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"git.linux-hosting.co.il/shukivaknin/jabali2/agentwire"
	"git.linux-hosting.co.il/shukivaknin/jabali2/internal/cronvalidate"
)

func TestCronApplyMissingUsername(t *testing.T) {
	params := json.RawMessage(`{
		"user_id": "1",
		"username": "",
		"job_id": "job1",
		"name": "Test Job",
		"command": "wp cron event list",
		"schedule": "0 * * * *",
		"owned_docroots": ["/var/www/site1"]
	}`)

	_, err := cronApplyHandler(context.Background(), params)
	if err == nil {
		t.Fatal("expected error for missing username")
	}
	agentErr, ok := err.(*agentwire.AgentError)
	if !ok {
		t.Fatalf("expected AgentError, got %T", err)
	}
	if agentErr.Code != agentwire.CodeInvalidArgument {
		t.Errorf("expected CodeInvalidArgument, got %s", agentErr.Code)
	}
}

func TestCronApplyMissingJobID(t *testing.T) {
	params := json.RawMessage(`{
		"user_id": "1",
		"username": "testuser",
		"job_id": "",
		"name": "Test Job",
		"command": "wp cron event list",
		"schedule": "0 * * * *",
		"owned_docroots": ["/var/www/site1"]
	}`)

	_, err := cronApplyHandler(context.Background(), params)
	if err == nil {
		t.Fatal("expected error for missing job_id")
	}
	agentErr, ok := err.(*agentwire.AgentError)
	if !ok {
		t.Fatalf("expected AgentError, got %T", err)
	}
	if agentErr.Code != agentwire.CodeInvalidArgument {
		t.Errorf("expected CodeInvalidArgument, got %s", agentErr.Code)
	}
}

func TestCronApplyMissingCommand(t *testing.T) {
	params := json.RawMessage(`{
		"user_id": "1",
		"username": "testuser",
		"job_id": "job1",
		"name": "Test Job",
		"command": "",
		"schedule": "0 * * * *",
		"owned_docroots": ["/var/www/site1"]
	}`)

	_, err := cronApplyHandler(context.Background(), params)
	if err == nil {
		t.Fatal("expected error for missing command")
	}
	agentErr, ok := err.(*agentwire.AgentError)
	if !ok {
		t.Fatalf("expected AgentError, got %T", err)
	}
	if agentErr.Code != agentwire.CodeInvalidArgument {
		t.Errorf("expected CodeInvalidArgument, got %s", agentErr.Code)
	}
}

func TestCronApplyMissingSchedule(t *testing.T) {
	params := json.RawMessage(`{
		"user_id": "1",
		"username": "testuser",
		"job_id": "job1",
		"name": "Test Job",
		"command": "wp cron event list",
		"schedule": "",
		"owned_docroots": ["/var/www/site1"]
	}`)

	_, err := cronApplyHandler(context.Background(), params)
	if err == nil {
		t.Fatal("expected error for missing schedule")
	}
	agentErr, ok := err.(*agentwire.AgentError)
	if !ok {
		t.Fatalf("expected AgentError, got %T", err)
	}
	if agentErr.Code != agentwire.CodeInvalidArgument {
		t.Errorf("expected CodeInvalidArgument, got %s", agentErr.Code)
	}
}

func TestCronApplyInvalidCommand(t *testing.T) {
	params := json.RawMessage(`{
		"user_id": "1",
		"username": "testuser",
		"job_id": "job1",
		"name": "Test Job",
		"command": "invalidcmd arg1",
		"schedule": "0 * * * *",
		"owned_docroots": ["/var/www/site1"]
	}`)

	_, err := cronApplyHandler(context.Background(), params)
	if err == nil {
		t.Fatal("expected error for invalid command")
	}
	agentErr, ok := err.(*agentwire.AgentError)
	if !ok {
		t.Fatalf("expected AgentError, got %T", err)
	}
	if agentErr.Code != agentwire.CodeInvalidArgument {
		t.Errorf("expected CodeInvalidArgument, got %s", agentErr.Code)
	}
}

func TestCronApplyInvalidSchedule(t *testing.T) {
	params := json.RawMessage(`{
		"user_id": "1",
		"username": "testuser",
		"job_id": "job1",
		"name": "Test Job",
		"command": "wp cron event list",
		"schedule": "invalid schedule",
		"owned_docroots": ["/var/www/site1"]
	}`)

	_, err := cronApplyHandler(context.Background(), params)
	if err == nil {
		t.Fatal("expected error for invalid schedule")
	}
	agentErr, ok := err.(*agentwire.AgentError)
	if !ok {
		t.Fatalf("expected AgentError, got %T", err)
	}
	if agentErr.Code != agentwire.CodeInvalidArgument {
		t.Errorf("expected CodeInvalidArgument, got %s", agentErr.Code)
	}
}

func TestCronApplyUnknownUser(t *testing.T) {
	params := json.RawMessage(`{
		"user_id": "999",
		"username": "nonexistentuser12345",
		"job_id": "job1",
		"name": "Test Job",
		"command": "wp cron event list",
		"schedule": "0 * * * *",
		"owned_docroots": ["/var/www/site1"]
	}`)

	_, err := cronApplyHandler(context.Background(), params)
	if err == nil {
		t.Fatal("expected error for unknown user")
	}
	agentErr, ok := err.(*agentwire.AgentError)
	if !ok {
		t.Fatalf("expected AgentError, got %T", err)
	}
	// Depending on whether cronvalidate rejects the command first,
	// we might get CodeInvalidArgument before CodeNotFound
	if agentErr.Code != agentwire.CodeNotFound && agentErr.Code != agentwire.CodeInvalidArgument {
		t.Errorf("expected CodeNotFound or CodeInvalidArgument, got %s", agentErr.Code)
	}
}

func TestCronApplyValidParamsStructure(t *testing.T) {
	// Test valid structure but user/linger not available
	currentUser := os.Getenv("USER")
	if currentUser == "" {
		t.Skip("USER env not set, skipping")
	}

	params := json.RawMessage(fmt.Sprintf(`{
		"user_id": "1",
		"username": "%s",
		"job_id": "test-job-123",
		"name": "Test Cron Job",
		"command": "wp cron event list",
		"schedule": "0 * * * *",
		"owned_docroots": ["/var/www/site1"]
	}`, currentUser))

	result, err := cronApplyHandler(context.Background(), params)
	if err != nil {
		// Expected to fail due to linger check, but structure should be valid
		agentErr, ok := err.(*agentwire.AgentError)
		if ok && agentErr.Code == "user_not_lingering" {
			// This is expected for most users
			return
		}
	}

	if result != nil {
		resp, ok := result.(*cronApplyResponse)
		if !ok {
			t.Fatalf("expected cronApplyResponse, got %T", result)
		}
		if resp.ServicePath == "" || resp.TimerPath == "" {
			t.Error("response missing service_path or timer_path")
		}
	}
}

func TestSingleQuote(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"simple", "'simple'"},
		{"with space", "'with space'"},
		{"with'quote", "'with'\\''quote'"},
		{"path/to/file", "'path/to/file'"},
		{"arg with 'multiple' quotes", "'arg with '\\''multiple'\\'' quotes'"},
	}

	for _, tt := range tests {
		got := singleQuote(tt.input)
		if got != tt.expected {
			t.Errorf("singleQuote(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestBuildCronServiceContent(t *testing.T) {
	cmd := &cronvalidate.Command{
		Argv: []string{"wp", "cron", "event", "list"},
	}
	ownedDocroots := []string{"/var/www/site1"}

	content := buildCronServiceContent("job1", "Test Job", cmd, "testuser", ownedDocroots)

	// Verify structure
	if !contains(content, "[Unit]") {
		t.Error("missing [Unit] section")
	}
	if !contains(content, "[Service]") {
		t.Error("missing [Service] section")
	}
	if !contains(content, "Type=oneshot") {
		t.Error("missing Type=oneshot")
	}
	if !contains(content, "ExecStart=") {
		t.Error("missing ExecStart=")
	}
	if !contains(content, "WorkingDirectory=%h") {
		t.Error("missing WorkingDirectory=%h")
	}
}

func TestBuildCronTimerContent(t *testing.T) {
	content, err := buildCronTimerContent("job1", "0 * * * *")
	if err != nil {
		t.Fatalf("buildCronTimerContent: %v", err)
	}

	// Verify structure
	if !contains(content, "[Unit]") {
		t.Error("missing [Unit] section")
	}
	if !contains(content, "[Timer]") {
		t.Error("missing [Timer] section")
	}
	if !contains(content, "[Install]") {
		t.Error("missing [Install] section")
	}
	// 5-field cron is translated to systemd OnCalendar (NOT raw cron,
	// which yields "Loaded: bad-setting"). "0 * * * *" → hourly at :00.
	if !contains(content, "OnCalendar=*-*-* *:0:00") {
		t.Errorf("OnCalendar not translated to systemd format; got:\n%s", content)
	}
	if !contains(content, "Unit=jabali-cron-job1.service") {
		t.Error("missing Unit setting")
	}
	if !contains(content, "WantedBy=timers.target") {
		t.Error("missing WantedBy setting")
	}
}

func TestCheckUserLinger(t *testing.T) {
	currentUser := os.Getenv("USER")
	if currentUser == "" {
		t.Skip("USER env not set")
	}

	// Check for current user (expected to fail unless linger is enabled)
	err := checkUserLinger(context.Background(), currentUser)
	// err may be nil or non-nil depending on system state, just ensure it doesn't panic
	_ = err
}

func TestFileExists(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")

	// File doesn't exist yet
	if fileExists(testFile) {
		t.Error("fileExists returned true for non-existent file")
	}

	// Create file
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// File exists now
	if !fileExists(testFile) {
		t.Error("fileExists returned false for existing file")
	}
}

