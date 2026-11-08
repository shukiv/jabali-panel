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

// A restore keeps to the account's hosting package limits on databases and
// database users, as a database or database user created in the panel does
// (dbops.Create, POST /database-users).

// capUsers is the account being restored, on the package pkg (nil: none).
type capUsers struct {
	repository.UserRepository
	pkg *string
}

func (r capUsers) FindByID(_ context.Context, id string) (*models.User, error) {
	u := "alice"
	return &models.User{ID: id, Username: &u, PackageID: r.pkg}, nil
}

type capPackages struct {
	repository.PackageRepository
	pkg *models.HostingPackage
	err error
}

func (r capPackages) FindByID(context.Context, string) (*models.HostingPackage, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.pkg == nil {
		return nil, repository.ErrNotFound
	}
	return r.pkg, nil
}

// capDBs and capDBUsers count the owner's rows for CreateWithinCap, checking
// the cap before the insert as the repository does.
type capDBs struct{ *maDBs }

func (r capDBs) CreateWithinCap(ctx context.Context, d *models.Database, max uint32) error {
	n := 0
	for _, e := range r.rows {
		if e.UserID == d.UserID {
			n++
		}
	}
	if n >= int(max) {
		return repository.ErrDatabaseCapExceeded
	}
	return r.Create(ctx, d)
}

type capDBUsers struct{ *maDBUsers }

func (r capDBUsers) CreateWithinCap(ctx context.Context, u *models.DatabaseUser, max uint32) error {
	n := 0
	for _, e := range r.rows {
		if e.UserID == u.UserID {
			n++
		}
	}
	if n >= int(max) {
		return repository.ErrDatabaseUserCapExceeded
	}
	return r.Create(ctx, u)
}

// capMeta is alice's bundle: databases alice_wp (db1) and alice_shop (db2),
// each with its own database user (du1, du2) granted on it.
func capMeta() *internalbackup.AccountMetadata {
	alice := "alice"
	return &internalbackup.AccountMetadata{
		User: internalbackup.MetadataUser{ID: "u1", Username: &alice},
		Databases: []internalbackup.MetadataDatabase{
			{ID: "db1", Name: "alice_wp", Engine: "mariadb"},
			{ID: "db2", Name: "alice_shop", Engine: "mariadb"},
		},
		DatabaseUsers: []internalbackup.MetadataDatabaseUser{
			{ID: "du1", Username: "alice_u1", Engine: "mariadb", NativePasswordHash: maHash,
				Grants: []internalbackup.MetadataDatabaseUserGrant{{ID: "g1", DatabaseID: "db1", DatabaseName: "alice_wp", GrantLevel: "rw", Privileges: "ALL"}}},
			{ID: "du2", Username: "alice_u2", Engine: "mariadb", NativePasswordHash: maHash,
				Grants: []internalbackup.MetadataDatabaseUserGrant{{ID: "g2", DatabaseID: "db2", DatabaseName: "alice_shop", GrantLevel: "rw", Privileges: "ALL"}}},
		},
	}
}

func applyWithPackage(f *maFixture, pkgs capPackages, meta *internalbackup.AccountMetadata) ApplyResult {
	pkgID := "pkg1"
	return Apply(context.Background(), meta, Deps{
		Users: capUsers{pkg: &pkgID}, Packages: pkgs,
		Databases: capDBs{f.dbs}, DatabaseUsers: capDBUsers{f.users}, DatabaseGrants: f.grants, Agent: f.agent,
	})
}

func hasErrorWith(r ApplyResult, parts ...string) bool {
	for _, e := range r.Errors {
		ok := true
		for _, p := range parts {
			ok = ok && strings.Contains(e, p)
		}
		if ok {
			return true
		}
	}
	return false
}

