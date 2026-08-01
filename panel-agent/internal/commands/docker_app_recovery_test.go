package commands

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// The reconciler's recovery dispatch sends compose_yml="RECOVERY",
// meaning "reuse the compose.yml the original REST install wrote".
// Before the sentinel branch existed the agent wrote the literal string
// over the on-disk compose and every recovery destroyed the install
// (production n8n: compose.yml == "RECOVERY", up failed with
// `cannot construct !!str RECOVERY`).
func TestDockerAppInstall_RecoveryWithoutOnDiskComposeFailsClearly(t *testing.T) {
	params, _ := json.Marshal(map[string]any{
		"slug":        "definitely-not-installed-zz",
		"compose_yml": "RECOVERY",
	})
	_, err := dockerAppInstallHandler(context.Background(), json.RawMessage(params))
	if err == nil {
		t.Fatal("expected an error for a recovery dispatch with no on-disk compose.yml")
	}
	ae, ok := err.(*agentwire.AgentError)
	if !ok {
		t.Fatalf("expected AgentError, got %T: %v", err, err)
	}
	if ae.Code != agentwire.CodeNotFound {
		t.Errorf("expected CodeNotFound, got %s (%s)", ae.Code, ae.Message)
	}
	if !strings.Contains(ae.Message, "re-install from the panel") {
		t.Errorf("error must tell the operator the way out, got: %s", ae.Message)
	}
}

func TestDockerAppInstall_EmptyComposeStillRejected(t *testing.T) {
	params, _ := json.Marshal(map[string]any{"slug": "x-app", "compose_yml": ""})
	_, err := dockerAppInstallHandler(context.Background(), json.RawMessage(params))
	if err == nil {
		t.Fatal("expected an error")
	}
	if ae, ok := err.(*agentwire.AgentError); !ok || ae.Code != agentwire.CodeInvalidArgument {
		t.Fatalf("expected CodeInvalidArgument, got %v", err)
	}
}
