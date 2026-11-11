package mailboxops

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// mailElsewhereDomains are the domains whose mail this server does not host
// (models.MailHostedElsewhere): email off, or an outside provider.
func mailElsewhereDomains() map[string]*models.Domain {
	verified := models.OwnershipState{OwnershipStatus: models.OwnershipVerified}
	return map[string]*models.Domain{
		"email off":              {ID: "d1", Name: "example.com", MailProvider: models.MailProviderNone, OwnershipState: verified},
		"outside provider":       {ID: "d1", Name: "example.com", MailProvider: models.MailProviderGoogle, OwnershipState: verified},
		"email on, outside mail": {ID: "d1", Name: "example.com", EmailEnabled: true, MailProvider: models.MailProviderGoogle, OwnershipState: verified},
		"email off, jabali mail": {ID: "d1", Name: "example.com", MailProvider: models.MailProviderJabali, OwnershipState: verified},
		"email off, no provider": {ID: "d1", Name: "example.com", OwnershipState: verified},
	}
}

// A send-only account only submits mail, so a domain whose mail is hosted
// elsewhere can have one: a site's SMTP login keeps working after its inbound
// mail moved to another provider.
func TestCreate_SendOnlyWhereMailIsHostedElsewhere(t *testing.T) {
	for name, dom := range mailElsewhereDomains() {
		t.Run(name, func(t *testing.T) {
			repo := &fakeMBRepo{}
			key := ssokey.Key{}
			mb, _, err := Create(context.Background(), Deps{Mailboxes: repo, SSOKey: &key, Addresses: okReleaser{}},
				CreateInput{Domain: dom, LocalPart: "smtp", SendOnly: true}, nil)
			if err != nil {
				t.Fatalf("send-only create: %v", err)
			}
			if repo.created == nil || !repo.created.SendOnly {
				t.Fatalf("the row must be written send-only: %+v", repo.created)
			}
			if mb.EmailCached != "smtp@example.com" {
				t.Errorf("email_cached = %q", mb.EmailCached)
			}
		})
	}
}

// A mailbox that receives mail stays refused there: this server would hold
// mail for a domain whose MX points elsewhere.
func TestCreate_ReceivingMailboxRefusedWhereMailIsHostedElsewhere(t *testing.T) {
	for name, dom := range mailElsewhereDomains() {
		t.Run(name, func(t *testing.T) {
			repo := &fakeMBRepo{}
			key := ssokey.Key{}
			_, _, err := Create(context.Background(), Deps{Mailboxes: repo, SSOKey: &key, Addresses: okReleaser{}},
				CreateInput{Domain: dom, LocalPart: "info"}, nil)
			if !errors.Is(err, ErrEmailNotEnabled) {
				t.Fatalf("want ErrEmailNotEnabled, got %v", err)
			}
			if repo.created != nil {
				t.Fatal("no row may be written")
			}
		})
	}
}

// The ownership gate comes first for a send-only account too: the mail
// server's login lookup only answers for proven domains.
func TestCreate_SendOnlyStillNeedsProvenOwnership(t *testing.T) {
	repo := &fakeMBRepo{}
	key := ssokey.Key{}
	dom := &models.Domain{ID: "d1", Name: "example.com", MailProvider: models.MailProviderNone,
		OwnershipState: models.OwnershipState{OwnershipStatus: models.OwnershipPending}}
	_, _, err := Create(context.Background(), Deps{Mailboxes: repo, SSOKey: &key, Addresses: okReleaser{}},
		CreateInput{Domain: dom, LocalPart: "smtp", SendOnly: true}, nil)
	if !errors.Is(err, ErrOwnershipPending) {
		t.Fatalf("want ErrOwnershipPending, got %v", err)
	}
	if repo.created != nil {
		t.Fatal("no row may be written")
	}
}
