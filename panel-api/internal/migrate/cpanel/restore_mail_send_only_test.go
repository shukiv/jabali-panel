package cpanel

import (
	"context"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// mailStateDomainRepo is fakeDomainRepoMB with the domain's mail state set.
type mailStateDomainRepo struct {
	fakeDomainRepoMB
	emailEnabled bool
	provider     string
}

func (f *mailStateDomainRepo) FindByName(ctx context.Context, name string) (*models.Domain, error) {
	d, err := f.fakeDomainRepoMB.FindByName(ctx, name)
	if d != nil {
		d.EmailEnabled, d.MailProvider = f.emailEnabled, f.provider
	}
	return d, err
}

func importCpanelMail(t *testing.T, shadowBody string, emailEnabled bool, provider string) (map[string]*models.Mailbox, []string) {
	t.Helper()
	parsed := buildCpanelOfflineTree(t, shadowBody)
	mb := &fakeMailboxRepoMB{}
	dom := &mailStateDomainRepo{fakeDomainRepoMB: fakeDomainRepoMB{id: "01DOMAINID0000000000000000"}, emailEnabled: emailEnabled, provider: provider}
	res, err := ImportMailboxes(context.Background(), parsed, stubMailAgent{}, "job1", mb, dom, true, "", fakeTargetUserID)
	if err != nil {
		t.Fatalf("ImportMailboxes: %v", err)
	}
	rows := map[string]*models.Mailbox{}
	for _, r := range mb.created {
		rows[r.LocalPart] = r
	}
	return rows, res.Skipped
}

// cPanel lists every mail account of a domain in etc/<domain>/shadow, but
// creates the account's Maildir only on first delivery or login. An account
// that only ever sent (a site's SMTP login) has no Maildir, and got no row.
func TestImportMailboxes_CpanelShadowOnlyAccountGetsARow(t *testing.T) {
	rows, skipped := importCpanelMail(t, "box:"+testSrcSHA512+":19000::::::\nsmtp:"+testSrcSHA512+":19000::::::\n", true, models.MailProviderJabali)
	smtp := rows["smtp"]
	if smtp == nil {
		t.Fatalf("no row for the shadow-only account; rows %v; lines %v", rows, skipped)
	}
	if smtp.PasswordHash != testSrcSHA512 {
		t.Errorf("the source hash was not kept on the shadow-only account")
	}
	if smtp.SendOnly || rows["box"] == nil || rows["box"].SendOnly {
		t.Errorf("on a domain whose mail is hosted here both accounts receive: %+v", rows)
	}
	if skippedContains(skipped, testSrcSHA512) || skippedContains(skipped, "$6$") {
		t.Fatalf("a hash reached the run summary: %v", skipped)
	}
}

// Where the domain's mail is hosted elsewhere every imported account is
// send-only: the mail server refuses mail for the domain, and the account
// keeps only its SMTP login. The run summary says so.
func TestImportMailboxes_SendOnlyWhereMailIsHostedElsewhere(t *testing.T) {
	for name, tc := range map[string]struct {
		emailEnabled bool
		provider     string
	}{
		"email off":        {false, models.MailProviderNone},
		"outside provider": {false, models.MailProviderGoogle},
	} {
		t.Run(name, func(t *testing.T) {
			rows, skipped := importCpanelMail(t, "box:"+testSrcSHA512+":19000::::::\nsmtp:"+testSrcSHA512+":19000::::::\n", tc.emailEnabled, tc.provider)
			for _, lp := range []string{"box", "smtp"} {
				r := rows[lp]
				if r == nil {
					t.Fatalf("no row for %s; lines %v", lp, skipped)
				}
				if !r.SendOnly {
					t.Errorf("%s: want a send-only row", lp)
				}
				if r.PasswordHash != testSrcSHA512 {
					t.Errorf("%s: the source hash was not kept", lp)
				}
			}
			if !skippedContains(skipped, "smtp@main.example is send-only") {
				t.Errorf("no send-only line: %v", skipped)
			}
		})
	}
}
