package commands

import (
	"context"
	"encoding/json"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// TestSSHUserSetShellHandler_RefusesProtectedUsers guards GH #658: a protected
// system account (root, the service user) must never be chsh'd into the tenant
// sandbox shell — that's what locked the operator out of SSH after a
// mis-created "root" panel user (GH #429).
func TestSSHUserSetShellHandler_RefusesProtectedUsers(t *testing.T) {
	for _, u := range []string{"root", "jabali"} {
		params, _ := json.Marshal(map[string]string{
			"username": u,
			"shell":    "/usr/local/bin/jabali-ssh-shell",
		})
		_, err := sshUserSetShellHandler(context.Background(), params)
		if err == nil {
			t.Fatalf("user %q: expected refusal, got nil", u)
		}
		ae, ok := err.(*agentwire.AgentError)
		if !ok || ae.Code != agentwire.CodePermissionDenied {
			t.Fatalf("user %q: want PermissionDenied AgentError, got %v", u, err)
		}
	}
}
