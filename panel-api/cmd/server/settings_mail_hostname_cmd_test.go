package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// JAB-390: install.sh renders the Bulwark JMAP URL and the /webmail redirects
// from `jabali settings mail-hostname --applied`, so a `jabali update` after a
// shared-mail-hostname switchover keeps the applied name instead of reverting
// every render to mail.<hostname>. The output is interpolated into config
// files: only a validated, normalized bare FQDN may ever be printed.
func TestPrintMailHostname(t *testing.T) {
	str := func(s string) *string { return &s }
	cases := []struct {
		name        string
		hostname    string
		stored      *string
		appliedOnly bool
		want        string
		wantErr     bool
	}{
		{name: "derived, effective", hostname: "mx.example.com", want: "mail.mx.example.com\n"},
		{name: "derived, applied only prints nothing", hostname: "mx.example.com", appliedOnly: true, want: ""},
		{name: "custom, effective", hostname: "mx.example.com", stored: str(" MX.Example.NET "), want: "mx.example.net\n"},
		{name: "custom, applied only", hostname: "mx.example.com", stored: str("MX.Example.NET"), appliedOnly: true, want: "mx.example.net\n"},
		{name: "invalid stored value falls back to derived", hostname: "mx.example.com", stored: str("https://evil.example/"), want: "mail.mx.example.com\n"},
		{name: "invalid stored value is never printed as applied", hostname: "mx.example.com", stored: str("https://evil.example/"), appliedOnly: true, want: ""},
		{name: "config injection is never printed", hostname: "mx.example.com", stored: str("mx.example.net;\nreturn 301 https://evil"), appliedOnly: true, want: ""},
		{name: "no hostname and no custom name is an error", hostname: "", wantErr: true},
		{name: "no hostname, applied only prints nothing", hostname: "", appliedOnly: true, want: ""},
		{name: "no hostname, custom name still resolves", hostname: "", stored: str("mx.example.net"), want: "mx.example.net\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := printMailHostname(&out, &models.ServerSettings{Hostname: tc.hostname, MailHostname: tc.stored}, tc.appliedOnly)
			if tc.wantErr {
				require.Error(t, err)
				require.Empty(t, out.String(), "nothing may be printed on error")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, out.String())
		})
	}
}

func TestSettingsMailHostnameCmdRegistered(t *testing.T) {
	cmd, _, err := newSettingsCmd().Find([]string{"mail-hostname"})
	require.NoError(t, err)
	require.Equal(t, "mail-hostname", cmd.Name())
	require.NotNil(t, cmd.Flags().Lookup("applied"), "install.sh calls `settings mail-hostname --applied`")
	require.NotNil(t, cmd.PreRunE, "the command reads the DB")
}
