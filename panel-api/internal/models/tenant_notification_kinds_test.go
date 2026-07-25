package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTenantNotificationKinds_OrDefault(t *testing.T) {
	t.Parallel()
	require.ElementsMatch(t, []string{"ntfy", "telegram", "discord", "webpush"},
		[]string(TenantNotificationKinds(nil).OrDefault()))
	// A non-empty set is returned as-is.
	explicit := TenantNotificationKinds{"ntfy"}
	require.Equal(t, TenantNotificationKinds{"ntfy"}, explicit.OrDefault())
}

func TestTenantNotificationKinds_DefaultExcludesRisky(t *testing.T) {
	t.Parallel()
	def := DefaultTenantNotificationKinds()
	require.False(t, def.Allows("webhook"), "webhook must be admin opt-in, not default")
	require.False(t, def.Allows("sms"))
	require.False(t, def.Allows("slack"))
	require.False(t, def.Allows("email"))
	require.True(t, def.Allows("ntfy"))
}

func TestTenantNotificationKinds_SanitizeDropsUnknown(t *testing.T) {
	t.Parallel()
	// "bogus" is unknown and dropped; known kinds (incl. email, phase 4d) are
	// kept, lowercased/trimmed; dupes collapse.
	in := TenantNotificationKinds{"NTFY", "email", "webhook", "bogus", "webhook", " discord "}
	out := in.Sanitize()
	require.ElementsMatch(t, []string{"ntfy", "email", "webhook", "discord"}, []string(out))
	require.NotContains(t, []string(out), "bogus")
}

func TestTenantNotificationKinds_ValueScanRoundTrip(t *testing.T) {
	t.Parallel()
	orig := TenantNotificationKinds{"ntfy", "webhook"}
	v, err := orig.Value()
	require.NoError(t, err)

	var back TenantNotificationKinds
	require.NoError(t, back.Scan(v))
	require.Equal(t, orig, back)

	// nil / empty column scans to an empty set (→ OrDefault at read time).
	var empty TenantNotificationKinds
	require.NoError(t, empty.Scan(nil))
	require.Len(t, empty, 0)
}
