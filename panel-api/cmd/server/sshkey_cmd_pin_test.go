package main

import (
	"os"
	"strings"
	"testing"
)

// TestSSHKeyCLI_RoutesThroughSharedModule pins the ssh-key add/delete commands
// to internal/sshkeyops (ADR-0083, JAB-292): the former inline
// ParseAndFingerprint + models.SSHKey construction + repo.Create/Delete copy is
// gone, so validation and persistence can't drift from the REST handler.
// cmd/server has no seeded-DB fixture, so this follows the repo's source-pin
// precedent (domain_email_cmd_test.go, log_cmd_scope_test.go).
func TestSSHKeyCLI_RoutesThroughSharedModule(t *testing.T) {
	src, err := os.ReadFile("sshkey_cmd.go")
	if err != nil {
		t.Fatalf("read sshkey_cmd.go: %v", err)
	}
	s := string(src)
	for _, want := range []string{"sshkeyops.Add(", "sshkeyops.RemoveKey(", "sshkeyops.Find("} {
		if !strings.Contains(s, want) {
			t.Errorf("ssh-key CLI must route through %s", want)
		}
	}
	// The validation + fingerprinting must live in the shared module, not
	// inline in the CLI (that is the drift ADR-0083 removes).
	if strings.Contains(s, "ParseAndFingerprint(") {
		t.Error("key validation/fingerprinting must live in sshkeyops, not inline in the CLI")
	}
}
