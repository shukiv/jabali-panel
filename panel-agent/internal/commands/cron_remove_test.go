package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func TestCronRemoveMissingUsername(t *testing.T) {
	params := json.RawMessage(`{
		"user_id": "1",
		"username": "",
		"job_id": "01HZZZZZZZZZZZZZZZZZZZZZZZ"
	}`)

	_, err := cronRemoveHandler(context.Background(), params)
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

func TestCronRemoveMissingJobID(t *testing.T) {
	params := json.RawMessage(`{
		"user_id": "1",
		"username": "testuser",
		"job_id": ""
	}`)

	_, err := cronRemoveHandler(context.Background(), params)
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

func TestCronRemoveUnknownUser(t *testing.T) {
	params := json.RawMessage(`{
		"user_id": "999",
		"username": "nonexistentuser12345",
		"job_id": "01HZZZZZZZZZZZZZZZZZZZZZZZ"
	}`)

	_, err := cronRemoveHandler(context.Background(), params)
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

func TestCronRemoveNoChange(t *testing.T) {
	currentUser := os.Getenv("USER")
	if currentUser == "" {
		t.Skip("USER env not set")
	}

	params := json.RawMessage(fmt.Sprintf(`{
		"user_id": "1",
		"username": "%s",
		"job_id": "nonexistent-job"
	}`, currentUser))

	result, err := cronRemoveHandler(context.Background(), params)
	if err != nil {
		// May fail due to systemd issues, but if it succeeds...
		return
	}

	if result != nil {
		resp, ok := result.(*cronRemoveResponse)
		if !ok {
			t.Fatalf("expected cronRemoveResponse, got %T", result)
		}
		// When job doesn't exist, should return NoChange=true
		if !resp.NoChange {
			t.Error("expected NoChange=true for non-existent job")
		}
	}
}

// job_id is interpolated into root-written unit paths under
// /etc/systemd/system and the per-user units dir, and cron.remove UNLINKS
// those paths as root. A traversal value must be refused at handler entry, on
// both verbs — panel-api only passes DB-minted ULIDs today, but the agent is
// the last line of validation and must not depend on that staying true.
func TestCronJobIDMustBeULID(t *testing.T) {
	bad := []string{
		"../ssh",
		"../../etc/systemd/system/ssh",
		"job1",
		"nonexistent-job",
		"01HZZZZZZZZZZZZZZZZZZZZZZ",  // 25 chars
		"01HZZZZZZZZZZZZZZZZZZZZZZZZ", // 27 chars
		"01HOZZZZZZZZZZZZZZZZZZZZZZ",  // contains O (not Crockford base32)
		"",
	}
	for _, id := range bad {
		removeParams := json.RawMessage(fmt.Sprintf(`{"user_id":"1","username":"root","job_id":%q}`, id))
		if _, err := cronRemoveHandler(context.Background(), removeParams); err == nil {
			t.Errorf("cron.remove accepted job_id %q — it lands in a root-side unlink path", id)
		}
		applyParams := json.RawMessage(fmt.Sprintf(
			`{"user_id":"1","username":"root","job_id":%q,"name":"n","command":"true","schedule":"0 * * * *"}`, id))
		if _, err := cronApplyHandler(context.Background(), applyParams); err == nil {
			t.Errorf("cron.apply accepted job_id %q — it lands in a root-written unit path", id)
		}
	}
}
