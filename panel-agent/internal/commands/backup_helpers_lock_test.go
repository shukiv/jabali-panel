package commands

import (
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// GH #1045: restic lock contention must surface as a typed "busy, retry"
// error, not a raw internal dump of restic's stderr JSON.
func TestBkInternal_LockContentionIsFailedPrecondition(t *testing.T) {
	err := bkInternal("list snapshots for forget",
		errors.New(`restic snapshots: exit status 11 (stderr: {"message_type":"exit_error","code":11,"message":"unable to create lock in backend: repository is already locked exclusively by PID 69086"})`))
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeFailedPrecondition {
		t.Fatalf("expected failed_precondition, got %v", err)
	}
	if len(ae.Message) > 200 {
		t.Fatalf("busy message should be human-sized, got %d chars", len(ae.Message))
	}
}

func TestBkInternal_OtherErrorsStayInternal(t *testing.T) {
	err := bkInternal("x", errors.New("boom"))
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeInternal {
		t.Fatalf("expected internal, got %v", err)
	}
}
