package main

import (
	"os"
	"strings"
	"testing"
)

// TestDirPrivacyCredRemove_RoutesThroughContainmentOp guards the JAB-316 fix
// under the JAB-355 consolidation. The `dir-privacy cred-remove` CLI once
// deleted <cred-id> by raw id after only resolving the rule — a cross-rule
// credential deletion. That containment (load the credential, reject when
// cred.RuleID != the resolved rule, fail closed) now lives in
// internal/dirprivops.DeleteCredential, which both adapters call and which is
// exercised behaviorally by dirprivops.TestDeleteCredential_Containment.
//
// This source-pin keeps the CLI cred-remove routed through that op so a future
// edit can't regress to a raw DeleteCredential that skips the check. cmd/server
// has no seeded-DB fixture, so this follows the repo's source-pin precedent
// (log_cmd_scope_test.go).
func TestDirPrivacyCredRemove_RoutesThroughContainmentOp(t *testing.T) {
	src, err := os.ReadFile("domain_directory_privacy_cmd.go")
	if err != nil {
		t.Fatalf("read domain_directory_privacy_cmd.go: %v", err)
	}
	if !strings.Contains(string(src), "dirprivops.DeleteCredential(") {
		t.Fatal("cred-remove must route through dirprivops.DeleteCredential, which enforces cross-rule containment (JAB-316)")
	}
}
