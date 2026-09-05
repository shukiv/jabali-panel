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
