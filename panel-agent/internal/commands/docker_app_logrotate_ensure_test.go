package commands

import (
	"context"
	"encoding/json"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// JAB-121 phase 2: the ensure handler validates its slug and no-ops on empty
// specs (never writes to /etc for a bogus/empty request).
func TestDockerAppLogrotateEnsureHandler_Validation(t *testing.T) {
	// bad slug → InvalidArgument, no filesystem touch.
	_, err := dockerAppLogrotateEnsureHandler(context.Background(),
		json.RawMessage(`{"slug":"../etc","log_rotate":[{"volume":"logs"}]}`))
	if err == nil {
		t.Fatal("expected error for bad slug")
	}
	if ae, ok := err.(*agentwire.AgentError); !ok || ae.Code != agentwire.CodeInvalidArgument {
		t.Errorf("want InvalidArgument, got %v", err)
	}

	// valid slug + empty specs → no-op success (writeDockerAppLogrotate returns
	// nil for empty and never creates a file).
	out, err := dockerAppLogrotateEnsureHandler(context.Background(),
		json.RawMessage(`{"slug":"someapp","log_rotate":[]}`))
	if err != nil {
		t.Fatalf("empty specs should succeed as no-op, got %v", err)
	}
	if m, ok := out.(map[string]any); !ok || m["ok"] != true {
		t.Errorf("unexpected result %v", out)
	}
}
