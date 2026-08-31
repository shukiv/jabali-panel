package api

import (
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1409: a domain must never be created with Jabali Mail when the mail module
// isn't installed — the provider is coerced to "none" (email stays off). Other
// providers are untouched regardless of the module.
func TestMailProviderForServer(t *testing.T) {
	cases := []struct {
		provider string
		mailOn   bool
		want     string
	}{
		{models.MailProviderJabali, true, models.MailProviderJabali},  // installed → keep
		{models.MailProviderJabali, false, models.MailProviderNone},   // NOT installed → None
		{models.MailProviderNone, false, models.MailProviderNone},     // already none
		{models.MailProviderM365, false, models.MailProviderM365},     // external, unaffected
		{models.MailProviderGoogle, false, models.MailProviderGoogle}, // external, unaffected
	}
	for _, c := range cases {
		if got := mailProviderForServer(c.provider, c.mailOn); got != c.want {
			t.Fatalf("mailProviderForServer(%q, mailOn=%v) = %q, want %q", c.provider, c.mailOn, got, c.want)
		}
	}
	// And the coerced provider derives email OFF.
	if em, _ := models.DeriveMailFlags(mailProviderForServer(models.MailProviderJabali, false)); em {
		t.Fatal("coerced-to-none provider must derive email_enabled=false")
	}
}
