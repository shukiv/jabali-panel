package api

import (
	"context"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1993: a restored domain brings back its mail settings — its mail
// provider and the provider's DKIM tokens, its DMARC np and testing tags and
// its CalDAV/CardDAV hosts — each held to the rule the domain page applies.

func TestRestoreDomainCheck_KeepsValidMailSettings(t *testing.T) {
	for _, source := range []RestoreSource{RestoreFromOwnBackup, RestoreFromUpload} {
		row := rdcRow("site.org")
		row.MailProvider, row.M365Onmicrosoft, row.GoogleDKIM = models.MailProviderM365, strp("Contoso"), strp("v=DKIM1; k=rsa; p=MIIB")
		row.DmarcNP, row.DmarcTesting = " reject ", true
		row.CalDAVHost, row.CardDAVHost = "dav.example.net:8443", "dav.example.net"
		w, err := phpCheck(source)(context.Background(), row, "alice")
		if err != nil || len(w) != 0 {
			t.Fatalf("source %d: warnings %v err %v", source, w, err)
		}
		// Stored as the page stores them: the tenant's full onmicrosoft name, the
		// np tag trimmed.
		if row.MailProvider != "m365" || odStrAPI(row.M365Onmicrosoft) != "contoso.onmicrosoft.com" || odStrAPI(row.GoogleDKIM) != "v=DKIM1; k=rsa; p=MIIB" ||
			row.DmarcNP != "reject" || !row.DmarcTesting || row.CalDAVHost != "dav.example.net:8443" || row.CardDAVHost != "dav.example.net" {
			t.Fatalf("source %d: got provider %s m365 %s google %s np %q testing %v caldav %s carddav %s", source, row.MailProvider,
				odStrAPI(row.M365Onmicrosoft), odStrAPI(row.GoogleDKIM), row.DmarcNP, row.DmarcTesting, row.CalDAVHost, row.CardDAVHost)
		}
	}
}

func odStrAPI(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// A mail setting the domain page would refuse is dropped with a warning; the
// domain and its other settings stay.
func TestRestoreDomainCheck_DropsMailSettingsTheDomainPageRefuses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		set   func(*models.Domain)
		unset func(*models.Domain) bool
	}{
		{"mail provider", func(d *models.Domain) { d.MailProvider = "postfix" }, func(d *models.Domain) bool { return d.MailProvider == "" }},
		{"Microsoft 365 tenant", func(d *models.Domain) { d.M365Onmicrosoft = strp("evil.example.com") }, func(d *models.Domain) bool { return d.M365Onmicrosoft == nil }},
		{"Google DKIM", func(d *models.Domain) { d.GoogleDKIM = strp("v=DKIM1\"; x") }, func(d *models.Domain) bool { return d.GoogleDKIM == nil }},
		{"DMARC np", func(d *models.Domain) { d.DmarcNP = "reject; rua=mailto:x@evil" }, func(d *models.Domain) bool { return d.DmarcNP == "" }},
		{"CalDAV host", func(d *models.Domain) { d.CalDAVHost = "dav.example.net 0 0 evil" }, func(d *models.Domain) bool { return d.CalDAVHost == "" }},
		{"CardDAV host", func(d *models.Domain) { d.CardDAVHost = "https://dav.example.net/" }, func(d *models.Domain) bool { return d.CardDAVHost == "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := rdcRow("site.org")
			row.DmarcTesting = true
			tc.set(row)
			w, err := phpCheck(RestoreFromUpload)(context.Background(), row, "alice")
			if err != nil || !tc.unset(row) || len(w) != 1 || !strings.Contains(w[0], tc.name+" dropped") {
				t.Fatalf("got warnings %v err %v; want it dropped with one warning", w, err)
			}
			if !row.DmarcTesting {
				t.Fatal("a valid setting beside it was dropped")
			}
		})
	}
}

// A domain with the custom posture (made from a DNS template) gets its mail
// from the template's records; Jabali mail can't run on it, as the domain's
// mail page refuses (template_posture_locked).
func TestRestoreDomainCheck_CustomPostureRunsNoJabaliMail(t *testing.T) {
	row := rdcRow("site.org")
	row.MailProvider, row.EmailEnabled = models.MailProviderCustom, true
	w, err := phpCheck(RestoreFromUpload)(context.Background(), row, "alice")
	if err != nil || row.EmailEnabled || row.MailProvider != models.MailProviderCustom || len(w) != 1 || !strings.Contains(w[0], "Jabali mail not turned on") {
		t.Fatalf("warnings %v err %v email %v provider %s; want mail off with one warning", w, err, row.EmailEnabled, row.MailProvider)
	}

	row = rdcRow("site.org")
	row.MailProvider, row.EmailEnabled = models.MailProviderCustom, false
	if w, err := phpCheck(RestoreFromUpload)(context.Background(), row, "alice"); err != nil || len(w) != 0 {
		t.Fatalf("custom without Jabali mail: warnings %v err %v", w, err)
	}
}
