package main

import (
	"os"
	"strings"
	"testing"
)

// TestNotificationChannelTestCmd_RefusesDisabled pins that the operator CLI
// `notification channels test <id>` refuses a disabled channel BEFORE it
// publishes a test envelope — the JAB-308 fix. The HTTP testChannel handler
// already returns 409 for a disabled channel; the dispatcher's resolveTargets
// drops !ch.Enabled targets, so testing a disabled channel from the CLI only
// queued an envelope that was silently dropped while the command printed
// success. This is a source-pin (the direct-DB command has no seam to exercise
// without a live MariaDB + Redis); it is non-vacuous because the test command
// referenced ch.Enabled nowhere on main. Falsify by deleting the guard.
func TestNotificationChannelTestCmd_RefusesDisabled(t *testing.T) {
	src, err := os.ReadFile("notification_send_cmd.go")
	if err != nil {
		t.Fatalf("read notification_send_cmd.go: %v", err)
	}
	s := string(src)

	// Scope the assertions to the test command's function body so the create
	// command's own Enabled field (Enabled: !disabled) cannot satisfy them.
	start := strings.Index(s, "func newNotificationChannelTestCmd()")
	if start < 0 {
		t.Fatalf("newNotificationChannelTestCmd not found")
	}
	body := s[start:]
	if next := strings.Index(body[1:], "\nfunc "); next >= 0 {
		body = body[:next+1]
	}

	findByID := strings.Index(body, "FindByID(ctx, args[0])")
	// Pin the guard statement itself ("if !ch.Enabled {"), not a bare
	// "!ch.Enabled" substring, so the mention of !ch.Enabled in this function's
	// own comment cannot satisfy the assertion once the guard is deleted.
	guard := strings.Index(body, "if !ch.Enabled {")
	publish := strings.Index(body, "notifications.Envelope{")

	if findByID < 0 || guard < 0 || publish < 0 {
		t.Fatalf("expected FindByID + `if !ch.Enabled {` guard + Envelope construction in the test command; got findByID=%d guard=%d envelope=%d", findByID, guard, publish)
	}
	// The disabled guard must sit after the channel is loaded and before the
	// envelope that would be published is built.
	if !(findByID < guard && guard < publish) {
		t.Errorf("!ch.Enabled guard (idx %d) must sit between FindByID (idx %d) and the Envelope construction (idx %d)", guard, findByID, publish)
	}
}

// TestNotificationChannelCreateCmd_EnforcesTenantOwnerPolicy pins that the
// operator CLI `notification channels create --user <id>` runs the shared
// notifchannelops owner policy in the same order the tenant HTTP handler does —
// the JAB-308 AC1/AC2 fix. Without it the CLI created a tenant-owned channel
// that skipped the kind allowlist, the own-address email forcing (anti-relay /
// SSRF) and the per-user quota the API enforces. Order matters and is pinned:
// CheckKindAllowed → ForceOwnEmailConfig → ValidateChannelKindConfig →
// CheckQuota (allowlist before the owner-email lookup; forcing before per-kind
// config validation so the forced fields are validated; quota last). Source-pin
// because the command is direct-DB with no seam to run without a live MariaDB;
// non-vacuous because none of the notifchannelops calls existed on main.
// Falsify by deleting any of the four calls.
func TestNotificationChannelCreateCmd_EnforcesTenantOwnerPolicy(t *testing.T) {
	src, err := os.ReadFile("notification_send_cmd.go")
	if err != nil {
		t.Fatalf("read notification_send_cmd.go: %v", err)
	}
	s := string(src)

	start := strings.Index(s, "func newNotificationChannelCreateCmd()")
	if start < 0 {
		t.Fatalf("newNotificationChannelCreateCmd not found")
	}
	body := s[start:]
	if next := strings.Index(body[1:], "\nfunc "); next >= 0 {
		body = body[:next+1]
	}

	allowlist := strings.Index(body, "notifchannelops.CheckKindAllowed(")
	force := strings.Index(body, "notifchannelops.ForceOwnEmailConfig(")
	kindConfig := strings.Index(body, "api.ValidateChannelKindConfig(")
	quota := strings.Index(body, "notifchannelops.CheckQuota(")

	if allowlist < 0 || force < 0 || kindConfig < 0 || quota < 0 {
		t.Fatalf("expected CheckKindAllowed + ForceOwnEmailConfig + ValidateChannelKindConfig + CheckQuota in the create command; got allowlist=%d force=%d kindConfig=%d quota=%d", allowlist, force, kindConfig, quota)
	}
	if !(allowlist < force && force < kindConfig && kindConfig < quota) {
		t.Errorf("owner-policy calls out of order: CheckKindAllowed(%d) < ForceOwnEmailConfig(%d) < ValidateChannelKindConfig(%d) < CheckQuota(%d) must hold", allowlist, force, kindConfig, quota)
	}
}
