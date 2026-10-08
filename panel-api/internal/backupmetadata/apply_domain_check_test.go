package backupmetadata

import (
	"context"
	"errors"
	"strings"
	"testing"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1898: every restored domain passes Deps.CheckDomain, and a refused
// domain takes its mailboxes and app installs with it.

type dcDomains struct {
	repository.DomainRepository
	created []models.Domain
}

func (r *dcDomains) FindByID(context.Context, string) (*models.Domain, error) {
	return nil, repository.ErrNotFound
}
func (r *dcDomains) FindByName(context.Context, string) (*models.Domain, error) {
	return nil, repository.ErrNotFound
}
func (r *dcDomains) Create(_ context.Context, d *models.Domain) error {
	r.created = append(r.created, *d)
	return nil
}

// Update stands for the write Apply makes after the insert, for a switch the
// insert turned on.
func (r *dcDomains) Update(context.Context, *models.Domain) error { return nil }

type dcMailboxes struct {
	repository.MailboxRepository
	created int
}

func (r *dcMailboxes) FindByID(context.Context, string) (*models.Mailbox, error) {
	return nil, repository.ErrNotFound
}
func (r *dcMailboxes) FindByEmail(context.Context, string) (*models.Mailbox, error) {
	return nil, repository.ErrNotFound
}
func (r *dcMailboxes) Create(context.Context, *models.Mailbox) error { r.created++; return nil }

type dcInstalls struct {
	repository.ApplicationInstallRepository
	created int
}

func (r *dcInstalls) Create(context.Context, *models.ApplicationInstall) error { r.created++; return nil }

func dcMeta() *internalbackup.AccountMetadata {
	uname := "alice"
	return &internalbackup.AccountMetadata{
		User: internalbackup.MetadataUser{ID: "u1", Email: "alice@example.com", Username: &uname},
		Domains: []internalbackup.MetadataDomain{
			{ID: "d-good", Name: "good.org", DocRoot: "/home/alice/domains/good.org/public_html",
				Mailboxes: []internalbackup.MetadataMailbox{{ID: "mb-good", LocalPart: "info"}}},
			{ID: "d-bad", Name: "bad.org", DocRoot: "/home/alice/domains/bad.org/public_html",
				Mailboxes: []internalbackup.MetadataMailbox{{ID: "mb-bad", LocalPart: "info"}}},
		},
		AppInstalls: []internalbackup.MetadataAppInstall{
			{ID: "ai-good", DomainID: "d-good", AppType: "wordpress"},
			{ID: "ai-bad", DomainID: "d-bad", AppType: "wordpress"},
		},
	}
}

func dcDeps() (*dcDomains, *dcMailboxes, *dcInstalls, Deps) {
	dom, mb, ai := &dcDomains{}, &dcMailboxes{}, &dcInstalls{}
	return dom, mb, ai, Deps{Users: &createGuardUsersRepo{}, Domains: dom, Mailboxes: mb, AppInstalls: ai, MailAddresses: &dcReleaser{mb: mb}}
}

// dcReleaser records the addresses Apply releases on the mail server, and
// whether the mailbox row was already stored at that moment.
type dcReleaser struct {
	mb         *dcMailboxes
	released   []string
	afterWrite bool
	err        error
}

func (r *dcReleaser) ReleaseAddress(_ context.Context, address string) error {
	r.released = append(r.released, address)
	if r.mb != nil && r.mb.created > len(r.released)-1 {
		r.afterWrite = true
	}
	return r.err
}

func hasError(errs []string, sub string) bool {
	for _, e := range errs {
		if strings.Contains(e, sub) {
			return true
		}
	}
	return false
}

func TestApply_RefusedDomainTakesItsChildren(t *testing.T) {
	dom, mb, ai, deps := dcDeps()
	var gotUser string
	deps.CheckDomain = func(_ context.Context, row *models.Domain, owner string) ([]string, error) {
		gotUser = owner
		if row.Name == "bad.org" {
			return nil, errors.New("nested under another owner")
		}
		return nil, nil
	}
	r := Apply(context.Background(), dcMeta(), deps)

	if gotUser != "alice" {
		t.Fatalf("the check got owner username %q, want alice", gotUser)
	}
	if len(dom.created) != 1 || dom.created[0].Name != "good.org" {
		t.Fatalf("want only good.org stored, got %+v", dom.created)
	}
	if mb.created != 1 || ai.created != 1 {
		t.Fatalf("the refused domain's mailbox/app install were restored: mailboxes=%d installs=%d", mb.created, ai.created)
	}
	if !hasError(r.Errors, "bad.org): not restored: nested under another owner") ||
		!hasError(r.Errors, "app_install ai-bad: not restored") {
		t.Fatalf("refusals not reported: %v", r.Errors)
	}
}

// No check wired: no domain is restored. A door that forgets the hook cannot
// skip the checks.
func TestApply_NoCheckRefusesEveryDomain(t *testing.T) {
	dom, mb, ai, deps := dcDeps()
	r := Apply(context.Background(), dcMeta(), deps)
	if len(dom.created) != 0 || mb.created != 0 || ai.created != 0 {
		t.Fatalf("restored without the checks: domains=%d mailboxes=%d installs=%d", len(dom.created), mb.created, ai.created)
	}
	if !hasError(r.Errors, "restore checks are not wired") {
		t.Fatalf("missing hook not reported: %v", r.Errors)
	}
}

// A field the check clears is stored cleared, and its warning is reported.
func TestApply_CheckWarningsReportedAndClearedFieldStored(t *testing.T) {
	dom, _, _, deps := dcDeps()
	meta := dcMeta()
	bad := "include /etc/shadow;"
	meta.Domains[0].NginxCustomDirectives = &bad
	meta.Domains = meta.Domains[:1]
	deps.CheckDomain = func(_ context.Context, row *models.Domain, _ string) ([]string, error) {
		row.NginxCustomDirectives = nil
		return []string{"custom nginx directives dropped: forbidden directive: include"}, nil
	}
	r := Apply(context.Background(), meta, deps)
	if len(dom.created) != 1 || dom.created[0].NginxCustomDirectives != nil {
		t.Fatalf("want good.org stored without directives, got %+v", dom.created)
	}
	if !hasError(r.Errors, "good.org): custom nginx directives dropped") {
		t.Fatalf("warning not reported: %v", r.Errors)
	}
}

// GH #1816 / ADR-0170 decision 2: an admin-run restore vouches for the names
// it recreates (verified, method restore), including archives from before
// the ownership field; a row the archive records as pending stays pending
// with a fresh challenge token.
func TestApply_RestoredOwnership(t *testing.T) {
	dom, _, _, deps := dcDeps()
	deps.CheckDomain = func(context.Context, *models.Domain, string) ([]string, error) { return nil, nil }
	meta := dcMeta()
	meta.Domains[0].OwnershipStatus = ""                     // good.org: an older archive
	meta.Domains[1].OwnershipStatus = models.OwnershipPending // bad.org: pending at the source
	Apply(context.Background(), meta, deps)

	if len(dom.created) != 2 {
		t.Fatalf("want both domains restored, got %d", len(dom.created))
	}
	byName := map[string]models.OwnershipState{}
	for _, d := range dom.created {
		byName[d.Name] = d.OwnershipState
	}
	if got := byName["good.org"]; got.OwnershipStatus != models.OwnershipVerified || got.OwnershipMethod != models.OwnershipMethodRestore {
		t.Fatalf("an admin restore must verify the name (restore), got %s/%s", got.OwnershipStatus, got.OwnershipMethod)
	}
	if got := byName["bad.org"]; got.OwnershipStatus != models.OwnershipPending || len(got.OwnershipToken) != 64 {
		t.Fatalf("a pending row must stay pending with a fresh token, got %s token=%q", got.OwnershipStatus, got.OwnershipToken)
	}
}
