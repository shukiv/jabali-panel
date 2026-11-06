package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// --deeplink is an in-panel path; anything else is refused before the
// broadcast is published (RunE returns before it touches Redis).
func TestNotificationBroadcast_DeeplinkOutsidePanelRefused(t *testing.T) {
	for _, link := range []string{"https://example.test/x", "//example.test", "javascript:alert(1)"} {
		cmd := newNotificationBroadcastCmd()
		require.NoError(t, cmd.Flags().Set("title", "t"))
		require.NoError(t, cmd.Flags().Set("deeplink", link))
		err := cmd.RunE(cmd, nil)
		require.ErrorContains(t, err, "--deeplink must be a panel path", link)
	}
}
