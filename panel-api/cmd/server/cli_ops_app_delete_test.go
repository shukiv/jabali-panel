package main

import (
	"os"
	"strings"
	"testing"
)

// TestCLIAppDelete_DelegatesToSharedLifecycle pins the JAB-314 fix. The CLI
// `app delete` used to run its own delete sequence that (a) continued dropping
// panel rows after an agent app.delete failure, (b) dropped DB rows even when the
// host-side drop failed, and (c) never tore down the app's cron jobs — every one
// a way to make an invisible host/DB orphan. It now delegates to the shared,
// fail-closed api.RunAppDelete so its transcript matches the HTTP handler.
// cmd/server has no DB/agent fixture, so this source-pins the delegation (repo
// precedent: log_cmd_scope_test.go); the behaviour itself is unit-tested in
// internal/api/app_delete_test.go.
func TestCLIAppDelete_DelegatesToSharedLifecycle(t *testing.T) {
	src, err := os.ReadFile("cli_ops_app.go")
	if err != nil {
		t.Fatalf("read cli_ops_app.go: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "api.RunAppDelete(api.AppDeleteArgs{") {
		t.Fatal("CLI app delete must delegate to the shared api.RunAppDelete — a divergent copy is how the fail-open orphan bug returns (JAB-314)")
	}
	// The old inline sequence stamped rows via a local db.drop/db_user.drop call
	// path; ensure the CLI no longer runs its own app.delete agent call (which
	// bypassed the shared fail-closed ordering).
	if strings.Contains(s, `sharedAgent.Call(agentCtx, "app.delete"`) {
		t.Fatal("CLI must not run its own app.delete sequence — it must go through api.RunAppDelete (JAB-314)")
	}
}
