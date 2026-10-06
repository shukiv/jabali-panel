package backupmetadata

import (
	"context"
	"testing"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// SECURITY (GH #1993 follow-up): an uploaded account backup is untrusted, and
// every row id in it is chosen by whoever made the file. A row the bundle
// names that already exists on this server, but under ANOTHER account, must
// never get the restore's children attached to it: a mailbox on someone
// else's domain, a forwarder or share on someone else's mailbox, a grant on
// someone else's database, an app install on someone else's site, an FTP
// account in someone else's home.

type owDomains struct {
	repository.DomainRepository
	existing map[string]models.Domain
	created  []models.Domain
}

func (r *owDomains) FindByID(_ context.Context, id string) (*models.Domain, error) {
	if d, ok := r.existing[id]; ok {
		return &d, nil
	}
	return nil, repository.ErrNotFound
}
func (r *owDomains) Create(_ context.Context, d *models.Domain) error {
	r.created = append(r.created, *d)
	return nil
}

type owMailboxes struct {
	repository.MailboxRepository
	existing map[string]models.Mailbox
	created  []string
}

func (r *owMailboxes) FindByID(_ context.Context, id string) (*models.Mailbox, error) {
	if m, ok := r.existing[id]; ok {
		return &m, nil
	}
	return nil, repository.ErrNotFound
}
func (r *owMailboxes) Create(_ context.Context, m *models.Mailbox) error {
	r.created = append(r.created, m.ID)
	return nil
}

type owAutoresponders struct {
	repository.EmailAutoresponderRepository
	updated []string
}

func (r *owAutoresponders) Update(_ context.Context, a *models.EmailAutoresponder) error {
	r.updated = append(r.updated, a.MailboxID)
	return nil
}

type owShares struct {
	repository.MailboxShareRepository
	created []string
}

func (r *owShares) FindByID(context.Context, string) (*models.MailboxShare, error) {
	return nil, repository.ErrNotFound
}
func (r *owShares) Create(_ context.Context, s *models.MailboxShare) error {
	r.created = append(r.created, s.ID)
	return nil
}

type owForwarders struct {
	repository.EmailForwarderRepository
	created []string
}

func (r *owForwarders) FindByID(context.Context, string) (*models.EmailForwarder, error) {
	return nil, repository.ErrNotFound
}
func (r *owForwarders) Create(_ context.Context, f *models.EmailForwarder) error {
	r.created = append(r.created, f.ID)
	return nil
}

type owDatabases struct {
	repository.DatabaseRepository
	existing map[string]models.Database
}

func (r *owDatabases) FindByID(_ context.Context, id string) (*models.Database, error) {
	if d, ok := r.existing[id]; ok {
		return &d, nil
	}
	return nil, repository.ErrNotFound
}
func (r *owDatabases) Create(_ context.Context, d *models.Database) error {
	if _, ok := r.existing[d.ID]; ok {
		return repository.ErrConflict
	}
	return nil
}

type owDBUsers struct {
	repository.DatabaseUserRepository
	existing map[string]models.DatabaseUser
}

func (r *owDBUsers) FindByID(_ context.Context, id string) (*models.DatabaseUser, error) {
	if u, ok := r.existing[id]; ok {
		return &u, nil
	}
	return nil, repository.ErrNotFound
}
func (r *owDBUsers) Create(_ context.Context, u *models.DatabaseUser) error {
	if _, ok := r.existing[u.ID]; ok {
		return repository.ErrConflict
	}
	return nil
}

type owGrants struct {
	repository.DatabaseUserGrantRepository
	created []string
}

func (r *owGrants) Create(_ context.Context, g *models.DatabaseUserGrant) error {
	r.created = append(r.created, g.ID)
	return nil
}

type owInstalls struct {
	repository.ApplicationInstallRepository
	created []string
}

func (r *owInstalls) Create(_ context.Context, a *models.ApplicationInstall) error {
	r.created = append(r.created, a.ID)
	return nil
}

type owFtp struct {
	repository.FtpAccountRepository
	created []string
}

func (r *owFtp) Create(_ context.Context, a *models.FtpAccount) error {
	r.created = append(r.created, a.ID)
	return nil
}

type owFixture struct {
	doms    *owDomains
	mbs     *owMailboxes
	ars     *owAutoresponders
	shares  *owShares
	fwds    *owForwarders
	dbs     *owDatabases
	dbUsers *owDBUsers
	grants  *owGrants
	apps    *owInstalls
	ftp     *owFtp
}

func newOwFixture() *owFixture {
	return &owFixture{
		doms:    &owDomains{existing: map[string]models.Domain{"d-bob": {ID: "d-bob", UserID: "u-bob", Name: "bob.org"}}},
		mbs:     &owMailboxes{existing: map[string]models.Mailbox{"mb-bob": {ID: "mb-bob", DomainID: "d-bob", LocalPart: "bob"}}},
		ars:     &owAutoresponders{},
		shares:  &owShares{},
		fwds:    &owForwarders{},
		dbs:     &owDatabases{existing: map[string]models.Database{"db-bob": {ID: "db-bob", UserID: "u-bob", Name: "bob_db"}}},
		dbUsers: &owDBUsers{existing: map[string]models.DatabaseUser{"du-bob": {ID: "du-bob", UserID: "u-bob", Username: "bob_user"}}},
		grants:  &owGrants{},
		apps:    &owInstalls{},
		ftp:     &owFtp{},
	}
}

func (f *owFixture) deps() Deps {
	return Deps{
		Users: &createGuardUsersRepo{}, Domains: f.doms, Mailboxes: f.mbs,
		Autoresponders: f.ars, MailboxShares: f.shares, Forwarders: f.fwds,
		Databases: f.dbs, DatabaseUsers: f.dbUsers, DatabaseGrants: f.grants,
		AppInstalls: f.apps, FtpAccounts: f.ftp,
		MailAddresses: &dcReleaser{},
		CheckDomain:   func(context.Context, *models.Domain, string) ([]string, error) { return nil, nil },
	}
}

func owMeta() *internalbackup.AccountMetadata {
	uname := "alice"
	return &internalbackup.AccountMetadata{
		User: internalbackup.MetadataUser{ID: "u1", Email: "alice@example.com", Username: &uname},
	}
}

func aliceDomain(id string) internalbackup.MetadataDomain {
	return internalbackup.MetadataDomain{ID: id, Name: "alice.org", DocRoot: "/home/alice/domains/alice.org/public_html"}
}

func TestApply_DomainOfAnotherAccountIsRefusedWithItsChildren(t *testing.T) {
	f := newOwFixture()
	m := owMeta()
	mbID := "mb-new"
	dm := aliceDomain("d-bob") // the id of bob's domain
	dm.Mailboxes = []internalbackup.MetadataMailbox{{ID: mbID, LocalPart: "info"}}
	dm.Forwarders = []internalbackup.MetadataForwarder{{ID: "fw1", MailboxID: &mbID, Target: "x@evil.example"}}
	m.Domains = []internalbackup.MetadataDomain{dm}
	m.AppInstalls = []internalbackup.MetadataAppInstall{{ID: "ai1", DomainID: "d-bob", AppType: "wordpress"}}

	r := Apply(context.Background(), m, f.deps())

	if len(f.mbs.created) != 0 || len(f.fwds.created) != 0 || len(f.apps.created) != 0 {
		t.Fatalf("children attached to another account's domain: mailboxes=%v forwarders=%v apps=%v",
			f.mbs.created, f.fwds.created, f.apps.created)
	}
	if !hasError(r.Errors, "another account") {
		t.Fatalf("errors %v should say the domain belongs to another account", r.Errors)
	}
}

func TestApply_MailboxOfAnotherDomainIsLeftAlone(t *testing.T) {
	f := newOwFixture()
	m := owMeta()
	dm := aliceDomain("d1")
	away := "away"
	dm.Mailboxes = []internalbackup.MetadataMailbox{{
		ID: "mb-bob", LocalPart: "info", // bob's mailbox id
		Autoresponder: &internalbackup.MetadataAutoresponder{Enabled: true, Subject: &away},
		SharedWith:    []internalbackup.MetadataMailboxShare{{ID: "sh1", SharedWithMailboxID: "mb-evil", Rights: `{"read":true}`}},
	}}
	m.Domains = []internalbackup.MetadataDomain{dm}

	r := Apply(context.Background(), m, f.deps())

	if len(f.ars.updated) != 0 || len(f.shares.created) != 0 {
		t.Fatalf("wrote to another domain's mailbox: autoresponders=%v shares=%v", f.ars.updated, f.shares.created)
	}
	if !hasError(r.Errors, "another domain") {
		t.Fatalf("errors %v should say the mailbox belongs to another domain", r.Errors)
	}
}

func TestApply_ForwarderOnAnotherAccountsMailboxIsSkipped(t *testing.T) {
	f := newOwFixture()
	m := owMeta()
	bob := "mb-bob"
	dm := aliceDomain("d1")
	dm.Forwarders = []internalbackup.MetadataForwarder{{ID: "fw1", MailboxID: &bob, Target: "x@evil.example"}}
	m.Domains = []internalbackup.MetadataDomain{dm}

	r := Apply(context.Background(), m, f.deps())

	if len(f.fwds.created) != 0 {
		t.Fatalf("forwarder created on another account's mailbox: %v", f.fwds.created)
	}
	if !hasError(r.Errors, "forwarder fw1") {
		t.Fatalf("errors %v should name the skipped forwarder", r.Errors)
	}
}

func TestApply_GrantOnAnotherAccountsDatabaseIsSkipped(t *testing.T) {
	f := newOwFixture()
	m := owMeta()
	m.DatabaseUsers = []internalbackup.MetadataDatabaseUser{{
		ID: "du1", Username: "alice_u",
		Grants: []internalbackup.MetadataDatabaseUserGrant{{ID: "g1", DatabaseID: "db-bob"}},
	}}

	r := Apply(context.Background(), m, f.deps())

	if len(f.grants.created) != 0 {
		t.Fatalf("grant created on another account's database: %v", f.grants.created)
	}
	if !hasError(r.Errors, "db_grant g1") {
		t.Fatalf("errors %v should name the skipped grant", r.Errors)
	}
}

func TestApply_GrantForAnotherAccountsDBUserIsSkipped(t *testing.T) {
	f := newOwFixture()
	m := owMeta()
	m.Databases = []internalbackup.MetadataDatabase{{ID: "db1", Name: "alice_db"}}
	m.DatabaseUsers = []internalbackup.MetadataDatabaseUser{{
		ID: "du-bob", Username: "bob_user", // bob's database user id
		Grants: []internalbackup.MetadataDatabaseUserGrant{{ID: "g1", DatabaseID: "db1"}},
	}}

	Apply(context.Background(), m, f.deps())

	if len(f.grants.created) != 0 {
		t.Fatalf("grant created for another account's database user: %v", f.grants.created)
	}
}

func TestApply_AppInstallOnAnotherAccountsDomainOrDatabaseIsSkipped(t *testing.T) {
	f := newOwFixture()
	m := owMeta()
	bobDB := "db-bob"
	m.Domains = []internalbackup.MetadataDomain{aliceDomain("d1")}
	m.AppInstalls = []internalbackup.MetadataAppInstall{
		{ID: "ai-foreign-domain", DomainID: "d-bob", AppType: "wordpress"},
		{ID: "ai-foreign-db", DomainID: "d1", DBID: &bobDB, AppType: "wordpress"},
	}

	Apply(context.Background(), m, f.deps())

	if len(f.apps.created) != 0 {
		t.Fatalf("app installs tied to another account's domain or database: %v", f.apps.created)
	}
}

func TestApply_FtpAccountOutsideTheAccountsHomeIsSkipped(t *testing.T) {
	f := newOwFixture()
	m := owMeta()
	m.FtpAccounts = []internalbackup.MetadataFtpAccount{
		{ID: "ftp-ok", Username: "alice_ftp", HomePath: "/home/alice/domains/alice.org"},
		{ID: "ftp-home", Username: "alice_x", HomePath: "/home/bob"},
		{ID: "ftp-dotdot", Username: "alice_y", HomePath: "/home/alice/../bob"},
		{ID: "ftp-jail", Username: "alice_z", HomePath: "/home/alice", JailPath: "/var/lib/jabali-ftp-jails/bob/bob_x"},
		{ID: "ftp-okjail", Username: "alice_j", HomePath: "/home/alice", JailPath: "/var/lib/jabali-ftp-jails/alice/alice_j"},
	}

	Apply(context.Background(), m, f.deps())

	if len(f.ftp.created) != 2 || f.ftp.created[0] != "ftp-ok" || f.ftp.created[1] != "ftp-okjail" {
		t.Fatalf("ftp accounts created = %v, want ftp-ok and ftp-okjail", f.ftp.created)
	}
}

// A same-server restore names the account's OWN existing rows: those keep
// working exactly as before (the domain is skipped, its children restored).
func TestApply_OwnExistingDomainStillGetsItsChildren(t *testing.T) {
	f := newOwFixture()
	f.doms.existing["d1"] = models.Domain{ID: "d1", UserID: "u1", Name: "alice.org"}
	m := owMeta()
	dm := aliceDomain("d1")
	dm.Mailboxes = []internalbackup.MetadataMailbox{{ID: "mb1", LocalPart: "info"}}
	m.Domains = []internalbackup.MetadataDomain{dm}

	r := Apply(context.Background(), m, f.deps())

	if len(f.mbs.created) != 1 {
		t.Fatalf("mailboxes created = %v (errors %v), want mb1", f.mbs.created, r.Errors)
	}
}
