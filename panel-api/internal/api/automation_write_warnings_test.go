package api

import "testing"

// A failed login-cache flush on suspend must reach the automation caller: the
// suspended user's mailboxes would otherwise keep webmail.
func TestLifecycleWarnings_CarriesTheMailWarning(t *testing.T) {
	w := lifecycleWarnings("", "", "", "mail_login_cache_flush_failed: x")
	if w["mail"] != "mail_login_cache_flush_failed: x" || len(w) != 1 {
		t.Fatalf("warnings = %v, want only the mail warning", w)
	}
	if lifecycleWarnings("", "", "", "") != nil {
		t.Fatal("no warnings must give nil")
	}
}
