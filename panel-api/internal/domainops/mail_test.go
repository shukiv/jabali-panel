package domainops

import (
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// MailProviderForServer is the one coercion every create adapter routes through
// (JAB-279 / GH #1409): a Jabali provider becomes none when the server mail
// module is off, so a domain never persists email-enabled mail that can't run.
// Every other provider — including a template-derived 'custom' posture — keeps
// its value regardless of the module flag, and the module-on case is a no-op.
func TestMailProviderForServer(t *testing.T) {
	cases := []struct {
		name          string
		provider      string
		moduleEnabled bool
		want          string
	}{
		{"jabali stays jabali when module on", models.MailProviderJabali, true, models.MailProviderJabali},
		{"jabali coerced to none when module off", models.MailProviderJabali, false, models.MailProviderNone},
		{"none stays none when module off", models.MailProviderNone, false, models.MailProviderNone},
		{"m365 untouched when module off", models.MailProviderM365, false, models.MailProviderM365},
		{"google untouched when module off", models.MailProviderGoogle, false, models.MailProviderGoogle},
		{"custom (template posture) untouched when module off", models.MailProviderCustom, false, models.MailProviderCustom},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MailProviderForServer(tc.provider, tc.moduleEnabled); got != tc.want {
				t.Errorf("MailProviderForServer(%q, %v) = %q, want %q", tc.provider, tc.moduleEnabled, got, tc.want)
			}
		})
	}
}
