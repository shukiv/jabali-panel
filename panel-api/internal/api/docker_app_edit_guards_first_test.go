package api

import (
	"context"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// An edit that a domain check refuses must leave the app's current domain
// alone. The edit used to drop the app's managed row (and, for a name the app
// was leaving, its vhost and certificate) before the alias, mail-hostname,
// cross-tenant and ownership checks ran, so a refused edit still took the
// app's domain away.

// guardDomainRepo answers the non-admin cross-tenant check's child lookup:
// the domains under a name, none unless the test adds some.
type guardDomainRepo struct {
	*editDomainRepo
	subs map[string][]models.Domain
}

func (g guardDomainRepo) FindStrictSubdomains(_ context.Context, name string) ([]models.Domain, error) {
	return g.subs[name], nil
}

// aliasHit says one hostname is another domain's alias.
type aliasHit struct {
	repository.WebDomainAliasRepository
	host string
}

func (a aliasHit) FindByHostname(_ context.Context, host string) (*models.WebDomainAlias, error) {
	if host == a.host {
		return &models.WebDomainAlias{Hostname: host}, nil
	}
	return nil, repository.ErrNotFound
}

// tenantEditHandler is portsOnlyEditHandler with the cross-tenant lookups a
// tenant's edit runs.
func tenantEditHandler(t *testing.T, domains ...models.Domain) (*dockerAppHandler, *editDomainRepo, *agent.MockClient) {
	t.Helper()
	h, dom, mock := portsOnlyEditHandler(t, domains...)
	h.cfg.Domains = guardDomainRepo{editDomainRepo: dom, subs: map[string][]models.Domain{}}
	return h, dom, mock
}

func wantEditRefused(t *testing.T, err error, code string) {
	t.Helper()
	var de *dockerEditError
	if !errors.As(err, &de) || de.Code != code {
		t.Fatalf("err = %v, want a %s refusal", err, code)
	}
}

func TestEditDomainPorts_ARefusedSameNameKeepsTheRow(t *testing.T) {
	h, dom, mock := tenantEditHandler(t, managedDomain("cur", "notes.example.com", "a1"))
	// A clash that appeared after the app got its name: the panel now uses it.
	h.cfg.ServerSettings = &fakeSettingsRepo{s: &models.ServerSettings{Hostname: "notes.example.com"}}

	name := "notes.example.com"
	err := h.editDomainPorts(context.Background(), h.cfg.Repo.(movedPortRepo).app, &name, nil, "u1", false)
	wantEditRefused(t, err, "domain_conflicts_mail_hostname")
	if dom.deleted["cur"] {
		t.Fatal("the refused edit deleted the app's domain row")
	}
	if len(dom.created) != 0 {
		t.Fatalf("a refused edit created %v", dom.created)
	}
	if got := vhostRemoves(t, mock); len(got) != 0 {
		t.Fatalf("a refused edit removed vhosts %+v", got)
	}
}

func TestEditDomainPorts_ARefusedNewNameKeepsTheOldDomain(t *testing.T) {
	h, dom, mock := tenantEditHandler(t, managedDomain("cur", "notes.example.com", "a1"))
	h.cfg.ServerSettings = &fakeSettingsRepo{s: &models.ServerSettings{Hostname: "panel.example.com"}}

	name := "panel.example.com"
	err := h.editDomainPorts(context.Background(), h.cfg.Repo.(movedPortRepo).app, &name, nil, "u1", false)
	wantEditRefused(t, err, "domain_conflicts_mail_hostname")
	if dom.deleted["cur"] {
		t.Fatal("the refused edit deleted the app's current domain row")
	}
	if got := vhostRemoves(t, mock); len(got) != 0 {
		t.Fatalf("the refused edit removed vhosts %+v; the app's domain stops serving", got)
	}
}

func TestEditDomainPorts_AnotherUsersDomainKeepsTheOldDomain(t *testing.T) {
	h, dom, mock := tenantEditHandler(t, managedDomain("cur", "notes.example.com", "a1"))
	dom.byName["taken.example.com"] = &models.Domain{ID: "taken", UserID: "u2", Name: "taken.example.com"}

	name := "taken.example.com"
	err := h.editDomainPorts(context.Background(), h.cfg.Repo.(movedPortRepo).app, &name, nil, "u1", false)
	wantEditRefused(t, err, "domain_in_use")
	if dom.deleted["cur"] {
		t.Fatal("the refused edit deleted the app's current domain row")
	}
	if got := vhostRemoves(t, mock); len(got) != 0 {
		t.Fatalf("the refused edit removed vhosts %+v", got)
	}
}

// The checks still pass a clean edit through, and a new name still replaces
// the old one.
func TestEditDomainPorts_ACleanNewNameStillMoves(t *testing.T) {
	h, dom, mock := tenantEditHandler(t, managedDomain("cur", "notes.example.com", "a1"))
	h.cfg.ServerSettings = &fakeSettingsRepo{s: &models.ServerSettings{Hostname: "panel.example.com"}}

	name := "wiki.example.com"
	if err := h.editDomainPorts(context.Background(), h.cfg.Repo.(movedPortRepo).app, &name, nil, "u1", false); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if !dom.deleted["cur"] {
		t.Fatal("the old name was not dropped")
	}
	if len(dom.created) != 1 || dom.created[0].Name != "wiki.example.com" {
		t.Fatalf("created %v, want wiki.example.com", dom.created)
	}
	wantPurged(t, mock, "notes.example.com")
}

// A name another Docker app's domain row holds is refused up front too; the
// create used to fail on the duplicate name only after the app's own row was
// gone.
func TestEditDomainPorts_AnotherAppsDomainKeepsTheOldDomain(t *testing.T) {
	h, dom, mock := tenantEditHandler(t, managedDomain("cur", "notes.example.com", "a1"))
	other := managedDomain("theirs", "board.example.com", "a2")
	other.UserID = "u2"
	dom.byName["board.example.com"] = &other

	name := "board.example.com"
	err := h.editDomainPorts(context.Background(), h.cfg.Repo.(movedPortRepo).app, &name, nil, "u1", false)
	wantEditRefused(t, err, "domain_in_use")
	if dom.deleted["cur"] || dom.deleted["theirs"] {
		t.Fatalf("the refused edit deleted %v", dom.deleted)
	}
	if len(dom.created) != 0 {
		t.Fatalf("a refused edit created %v", dom.created)
	}
	if got := vhostRemoves(t, mock); len(got) != 0 {
		t.Fatalf("the refused edit removed vhosts %+v", got)
	}
}

// lookupFailsRepo can't read the other app (a database error, not "no such
// app").
type lookupFailsRepo struct{ movedPortRepo }

func (lookupFailsRepo) FindByID(_ context.Context, id string) (*models.DockerApp, error) {
	return nil, errors.New("database is down")
}

// Not knowing whether the other app still exists must not read as "it is
// gone": that would let the edit clear the other app's row as an orphan.
func TestEditDomainPorts_AnUnreadableOwnerFailsClosed(t *testing.T) {
	h, dom, _ := tenantEditHandler(t, managedDomain("cur", "notes.example.com", "a1"))
	h.cfg.Repo = lookupFailsRepo{h.cfg.Repo.(movedPortRepo)}
	other := managedDomain("theirs", "board.example.com", "a2")
	other.UserID = "u2"
	dom.byName["board.example.com"] = &other

	name := "board.example.com"
	app := h.cfg.Repo.(lookupFailsRepo).app
	err := h.editDomainPorts(context.Background(), app, &name, nil, "u1", false)
	wantEditRefused(t, err, "db_app_lookup")
	if dom.deleted["cur"] || dom.deleted["theirs"] {
		t.Fatalf("an edit that could not read the owner deleted %v", dom.deleted)
	}
}

func TestEditDomainPorts_AnAliasNameKeepsTheOldDomain(t *testing.T) {
	h, dom, mock := tenantEditHandler(t, managedDomain("cur", "notes.example.com", "a1"))
	h.cfg.WebDomainAliases = aliasHit{host: "shop.example.com"}

	name := "shop.example.com"
	err := h.editDomainPorts(context.Background(), h.cfg.Repo.(movedPortRepo).app, &name, nil, "u1", false)
	wantEditRefused(t, err, "domain_conflicts_alias")
	if dom.deleted["cur"] {
		t.Fatal("the refused edit deleted the app's current domain row")
	}
	if got := vhostRemoves(t, mock); len(got) != 0 {
		t.Fatalf("the refused edit removed vhosts %+v", got)
	}
}

func TestEditDomainPorts_ANameOverAnotherTenantKeepsTheOldDomain(t *testing.T) {
	h, dom, mock := tenantEditHandler(t, managedDomain("cur", "notes.example.com", "a1"))
	h.cfg.Domains.(guardDomainRepo).subs["corp.example.com"] = []models.Domain{{ID: "x", UserID: "u2", Name: "mail.corp.example.com"}}

	name := "corp.example.com"
	err := h.editDomainPorts(context.Background(), h.cfg.Repo.(movedPortRepo).app, &name, nil, "u1", false)
	wantEditRefused(t, err, "domain_conflicts_tenant")
	if dom.deleted["cur"] {
		t.Fatal("the refused edit deleted the app's current domain row")
	}
	if got := vhostRemoves(t, mock); len(got) != 0 {
		t.Fatalf("the refused edit removed vhosts %+v", got)
	}
}

// settingsDown can't be read.
type settingsDown struct{ fakeSettingsRepo }

func (*settingsDown) Get(context.Context) (*models.ServerSettings, error) {
	return nil, errors.New("database is down")
}

// Moving onto the tenant's own existing domain attaches the app to it; the
// name-claim checks (which fail closed on a lookup error) are for names the
// app would create, so they don't stand in its way.
func TestEditDomainPorts_AttachingToAnOwnDomainSkipsTheNameClaimChecks(t *testing.T) {
	h, dom, _ := tenantEditHandler(t, managedDomain("cur", "notes.example.com", "a1"))
	h.cfg.ServerSettings = &settingsDown{}
	dom.byName["blog.example.com"] = &models.Domain{ID: "blog", UserID: "u1", Name: "blog.example.com"}

	name := "blog.example.com"
	if err := h.editDomainPorts(context.Background(), h.cfg.Repo.(movedPortRepo).app, &name, nil, "u1", false); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if _, ok := dom.attached["blog"]; !ok {
		t.Fatalf("attached %v, want the app on blog.example.com", dom.attached)
	}
}
