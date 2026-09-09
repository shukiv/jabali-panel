package dbops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- fakes for the reassign (GH #1609) path ---

type caUsers struct {
	repository.UserRepository
	byID map[string]*models.User
}

func (r *caUsers) FindByID(_ context.Context, id string) (*models.User, error) {
	if u, ok := r.byID[id]; ok {
		return u, nil
	}
	return nil, repository.ErrNotFound
}

type caPackages struct {
	repository.PackageRepository
	byID map[string]*models.HostingPackage
}

func (r *caPackages) FindByID(_ context.Context, id string) (*models.HostingPackage, error) {
	if p, ok := r.byID[id]; ok {
		return p, nil
	}
	return nil, repository.ErrNotFound
}

type caDatabases struct {
	repository.DatabaseRepository
	row         *models.Database
	count       int64
	existsNames map[string]bool // "<userID>|<name>" → taken
	newName     string
	newOwner    string
}

func (r *caDatabases) FindByID(_ context.Context, id string) (*models.Database, error) {
	if r.row == nil || r.row.ID != id {
		return nil, repository.ErrNotFound
	}
	return r.row, nil
}
func (r *caDatabases) CountByUserID(_ context.Context, _ string) (int64, error) { return r.count, nil }
func (r *caDatabases) ExistsByUserAndName(_ context.Context, userID, name string) (bool, error) {
	return r.existsNames[userID+"|"+name], nil
}
func (r *caDatabases) UpdateName(_ context.Context, _, name string) error {
	r.newName = name
	return nil
}
func (r *caDatabases) TransferOwner(_ context.Context, _, uid, name string) error {
	r.newName = name
	r.newOwner = uid
	return nil
}

type caDBUsers struct {
	repository.DatabaseUserRepository
	byID        map[string]*models.DatabaseUser
	existsUsers map[string]bool // "<userID>|<username>" → taken
	count       int64           // CountByUserID → for the DB-user cap clamp
	renamed     map[string]string
	reowned     map[string]string
}

func (r *caDBUsers) FindByID(_ context.Context, id string) (*models.DatabaseUser, error) {
	if u, ok := r.byID[id]; ok {
		return u, nil
	}
	return nil, repository.ErrNotFound
}
func (r *caDBUsers) ExistsByUserAndUsername(_ context.Context, userID, username string) (bool, error) {
	return r.existsUsers[userID+"|"+username], nil
}
func (r *caDBUsers) CountByUserID(_ context.Context, _ string) (int64, error) { return r.count, nil }
func (r *caDBUsers) UpdateUsername(_ context.Context, id, name string) error {
	if r.renamed == nil {
		r.renamed = map[string]string{}
	}
	r.renamed[id] = name
	return nil
}
func (r *caDBUsers) TransferOwner(_ context.Context, id, uid, name string) error {
	if r.renamed == nil {
		r.renamed = map[string]string{}
	}
	if r.reowned == nil {
		r.reowned = map[string]string{}
	}
	r.renamed[id] = name
	r.reowned[id] = uid
	return nil
}

type caGrants struct {
	repository.DatabaseUserGrantRepository
	byDB   map[string][]models.DatabaseUserGrant
	byUser map[string][]models.DatabaseUserGrant
}

func (r *caGrants) ListByDatabaseID(_ context.Context, dbID string) ([]models.DatabaseUserGrant, error) {
	return r.byDB[dbID], nil
}
func (r *caGrants) ListByDatabaseUserID(_ context.Context, duID string) ([]models.DatabaseUserGrant, error) {
	return r.byUser[duID], nil
}

func uidPtr(v uint32) *uint32 { return &v }
func strPtr(s string) *string { return &s }

// caFixture builds a stock scenario: database alice_blog (owner alice) with one
// DB user alice_web granted rw, reassigning to bob.
type caFixture struct {
	ag     *recAgent
	dbs    *caDatabases
	dus    *caDBUsers
	grants *caGrants
	users  *caUsers
	pkgs   *caPackages
	deps   Deps
}

func newCAFixture() *caFixture {
	alice := &models.User{ID: "alice", Username: strPtr("alice"), LinuxUID: uidPtr(1001)}
	bob := &models.User{ID: "bob", Username: strPtr("bob"), LinuxUID: uidPtr(1002)}
	db := &models.Database{ID: "db1", UserID: "alice", Name: "alice_blog", Engine: "mariadb"}
	du := &models.DatabaseUser{ID: "du1", UserID: "alice", Username: "alice_web", Engine: "mariadb"}
	grant := models.DatabaseUserGrant{ID: "g1", DatabaseID: "db1", DatabaseUserID: "du1", GrantLevel: "rw", Privileges: "ALL"}

	f := &caFixture{
		ag:     &recAgent{},
		dbs:    &caDatabases{row: db, existsNames: map[string]bool{}},
		dus:    &caDBUsers{byID: map[string]*models.DatabaseUser{"du1": du}, existsUsers: map[string]bool{}},
		grants: &caGrants{byDB: map[string][]models.DatabaseUserGrant{"db1": {grant}}, byUser: map[string][]models.DatabaseUserGrant{"du1": {grant}}},
		users:  &caUsers{byID: map[string]*models.User{"alice": alice, "bob": bob}},
		pkgs:   &caPackages{byID: map[string]*models.HostingPackage{}},
	}
	f.deps = Deps{
		Users:          f.users,
		Packages:       f.pkgs,
		Databases:      f.dbs,
		DatabaseUsers:  f.dus,
		DatabaseGrants: f.grants,
		Installs:       &recInstalls{},
		Agent:          f.ag,
	}
	return f
}

func TestReassign_HappyPath_RenamesAndReowns(t *testing.T) {
	f := newCAFixture()
	res, err := ReassignDatabaseOwner(context.Background(), f.deps, ReassignInput{DatabaseID: "db1", NewOwnerID: "bob"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.NewName != "bob_blog" {
		t.Errorf("new db name = %q, want bob_blog", res.NewName)
	}
	if f.dbs.newName != "bob_blog" || f.dbs.newOwner != "bob" {
		t.Errorf("db row not repointed: name=%q owner=%q", f.dbs.newName, f.dbs.newOwner)
	}
	if f.dus.renamed["du1"] != "bob_web" || f.dus.reowned["du1"] != "bob" {
		t.Errorf("db-user row not repointed: name=%q owner=%q", f.dus.renamed["du1"], f.dus.reowned["du1"])
	}
	// Transcript: engine rename first (db then user), then re-grant + revoke stale.
	want := []string{"db.rename_db", "db.rename_user", "db_user.grant", "db_user.revoke"}
	if strings.Join(f.ag.calls, ",") != strings.Join(want, ",") {
		t.Errorf("agent transcript = %v, want %v", f.ag.calls, want)
	}
}

func TestReassign_Postgres_Refused(t *testing.T) {
	f := newCAFixture()
	f.dbs.row.Engine = "postgres"
	_, err := ReassignDatabaseOwner(context.Background(), f.deps, ReassignInput{DatabaseID: "db1", NewOwnerID: "bob"})
	if err == nil || !contains(err, ErrReassignEngine) {
		t.Fatalf("want ErrReassignEngine, got %v", err)
	}
	if len(f.ag.calls) != 0 {
		t.Errorf("expected zero mutation, got %v", f.ag.calls)
	}
}

func TestReassign_SameOwner_Refused(t *testing.T) {
	f := newCAFixture()
	_, err := ReassignDatabaseOwner(context.Background(), f.deps, ReassignInput{DatabaseID: "db1", NewOwnerID: "alice"})
	if err == nil || !contains(err, ErrReassignSameOwner) {
		t.Fatalf("want ErrReassignSameOwner, got %v", err)
	}
}

func TestReassign_AppInstall_Refused_ZeroMutation(t *testing.T) {
	f := newCAFixture()
	f.deps.Installs = &recInstalls{install: &models.ApplicationInstall{ID: "inst1"}}
	_, err := ReassignDatabaseOwner(context.Background(), f.deps, ReassignInput{DatabaseID: "db1", NewOwnerID: "bob"})
	if err == nil || !contains(err, ErrAttached) {
		t.Fatalf("want ErrAttached, got %v", err)
	}
	if len(f.ag.calls) != 0 {
		t.Errorf("expected zero mutation on refusal, got %v", f.ag.calls)
	}
}

func TestReassign_SharedDBUser_Refused(t *testing.T) {
	f := newCAFixture()
	// The DB user also has a grant on another database → shared → refuse.
	f.grants.byUser["du1"] = append(f.grants.byUser["du1"],
		models.DatabaseUserGrant{ID: "g2", DatabaseID: "other", DatabaseUserID: "du1", Privileges: "ALL"})
	_, err := ReassignDatabaseOwner(context.Background(), f.deps, ReassignInput{DatabaseID: "db1", NewOwnerID: "bob"})
	if err == nil || !contains(err, ErrReassignSharedUser) {
		t.Fatalf("want ErrReassignSharedUser, got %v", err)
	}
	if len(f.ag.calls) != 0 {
		t.Errorf("expected zero mutation on refusal, got %v", f.ag.calls)
	}
}

func TestReassign_QuotaClamp_Refused(t *testing.T) {
	f := newCAFixture()
	bob := f.users.byID["bob"]
	bob.PackageID = strPtr("pkg1")
	f.pkgs.byID["pkg1"] = &models.HostingPackage{MaxDatabases: 2}
	f.dbs.count = 2 // bob already at cap
	_, err := ReassignDatabaseOwner(context.Background(), f.deps, ReassignInput{DatabaseID: "db1", NewOwnerID: "bob"})
	if err == nil || !contains(err, ErrQuotaExceeded) {
		t.Fatalf("want ErrQuotaExceeded, got %v", err)
	}
}

func TestReassign_DBUserQuotaClamp_Refused(t *testing.T) {
	f := newCAFixture()
	bob := f.users.byID["bob"]
	bob.PackageID = strPtr("pkg1")
	// DB cap has room; DB-USER cap is the binding one: bob at 2/2, move brings 1 more.
	f.pkgs.byID["pkg1"] = &models.HostingPackage{MaxDatabases: 10, MaxDatabaseUsers: 2}
	f.dus.count = 2
	_, err := ReassignDatabaseOwner(context.Background(), f.deps, ReassignInput{DatabaseID: "db1", NewOwnerID: "bob"})
	if err == nil || !contains(err, ErrQuotaExceeded) {
		t.Fatalf("want ErrQuotaExceeded (db-user cap), got %v", err)
	}
	if len(f.ag.calls) != 0 {
		t.Errorf("expected zero mutation on refusal, got %v", f.ag.calls)
	}
}

func TestReassign_Collision_Refused(t *testing.T) {
	f := newCAFixture()
	f.dbs.existsNames["bob|bob_blog"] = true // target name already owned by bob
	_, err := ReassignDatabaseOwner(context.Background(), f.deps, ReassignInput{DatabaseID: "db1", NewOwnerID: "bob"})
	if err == nil || !contains(err, ErrReassignCollision) {
		t.Fatalf("want ErrReassignCollision, got %v", err)
	}
	if len(f.ag.calls) != 0 {
		t.Errorf("expected zero mutation on refusal, got %v", f.ag.calls)
	}
}

// contains is errors.Is with a friendlier name for the tables above.
func contains(err, target error) bool { return errors.Is(err, target) }
