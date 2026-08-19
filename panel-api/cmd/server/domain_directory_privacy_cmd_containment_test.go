package main

import (
	"os"
	"strings"
	"testing"
)

// TestDirPrivacyCredRemove_EnforcesRuleContainment guards the JAB-316 fix. The
// `dir-privacy cred-remove <domain> <rule-id> <cred-id>` CLI resolved the rule
// but then deleted <cred-id> by raw id, without checking the credential
// actually belongs to that rule — a cross-rule credential deletion (an operator
// could pass a rule they name alongside any credential id and delete it). The
// HTTP adapter enforces cred.RuleID == rule.ID; this pins the same containment
// in the CLI. cmd/server has no seeded-DB fixture, so this follows the repo's
// source-pin precedent (log_cmd_scope_test.go).
func TestDirPrivacyCredRemove_EnforcesRuleContainment(t *testing.T) {
	src, err := os.ReadFile("domain_directory_privacy_cmd.go")
	if err != nil {
		t.Fatalf("read domain_directory_privacy_cmd.go: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "FindCredentialByID(ctx, args[2])") {
		t.Fatal("cred-remove must fetch the credential by id before deleting it, to check rule containment (JAB-316)")
	}
	if !strings.Contains(s, "cred.RuleID != rule.ID") {
		t.Fatal("cred-remove must reject a credential whose RuleID != the resolved rule — fail closed (JAB-316)")
	}
}
