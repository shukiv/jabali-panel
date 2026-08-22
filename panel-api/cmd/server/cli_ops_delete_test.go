package main

import (
	"os"
	"strings"
	"testing"
)

// TestCLIUserDelete_RoutesThroughCascade pins the JAB-278 fix. The operator CLI
// account delete used to run its own teardown that omitted Docker teardown
// (which must BLOCK on failure), FTP subaccount reaping, Redis cache-ACL
// revocation, and port-allocation release, and dropped the panel row even after
// a MariaDB drop failed — manufacturing invisible orphans. It now routes through
// the one canonical userops.DeleteCascade the REST + automation adapters use.
// cmd/server has no DB/agent fixture, so this source-pins the delegation; the
// cascade behaviour itself is tested in internal/userops.
func TestCLIUserDelete_RoutesThroughCascade(t *testing.T) {
	src, err := os.ReadFile("cli_ops.go")
	if err != nil {
		t.Fatalf("read cli_ops.go: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "userops.DeleteCascade(ctx, deps, deleteDeps, target,") {
		t.Fatal("CLI user delete must route through the canonical userops.DeleteCascade (JAB-278)")
	}
	// The divergent inline teardown must be gone.
	if strings.Contains(s, `sharedAgent.Call(agentCtx, "user.delete"`) || strings.Contains(s, `"db_user.drop"`) {
		t.Fatal("CLI must not run its own account-teardown agent calls — DeleteCascade owns them (JAB-278)")
	}
	// It must wire the previously-missing teardown deps.
	for _, want := range []string{"DockerApps:", "FtpAccounts:", "RevokeCacheACLs:"} {
		if !strings.Contains(s, want) {
			t.Fatalf("CLI delete deps missing %q — the cascade needs it for full teardown (JAB-278)", want)
		}
	}
}
