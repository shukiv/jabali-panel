package models

import "testing"

// A domain whose mail is off, or hosted by another provider, must not be
// treated as local by this server's mail server.
func TestMailHostedElsewhere(t *testing.T) {
	cases := []struct {
		provider     string
		emailEnabled bool
		want         bool
	}{
		{MailProviderJabali, true, false},
		{"", true, false}, // a row from before the provider enum: Jabali
		{MailProviderJabali, false, true},
		{"", false, true},
		{MailProviderNone, false, true},
		{MailProviderGoogle, false, true},
		{MailProviderM365, false, true},
		{MailProviderCustom, false, true},
		// Inconsistent rows (email on with an external provider) still count
		// as elsewhere: the provider decides where the domain's mail lives.
		{MailProviderNone, true, true},
		{MailProviderGoogle, true, true},
		{MailProviderM365, true, true},
		{MailProviderCustom, true, true},
	}
	for _, c := range cases {
		if got := MailHostedElsewhere(c.emailEnabled, c.provider); got != c.want {
			t.Errorf("MailHostedElsewhere(%v, %q) = %v, want %v", c.emailEnabled, c.provider, got, c.want)
		}
	}
}
