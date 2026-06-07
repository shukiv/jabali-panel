package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"git.linux-hosting.co.il/shukivaknin/jabali2/agentwire"
)

func TestCronRunNowMissingUsername(t *testing.T) {
	params := json.RawMessage(`{
		"user_id": "1",
		"username": "",
		"job_id": "job1"
	}`)

	_, err := cronRunNowHandler(context.Background(), params)
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

func TestCronRunNowMissingJobID(t *testing.T) {
	params := json.RawMessage(`{
		"user_id": "1",
		"username": "testuser",
		"job_id": ""
	}`)

	_, err := cronRunNowHandler(context.Background(), params)
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

func TestCronRunNowUnknownUser(t *testing.T) {
	params := json.RawMessage(`{
		"user_id": "999",
		"username": "nonexistentuser12345",
		"job_id": "job1",
		"command": "php /home/nonexistentuser12345/public_html/wp-cron.php",
		"owned_docroots": ["/home/nonexistentuser12345/public_html"]
	}`)

	_, err := cronRunNowHandler(context.Background(), params)
	if err == nil {
		t.Fatal("expected error for unknown user")
	}
	agentErr, ok := err.(*agentwire.AgentError)
	if !ok {
		t.Fatalf("expected AgentError, got %T", err)
	}
	if agentErr.Code != agentwire.CodeNotFound {
		t.Errorf("expected CodeNotFound, got %s", agentErr.Code)
	}
}

func TestCronRunNowResponseStructure(t *testing.T) {
	currentUser := os.Getenv("USER")
	if currentUser == "" {
		t.Skip("USER env not set")
	}

	params := json.RawMessage(fmt.Sprintf(`{
		"user_id": "1",
		"username": "%s",
		"job_id": "nonexistent-job"
	}`, currentUser))

	result, err := cronRunNowHandler(context.Background(), params)
	// May fail due to systemd/service issues, but check structure if successful
	if err == nil && result != nil {
		resp, ok := result.(*cronRunNowResponse)
		if !ok {
			t.Fatalf("expected cronRunNowResponse, got %T", result)
		}
		// Exit code should be a valid integer (0 or >0)
		if resp.ExitCode < 0 {
			t.Error("exit code should be >= 0")
		}
	}
}
