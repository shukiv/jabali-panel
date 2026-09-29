package main

import (
	"context"
	"log/slog"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/app"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

type pmDomains struct {
	repository.DomainRepository
	primary *models.Domain
}

func (f *pmDomains) FindPanelPrimary(context.Context) (*models.Domain, error) {
	if f.primary == nil {
		return nil, repository.ErrNotFound
	}
	return f.primary, nil
}

type pmMailboxes struct {
	repository.MailboxRepository
	existing map[string]bool // domainID/localPart
	held     bool            // an alias, group or shared resource has the address
	created  []*models.Mailbox
}

func (f *pmMailboxes) AddressHeld(context.Context, string, string) (bool, error) {
	return f.held, nil
}

func (f *pmMailboxes) ExistsByDomainAndLocalPart(_ context.Context, domainID, localPart string) (bool, error) {
	return f.existing[domainID+"/"+localPart], nil
}

func (f *pmMailboxes) Create(_ context.Context, mb *models.Mailbox) error {
	f.created = append(f.created, mb)
	return nil
}

type pmGroups struct {
	repository.MailGroupRepository
	existing map[string]bool
}

func (f *pmGroups) ExistsByDomainAndLocalPart(_ context.Context, domainID, localPart string) (bool, error) {
	return f.existing[domainID+"/"+localPart], nil
}

type pmForwarders struct {
	repository.EmailForwarderRepository
	rows []models.EmailForwarder
}

func (f *pmForwarders) ListByDomainIDs(_ context.Context, ids []string) ([]models.EmailForwarder, error) {
	return f.rows, nil
}

func pmDeps(primary *models.Domain) (app.Deps, *pmMailboxes, *pmGroups, *pmForwarders) {
	mbs := &pmMailboxes{existing: map[string]bool{}}
	groups := &pmGroups{existing: map[string]bool{}}
	fwds := &pmForwarders{}
	key := &ssokey.Key{1, 2, 3}
	return app.Deps{
		Domains:    &pmDomains{primary: primary},
		Mailboxes:  mbs,
		MailGroups: groups,
		Forwarders: fwds,
		SSOKey:     key,
	}, mbs, groups, fwds
}

func TestProvisionPostmasterMailbox_CreatesTheAdminMailbox(t *testing.T) {
	dom := &models.Domain{ID: "d-panel", Name: "panel.example", EmailEnabled: true, IsPanelPrimary: true}
	deps, mbs, _, _ := pmDeps(dom)

	got := provisionPostmasterMailbox(context.Background(), deps, slog.New(slog.DiscardHandler))

	if got != "postmaster@panel.example" {
		t.Fatalf("returned %q, want postmaster@panel.example", got)
	}
	if len(mbs.created) != 1 {
		t.Fatalf("%d mailboxes created, want 1", len(mbs.created))
	}
	mb := mbs.created[0]
	if mb.DomainID != "d-panel" || mb.LocalPart != "postmaster" || mb.QuotaBytes != 1<<30 || mb.System || mb.SendOnly || mb.IsDisabled {
		t.Fatalf("mailbox = %+v", mb)
	}
	// The sealed password is the one the hash was made from, so the admin
	// can open the mailbox in webmail from the panel.
	pw, err := deps.SSOKey.Open(mb.PasswordEnc)
	if err != nil {
		t.Fatalf("open sealed password: %v", err)
	}
	if bcrypt.CompareHashAndPassword([]byte(mb.PasswordHash), pw) != nil {
		t.Fatal("sealed password does not match the stored hash")
	}
}

func TestProvisionPostmasterMailbox_LeavesTheAdminsChoiceAlone(t *testing.T) {
	alias := "postmaster"
	cases := map[string]func(dom *models.Domain, mbs *pmMailboxes, groups *pmGroups, fwds *pmForwarders){
		"mailbox exists": func(dom *models.Domain, mbs *pmMailboxes, _ *pmGroups, _ *pmForwarders) {
			mbs.existing[dom.ID+"/postmaster"] = true
		},
		"group": func(dom *models.Domain, _ *pmMailboxes, groups *pmGroups, _ *pmForwarders) {
			groups.existing[dom.ID+"/postmaster"] = true
		},
		"alias": func(dom *models.Domain, _ *pmMailboxes, _ *pmGroups, fwds *pmForwarders) {
			fwds.rows = []models.EmailForwarder{{DomainID: dom.ID, Type: "alias", LocalPart: &alias}}
		},
		"email off": func(dom *models.Domain, _ *pmMailboxes, _ *pmGroups, _ *pmForwarders) {
			dom.EmailEnabled = false
		},
	}
	for name, setup := range cases {
		dom := &models.Domain{ID: "d-panel", Name: "panel.example", EmailEnabled: true, IsPanelPrimary: true}
		deps, mbs, groups, fwds := pmDeps(dom)
		setup(dom, mbs, groups, fwds)
		provisionPostmasterMailbox(context.Background(), deps, slog.New(slog.DiscardHandler))
		if len(mbs.created) != 0 {
			t.Errorf("%s: created %d mailboxes, want none", name, len(mbs.created))
		}
	}

	deps, mbs, _, _ := pmDeps(nil)
	if got := provisionPostmasterMailbox(context.Background(), deps, slog.New(slog.DiscardHandler)); got != "" || len(mbs.created) != 0 {
		t.Errorf("no panel domain: returned %q, created %d", got, len(mbs.created))
	}
}

// A shared resource at postmaster@ on the panel domain is not among the
// checks above, but the database refuses a mailbox there. The mailbox is not
// made, and the address is not released on the mail server: that would take
// a live alias off its account.
func TestProvisionPostmasterMailbox_LeavesAHeldAddressAlone(t *testing.T) {
	dom := &models.Domain{ID: "d-panel", Name: "panel.example", EmailEnabled: true, IsPanelPrimary: true}
	deps, mbs, _, _ := pmDeps(dom)
	mbs.held = true
	rel := &cliTestReleaser{}
	deps.MailAddresses = rel

	got := provisionPostmasterMailbox(context.Background(), deps, slog.New(slog.DiscardHandler))

	if got != "" || len(mbs.created) != 0 || len(rel.released) != 0 {
		t.Fatalf("returned %q, created %d, released %v; want nothing", got, len(mbs.created), rel.released)
	}
}
