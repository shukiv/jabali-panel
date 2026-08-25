package repository

import (
	"strings"
	"testing"
)

// The mailbox inventory / directory projection must never select the
// secret-bearing columns into memory (JAB-370): the bcrypt password_hash and
// the AES-encrypted password_enc are irrelevant to every inventory consumer and
// were being pulled by `SELECT m.*` on admin- and fleet-polled paths. Both
// ListAllWithDomain and ListByOwnerWithDomain build from this one constant, so
// guarding it guards both.
func TestMailboxInventorySelect_ExcludesSecretColumns(t *testing.T) {
	for _, secret := range []string{"password_hash", "password_enc"} {
		if strings.Contains(mailboxInventorySelect, secret) {
			t.Fatalf("mailbox inventory projection must not select %q; got %q", secret, mailboxInventorySelect)
		}
	}
	if strings.Contains(mailboxInventorySelect, "m.*") {
		t.Fatalf("mailbox inventory projection must be an explicit allowlist, not a wildcard: %q", mailboxInventorySelect)
	}
	// The safe columns every inventory consumer relies on must still be present.
	for _, col := range []string{
		"m.id", "m.domain_id", "m.local_part", "m.email_cached", "m.display_name",
		"m.quota_bytes", "m.is_disabled", "m.send_only", "m.system",
		"m.last_usage_bytes", "m.last_usage_at", "domain_name", "owner_user_id",
	} {
		if !strings.Contains(mailboxInventorySelect, col) {
			t.Errorf("mailbox inventory projection is missing expected safe column %q", col)
		}
	}
}
