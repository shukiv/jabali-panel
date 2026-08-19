package main

import (
	"os"
	"strings"
	"testing"
)

// TestLogAccessCreate_RoutesThroughSharedScopePolicy is the CLI half of the
// JAB-303 parity contract. The CLI is the path that was actually vulnerable
// (it minted nil-domain, hence server-wide, grants for tenant beneficiaries),
// but a live rejection test needs a seeded DB fixture that cmd/server does not
// have. Following the repo's cross-language pin precedent
// (install/tests/test_ftp_module_optin.sh §7, which source-pins a Go file), this
// asserts the mint path delegates to logaccess.ValidateGrantScope so the guard
// cannot be silently deleted, leaving the server-wide-for-tenant hole behind.
func TestLogAccessCreate_RoutesThroughSharedScopePolicy(t *testing.T) {
	src, err := os.ReadFile("log_cmd.go")
	if err != nil {
		t.Fatalf("read log_cmd.go: %v", err)
	}
	if !strings.Contains(string(src), "logaccess.ValidateGrantScope(u.IsAdmin") {
		t.Fatal("log access create must gate the mint through logaccess.ValidateGrantScope(u.IsAdmin, ...) — a nil-domain grant is server-wide and must be admin-only (JAB-303)")
	}
}
