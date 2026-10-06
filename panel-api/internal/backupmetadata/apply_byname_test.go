package backupmetadata

import (
	"context"
	"testing"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1993: an admin often creates the account, and sometimes its domain,
// mailbox or database, on the new server before restoring the backup. Those
// rows have other ids than the backup's. The restore must attach the backup's
// mailboxes, forwarders, grants and app installs to the account's own row of
// the same name instead of refusing them, and never to another account's.

type bnMailboxes struct {
	repository.MailboxRepository
	existing []models.Mailbox
	created  []models.Mailbox
}

func (r *bnMailboxes) FindByID(_ context.Context, id string) (*models.Mailbox, error) {
	for _, m := range r.existing {
		if m.ID == id {
			return &m, nil
		}
	}
	return nil, repository.ErrNotFound
}
func (r *bnMailboxes) FindByEmail(_ context.Context, email string) (*models.Mailbox, error) {
	for _, m := range r.existing {
		if m.EmailCached == email {
			return &m, nil
		}
	}
	return nil, repository.ErrNotFound
}
func (r *bnMailboxes) Create(_ context.Context, m *models.Mailbox) error {
	r.created = append(r.created, *m)
	return nil
}
func (r *bnMailboxes) ListByDomainID(context.Context, string, repository.ListOptions) ([]models.Mailbox, int64, error) {
	return nil, 0, nil
}

type bnForwarders struct {
	repository.EmailForwarderRepository
	created []models.EmailForwarder
}

func (r *bnForwarders) FindByID(context.Context, string) (*models.EmailForwarder, error) {
	return nil, repository.ErrNotFound
}
func (r *bnForwarders) Create(_ context.Context, f *models.EmailForwarder) error {
	r.created = append(r.created, *f)
	return nil
}

type bnInstalls struct {
	repository.ApplicationInstallRepository
	created []models.ApplicationInstall
}

func (r *bnInstalls) Create(_ context.Context, a *models.ApplicationInstall) error {
	r.created = append(r.created, *a)
	return nil
}

// bnDatabases refuses a second row of an existing name, like the unique key.
type bnDatabases struct {
	repository.DatabaseRepository
	rows []models.Database
}

func (r *bnDatabases) FindByID(_ context.Context, id string) (*models.Database, error) {
	for _, d := range r.rows {
		if d.ID == id {
			return &d, nil
		}
	}
	return nil, repository.ErrNotFound
}
func (r *bnDatabases) List(context.Context, repository.ListOptions) ([]models.Database, int64, error) {
	return r.rows, int64(len(r.rows)), nil
}
func (r *bnDatabases) Create(_ context.Context, d *models.Database) error {
	for _, e := range r.rows {
		if e.Name == d.Name || e.ID == d.ID {
			return repository.ErrConflict
		}
	}
	r.rows = append(r.rows, *d)
	return nil
}

type bnDBUsers struct {
	repository.DatabaseUserRepository
	rows []models.DatabaseUser
}

func (r *bnDBUsers) FindByID(_ context.Context, id string) (*models.DatabaseUser, error) {
	for _, u := range r.rows {
		if u.ID == id {
			return &u, nil
		}
	}
	return nil, repository.ErrNotFound
}
func (r *bnDBUsers) List(context.Context, repository.ListOptions) ([]models.DatabaseUser, int64, error) {
	return r.rows, int64(len(r.rows)), nil
}
func (r *bnDBUsers) Create(_ context.Context, u *models.DatabaseUser) error {
	for _, e := range r.rows {
		if e.Username == u.Username || e.ID == u.ID {
			return repository.ErrConflict
		}
	}
	r.rows = append(r.rows, *u)
	return nil
}

type bnFixture struct {
	doms    *owDomains
	mbs     *bnMailboxes
	ars     *owAutoresponders
	fwds    *bnForwarders
	dbs     *bnDatabases
	dbUsers *bnDBUsers
	grants  *bnGrants
	apps    *bnInstalls
}

type bnGrants struct {
	repository.DatabaseUserGrantRepository
	created []models.DatabaseUserGrant
}

func (r *bnGrants) Create(_ context.Context, g *models.DatabaseUserGrant) error {
	r.created = append(r.created, *g)
	return nil
}

// newBnFixture: alice (u1) already has alice.org (d-here) on this server; bob
// has bob.org.
func newBnFixture() *bnFixture {
	return &bnFixture{
		doms: &owDomains{existing: map[string]models.Domain{
			"d-here": {ID: "d-here", UserID: "u1", Name: "alice.org"},
			"d-bob":  {ID: "d-bob", UserID: "u-bob", Name: "bob.org"},
		}},
		mbs:     &bnMailboxes{},
		ars:     &owAutoresponders{},
		fwds:    &bnForwarders{},
		dbs:     &bnDatabases{},
		dbUsers: &bnDBUsers{},
		grants:  &bnGrants{},
		apps:    &bnInstalls{},
	}
}

func (f *bnFixture) deps() Deps {
	return Deps{
		Users: &createGuardUsersRepo{}, Domains: f.doms, Mailboxes: f.mbs,
		Autoresponders: f.ars, MailboxShares: &owShares{}, Forwarders: f.fwds,
		Databases: f.dbs, DatabaseUsers: f.dbUsers, DatabaseGrants: f.grants,
		AppInstalls:   f.apps,
		MailAddresses: &dcReleaser{},
		CheckDomain:   func(context.Context, *models.Domain, string) ([]string, error) { return nil, nil },
	}
}

func TestApply_RestoresUnderTheAccountsOwnDomainOfTheSameName(t *testing.T) {
	f := newBnFixture()
	m := owMeta()
	mbID := "mb-backup"
	dm := aliceDomain("d-backup") // alice.org under the old server's id
	dm.Mailboxes = []internalbackup.MetadataMailbox{{ID: mbID, LocalPart: "info", EmailCached: "info@alice.org"}}
	dm.Forwarders = []internalbackup.MetadataForwarder{{ID: "fw1", MailboxID: &mbID, Target: "info@alice.net"}}
	m.Domains = []internalbackup.MetadataDomain{dm}
	m.AppInstalls = []internalbackup.MetadataAppInstall{{ID: "ai1", DomainID: "d-backup", AppType: "wordpress"}}

	r := Apply(context.Background(), m, f.deps())

	if len(f.doms.created) != 0 {
		t.Errorf("created domains %v; alice.org already exists for the account", f.doms.created)
	}
	if len(f.mbs.created) != 1 || f.mbs.created[0].DomainID != "d-here" {
		t.Fatalf("mailboxes %+v, want info@ restored under d-here (errors %v)", f.mbs.created, r.Errors)
	}
	if len(f.fwds.created) != 1 || f.fwds.created[0].DomainID != "d-here" || *f.fwds.created[0].MailboxID != mbID {
		t.Errorf("forwarders %+v, want fw1 on d-here for %s", f.fwds.created, mbID)
	}
	if len(f.apps.created) != 1 || f.apps.created[0].DomainID != "d-here" {
		t.Errorf("app installs %+v, want ai1 on d-here (errors %v)", f.apps.created, r.Errors)
	}
}

func TestApply_UsesTheAccountsOwnMailboxOfTheSameAddress(t *testing.T) {
	f := newBnFixture()
	f.mbs.existing = []models.Mailbox{{ID: "mb-here", DomainID: "d-here", LocalPart: "info", EmailCached: "info@alice.org"}}
	m := owMeta()
	mbID := "mb-backup"
	away := "away"
	dm := aliceDomain("d-here")
	dm.Mailboxes = []internalbackup.MetadataMailbox{{ID: mbID, LocalPart: "info", EmailCached: "info@alice.org",
		Autoresponder: &internalbackup.MetadataAutoresponder{Enabled: true, Subject: &away}}}
	dm.Forwarders = []internalbackup.MetadataForwarder{{ID: "fw1", MailboxID: &mbID, Target: "info@alice.net"}}
	m.Domains = []internalbackup.MetadataDomain{dm}

	r := Apply(context.Background(), m, f.deps())

	if len(f.mbs.created) != 0 {
		t.Errorf("created mailboxes %+v; info@alice.org already exists", f.mbs.created)
	}
	if len(f.ars.updated) != 1 || f.ars.updated[0] != "mb-here" {
		t.Errorf("autoresponders written for %v, want mb-here", f.ars.updated)
	}
	if len(f.fwds.created) != 1 || *f.fwds.created[0].MailboxID != "mb-here" {
		t.Errorf("forwarders %+v, want fw1 on mb-here (errors %v)", f.fwds.created, r.Errors)
	}
}

func TestApply_GrantsAttachToTheAccountsOwnDatabaseOfTheSameName(t *testing.T) {
	for _, untrusted := range []bool{false, true} {
		f := newBnFixture()
		f.dbs.rows = []models.Database{{ID: "db-here", UserID: "u1", Name: "alice_shop"}}
		f.dbUsers.rows = []models.DatabaseUser{{ID: "du-here", UserID: "u1", Username: "alice_app"}}
		m := owMeta()
		m.Domains = []internalbackup.MetadataDomain{aliceDomain("d-here")}
		m.Databases = []internalbackup.MetadataDatabase{{ID: "db-backup", Name: "alice_shop"}}
		m.DatabaseUsers = []internalbackup.MetadataDatabaseUser{{ID: "du-backup", Username: "alice_app",
			Grants: []internalbackup.MetadataDatabaseUserGrant{{ID: "g1", DatabaseID: "db-backup"}}}}
		dbID := "db-backup"
		m.AppInstalls = []internalbackup.MetadataAppInstall{{ID: "ai1", DomainID: "d-here", DBID: &dbID, AppType: "wordpress"}}
		d := f.deps()
		d.Untrusted = untrusted

		r := Apply(context.Background(), m, d)

		if len(f.grants.created) != 1 || f.grants.created[0].DatabaseID != "db-here" || f.grants.created[0].DatabaseUserID != "du-here" {
			t.Errorf("untrusted=%v: grants %+v, want g1 on db-here for du-here (errors %v)", untrusted, f.grants.created, r.Errors)
		}
		if len(f.apps.created) != 1 || *f.apps.created[0].DBID != "db-here" {
			t.Errorf("untrusted=%v: app installs %+v, want ai1 on db-here (errors %v)", untrusted, f.apps.created, r.Errors)
		}
	}
}

func TestApply_AnotherAccountsRowsOfTheSameNameAreRefused(t *testing.T) {
	f := newBnFixture()
	f.mbs.existing = []models.Mailbox{{ID: "mb-bob", DomainID: "d-bob", LocalPart: "bob", EmailCached: "bob@bob.org"}}
	f.dbs.rows = []models.Database{{ID: "db-bob", UserID: "u-bob", Name: "alice_shop"}}
	f.dbUsers.rows = []models.DatabaseUser{{ID: "du-bob", UserID: "u-bob", Username: "alice_app"}}
	m := owMeta()
	mbID := "mb-backup"
	bob := internalbackup.MetadataDomain{ID: "d-backup", Name: "bob.org", DocRoot: "/home/alice/domains/bob.org/public_html",
		Mailboxes:  []internalbackup.MetadataMailbox{{ID: mbID, LocalPart: "bob", EmailCached: "bob@bob.org"}},
		Forwarders: []internalbackup.MetadataForwarder{{ID: "fw1", MailboxID: &mbID, Target: "x@evil.example"}}}
	m.Domains = []internalbackup.MetadataDomain{bob}
	m.Databases = []internalbackup.MetadataDatabase{{ID: "db-backup", Name: "alice_shop"}}
	m.DatabaseUsers = []internalbackup.MetadataDatabaseUser{{ID: "du-backup", Username: "alice_app",
		Grants: []internalbackup.MetadataDatabaseUserGrant{{ID: "g1", DatabaseID: "db-backup"}}}}
	m.AppInstalls = []internalbackup.MetadataAppInstall{{ID: "ai1", DomainID: "d-backup", AppType: "wordpress"}}

	r := Apply(context.Background(), m, f.deps())

	if len(f.doms.created) != 0 || len(f.mbs.created) != 0 || len(f.fwds.created) != 0 || len(f.apps.created) != 0 || len(f.grants.created) != 0 {
		t.Fatalf("attached to another account's rows: domains=%v mailboxes=%v forwarders=%v apps=%v grants=%v",
			f.doms.created, f.mbs.created, f.fwds.created, f.apps.created, f.grants.created)
	}
	for _, want := range []string{"a domain with this name belongs to another account", "a database user with this name belongs to another account"} {
		if !hasError(r.Errors, want) {
			t.Errorf("errors %v should contain %q", r.Errors, want)
		}
	}
}
