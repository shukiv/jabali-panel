package notifications

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Links every producer builds today stay as they are.
var panelLinks = []string{
	"/",
	"/jabali-admin/backups",
	"/jabali-admin/security?tab=malware",
	"/admin/security?tab=malware&sub=exec_audit",
	"/admin/domains?ghost=mismatch",
	"/admin/notifications/channels/01JD0000000000000000000000",
	"/jabali-admin/users/5b0e8c1e-3f7a-4c1d-9f8e-2a6b7c8d9e0f",
	"/me/notifications",
	"/jabali-admin/notifications/history?focus=01ABC#row",
}

// Anything that is not a plain path inside the panel.
var notPanelLinks = []string{
	"https://example.test/x",
	"http://example.test",
	"javascript:alert(1)",
	"data:text/html,x",
	"//example.test/x",
	"/\\example.test/x",
	"/a\\b",
	"\\\\example.test",
	"/\t/example.test",
	"/a\nb",
	"/a\rb",
	"/a\x00b",
	"/a b",
	`/a"b`,
	"/a'b",
	"/a<b>",
	"/a|b",
	"/a`b",
	"/café",
	"jabali-admin/backups",
	" /jabali-admin/backups",
	"/" + strings.Repeat("a", maxPanelPathLen),
}

func TestPanelPath(t *testing.T) {
	t.Parallel()
	for _, link := range panelLinks {
		require.Equal(t, link, PanelPath(link), "panel link %q", link)
	}
	for _, link := range notPanelLinks {
		require.Empty(t, PanelPath(link), "not a panel link: %q", link)
	}
	require.Empty(t, PanelPath(""))
}

func TestStreamMap_KeepsOnlyPanelLinks(t *testing.T) {
	t.Parallel()
	for _, link := range panelLinks {
		m := Envelope{EventKind: "k", Severity: "info", Title: "t", Deeplink: link}.StreamMap()
		require.Equal(t, link, m[fieldDeeplink], "panel link %q", link)
	}
	for _, link := range notPanelLinks {
		m := Envelope{EventKind: "k", Severity: "info", Title: "t", Deeplink: link}.StreamMap()
		_, present := m[fieldDeeplink]
		require.False(t, present, "not a panel link left in the stream: %q", link)
	}
}

// An entry already in the stream, or added by anything but Publish, is
// checked again when the dispatcher reads it.
func TestEnvelopeFromStream_KeepsOnlyPanelLinks(t *testing.T) {
	t.Parallel()
	read := func(link string) string {
		env, err := envelopeFromStream(map[string]any{
			fieldEventKind: "k",
			fieldSeverity:  "info",
			fieldTitle:     "t",
			fieldDeeplink:  link,
		})
		require.NoError(t, err)
		return env.Deeplink
	}
	for _, link := range panelLinks {
		require.Equal(t, link, read(link), "panel link %q", link)
	}
	for _, link := range notPanelLinks {
		require.Empty(t, read(link), "not a panel link read from the stream: %q", link)
	}
}
