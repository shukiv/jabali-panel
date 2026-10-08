package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func allowlistAddParams(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"value":      "203.0.113.7",
		"reason":     "auto-whitelist: login admin@example.com",
		"expiration": "168h",
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// GH #357: a box installed without the security module has no cscli. The
// login allowlist's add says so with a distinct answer, so the panel stops
// asking on every admin request. Real exec with an empty PATH: nothing runs.
func TestCSAllowlistsAdd_NoCscliIsNotInstalled(t *testing.T) {
	withRealExec(t)
	t.Setenv("PATH", t.TempDir())

	_, err := csAllowlistsAddHandler(context.Background(), allowlistAddParams(t))
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeFailedPrecondition || ae.Message != agentwire.MsgCrowdSecNotInstalled {
		t.Fatalf("err = %v, want %s %q", err, agentwire.CodeFailedPrecondition, agentwire.MsgCrowdSecNotInstalled)
	}
}

// A cscli that is there but fails stays an internal error, so the panel
// retries it.
func TestCSAllowlistsAdd_FailingCscliStaysInternal(t *testing.T) {
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "false")
	}
	t.Cleanup(func() { execCommandContext = prev })

	_, err := csAllowlistsAddHandler(context.Background(), allowlistAddParams(t))
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeInternal {
		t.Fatalf("err = %v, want %s", err, agentwire.CodeInternal)
	}
}
