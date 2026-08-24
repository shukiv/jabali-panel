package models

import "testing"

// JAB-319 / GH #280: the alias target must be the alias's OWN address, so two
// aliases on one mailbox get DISTINCT targets and never collide on
// uq_external_forward(mailbox_id, type, target). The CLI had regressed to the
// mailbox address (same for every alias), which failed the second alias.
func TestAliasForwarderTarget_UniquePerAlias(t *testing.T) {
	domain := "example.com"

	sales := AliasForwarderTarget("sales", domain)
	support := AliasForwarderTarget("support", domain)

	if sales != "sales@example.com" {
		t.Errorf("alias target should be its own address, got %q", sales)
	}
	if support != "support@example.com" {
		t.Errorf("alias target should be its own address, got %q", support)
	}
	// The crux: two aliases on the same mailbox produce DIFFERENT targets, so
	// the (mailbox_id, type, target) unique key admits both.
	if sales == support {
		t.Fatal("two aliases on one mailbox must have distinct targets (unique-key collision otherwise)")
	}
	// And an alias target is NEVER the mailbox's own address — that was the bug
	// (every alias defaulting to mailbox@domain collided).
	mailbox := "info" // the owning mailbox local part
	if AliasForwarderTarget("sales", domain) == mailbox+"@"+domain {
		t.Fatal("alias target must not equal the mailbox address")
	}
}
