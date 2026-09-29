package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/app"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

type notifyTestSettings struct {
	repository.ServerSettingsRepository
}

func (notifyTestSettings) Get(context.Context) (*models.ServerSettings, error) {
	return &models.ServerSettings{Hostname: "panel.example.com"}, nil
}

type notifyTestDomains struct {
	repository.DomainRepository
}

func (notifyTestDomains) FindByName(context.Context, string) (*models.Domain, error) {
	return &models.Domain{ID: "d1", Name: "panel.example.com", EmailEnabled: true}, nil
}

// notifyTestMailboxes answers the one-owner check with held.
type notifyTestMailboxes struct {
	repository.MailboxRepository
	held    bool
	created *models.Mailbox
}

func (*notifyTestMailboxes) ExistsByDomainAndLocalPart(context.Context, string, string) (bool, error) {
	return false, nil
}

func (m *notifyTestMailboxes) AddressHeld(context.Context, string, string) (bool, error) {
	return m.held, nil
}

func (m *notifyTestMailboxes) Create(_ context.Context, mb *models.Mailbox) error {
	m.created = mb
	return nil
}

// When an alias, group or shared resource holds the notify address, the
// database refuses the mailbox. Clearing the address on the mail server first
// would take that alias off its account in Stalwart's registry, so the notify
// mailbox is not provisioned and nothing is released.
func TestProvisionNotifyMailbox_LeavesAHeldAddressAlone(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	key := ssokey.Key{}
	for _, held := range []bool{true, false} {
		mbs := &notifyTestMailboxes{held: held}
		rel := &cliTestReleaser{}
		deps := app.Deps{ServerSettings: notifyTestSettings{}, Domains: notifyTestDomains{}, Mailboxes: mbs, SSOKey: &key, MailAddresses: rel}

		got := provisionNotifyMailbox(context.Background(), deps, log)

		if held {
			if got != "" || len(rel.released) != 0 || mbs.created != nil {
				t.Fatalf("held: got %q, released %v, created %v; want nothing", got, rel.released, mbs.created)
			}
			continue
		}
		if got != "jabali-notify@panel.example.com" || len(rel.released) != 1 || rel.released[0] != got || mbs.created == nil {
			t.Fatalf("free: got %q, released %v, created %v; want the address released and the mailbox made", got, rel.released, mbs.created)
		}
	}
}