// A package that allows one database and one database user: the first of
// each is restored, the second is not, and the report says why.
func TestApply_RestoreKeepsToThePackageDatabaseLimits(t *testing.T) {
	f := newMAFixture()
	r := applyWithPackage(f, capPackages{pkg: &models.HostingPackage{MaxDatabases: 1, MaxDatabaseUsers: 1}}, capMeta())

	if f.dbs.rows["db1"] == nil || f.dbs.rows["db2"] != nil {
		t.Fatalf("database rows %v: want db1 only", f.dbs.rows)
	}
	if !hasErrorWith(r, "db2", "alice_shop", "not restored", "allows 1 database") {
		t.Errorf("errors %v: want db2 refused for the package's database limit", r.Errors)
	}
	if f.users.rows["du1"] == nil || f.users.rows["du2"] != nil {
		t.Fatalf("database user rows %v: want du1 only", f.users.rows)
	}
	if !hasErrorWith(r, "du2", "alice_u2", "not restored", "allows 1 database user") {
		t.Errorf("errors %v: want du2 refused for the package's database user limit", r.Errors)
	}
	if f.grants.rows["g1"] == nil || f.grants.rows["g2"] != nil {
		t.Errorf("grant rows %v: want g1 only", f.grants.rows)
	}
	for _, c := range f.agent.dbUserCalls() {
		if c.params["db_user_name"] == "alice_u2" {
			t.Errorf("agent %s for the refused user alice_u2: %v", c.cmd, c.params)
		}
	}
	if r.Databases != 1 || r.DatabaseUsers != 1 {
		t.Errorf("counts databases=%d users=%d, want 1 and 1", r.Databases, r.DatabaseUsers)
	}
}

// What the account already has counts: at its limit, a restore adds no
// database, but a database it already has is still matched (its grants come
// back), not reported as over the limit.
func TestApply_RestoreAtTheLimitStillMatchesWhatTheAccountHas(t *testing.T) {
	f := newMAFixture()
	f.dbs.rows["db1"] = &models.Database{ID: "db1", UserID: "u1", Name: "alice_wp", Engine: "mariadb"}
	f.users.rows["du1"] = &models.DatabaseUser{ID: "du1", UserID: "u1", Username: "alice_u1", Engine: "mariadb"}
	r := applyWithPackage(f, capPackages{pkg: &models.HostingPackage{MaxDatabases: 1, MaxDatabaseUsers: 1}}, capMeta())

	if f.dbs.rows["db2"] != nil || f.users.rows["du2"] != nil {
		t.Fatalf("rows dbs=%v users=%v: nothing new fits the limit", f.dbs.rows, f.users.rows)
	}
	if hasErrorWith(r, "db1", "allows") || hasErrorWith(r, "du1", "allows") {
		t.Errorf("errors %v: db1 and du1 are the account's own, not over the limit", r.Errors)
	}
	if f.grants.rows["g1"] == nil {
		t.Errorf("grant rows %v: g1 on the account's own db1 should come back", f.grants.rows)
	}
	if !hasErrorWith(r, "db2", "allows 1 database") || !hasErrorWith(r, "du2", "allows 1 database user") {
		t.Errorf("errors %v: want db2 and du2 refused for the limits", r.Errors)
	}
}

// A package that is set but can't be read restores no database and no
// database user: the limits are not lifted because they couldn't be read.
func TestApply_RestoreWithAnUnreadablePackageRestoresNoDatabases(t *testing.T) {
	f := newMAFixture()
	r := applyWithPackage(f, capPackages{err: errors.New("db down")}, capMeta())

	if len(f.dbs.rows) != 0 || len(f.users.rows) != 0 || len(f.grants.rows) != 0 {
		t.Fatalf("rows dbs=%v users=%v grants=%v: want none", f.dbs.rows, f.users.rows, f.grants.rows)
	}
	if !hasErrorWith(r, "databases", "not restored") {
		t.Errorf("errors %v: want the databases refused", r.Errors)
	}
	if len(f.agent.dbUserCalls()) != 0 {
		t.Errorf("agent calls %v: want none", f.agent.dbUserCalls())
	}
}

// A package that sets no limit, or that no longer exists, restores
// everything (no package = no limit, GH #282).
func TestApply_RestoreWithoutLimitsRestoresEverything(t *testing.T) {
	for name, pkgs := range map[string]capPackages{
		"no limits":       {pkg: &models.HostingPackage{}},
		"package deleted": {},
	} {
		t.Run(name, func(t *testing.T) {
			f := newMAFixture()
			r := applyWithPackage(f, pkgs, capMeta())
			if len(f.dbs.rows) != 2 || len(f.users.rows) != 2 || len(f.grants.rows) != 2 {
				t.Fatalf("rows dbs=%v users=%v grants=%v (errors %v): want everything", f.dbs.rows, f.users.rows, f.grants.rows, r.Errors)
			}
		})
	}
}
