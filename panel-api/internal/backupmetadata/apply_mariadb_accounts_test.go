package backupmetadata

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1993: a restore rebuilt the database-user and grant ROWS but never the
// MariaDB accounts behind them, so after restoring an account on another
// server every site's database login failed. Apply now has the agent create
// the account (with the password hash the backup carried) and its grants, for
// the rows it created.

const maHash = "*2470C0C06DEE42FD1618BB99005ADCA2EC9D1E19"

type maDBs struct {
	repository.DatabaseRepository
	rows map[string]*models.Database
}

func (r *maDBs) List(context.Context, repository.ListOptions) ([]models.Database, int64, error) {
	var out []models.Database
	for _, d := range r.rows {
		out = append(out, *d)
	}
	return out, int64(len(out)), nil
}
func (r *maDBs) Create(_ context.Context, d *models.Database) error {
	if _, ok := r.rows[d.ID]; ok {
		return repository.ErrConflict
	}
	c := *d
	r.rows[d.ID] = &c
	return nil
}
func (r *maDBs) FindByID(_ context.Context, id string) (*models.Database, error) {
	if d, ok := r.rows[id]; ok {
		c := *d
		return &c, nil
	}
	return nil, repository.ErrNotFound
}

type maDBUsers struct {
	repository.DatabaseUserRepository
	rows map[string]*models.DatabaseUser
}

func (r *maDBUsers) List(context.Context, repository.ListOptions) ([]models.DatabaseUser, int64, error) {
	var out []models.DatabaseUser
	for _, u := range r.rows {
		out = append(out, *u)
	}
	return out, int64(len(out)), nil
}
func (r *maDBUsers) Create(_ context.Context, u *models.DatabaseUser) error {
	for _, e := range r.rows {
		if e.ID == u.ID || (e.UserID == u.UserID && e.Username == u.Username) {
			return repository.ErrConflict
		}
	}
	c := *u
	r.rows[u.ID] = &c
	return nil
}
func (r *maDBUsers) FindByID(_ context.Context, id string) (*models.DatabaseUser, error) {
	if u, ok := r.rows[id]; ok {
		c := *u
		return &c, nil
	}
	return nil, repository.ErrNotFound
}
func (r *maDBUsers) Delete(_ context.Context, id string) error {
	delete(r.rows, id)
	return nil
}

type maGrants struct {
	repository.DatabaseUserGrantRepository
	rows map[string]*models.DatabaseUserGrant
}

func (r *maGrants) Create(_ context.Context, g *models.DatabaseUserGrant) error {
	if _, ok := r.rows[g.ID]; ok {
		return repository.ErrConflict
	}
	c := *g
	r.rows[g.ID] = &c
	return nil
}
func (r *maGrants) Delete(_ context.Context, id string) error {
	delete(r.rows, id)
	return nil
}

type maCall struct {
	cmd    string
	params map[string]any
}

// maAgent records every call and fails the commands named in fail (with
// failErr when set). old is an agent without db_user.create's create_only.
type maAgent struct {
	calls   []maCall
	fail    map[string]bool
	failErr error
	old     bool
}

func (a *maAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	if cmd == "agent.version" {
		if a.old {
			return json.RawMessage(`{"version":"x","capabilities":["restore_upload_confinement"]}`), nil
		}
		return json.RawMessage(`{"version":"x","capabilities":["restore_upload_confinement","db_user_create_only"]}`), nil
	}
	raw, _ := json.Marshal(params)
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	a.calls = append(a.calls, maCall{cmd, p})
	if a.fail[cmd] {
		if a.failErr != nil {
			return nil, a.failErr
		}
		return nil, errors.New("agent: " + cmd + " failed")
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func (a *maAgent) dbUserCalls() []maCall {
	var out []maCall
	for _, c := range a.calls {
		if strings.HasPrefix(c.cmd, "db_user.") {
			out = append(out, c)
		}
	}
	return out
}

type maFixture struct {
	dbs    *maDBs
	users  *maDBUsers
	grants *maGrants
	agent  *maAgent
}

func newMAFixture() *maFixture {
	return &maFixture{
		dbs:    &maDBs{rows: map[string]*models.Database{}},
		users:  &maDBUsers{rows: map[string]*models.DatabaseUser{}},
		grants: &maGrants{rows: map[string]*models.DatabaseUserGrant{}},
		agent:  &maAgent{fail: map[string]bool{}},
	}
}

func (f *maFixture) apply(meta *internalbackup.AccountMetadata) ApplyResult {
	return Apply(context.Background(), meta, Deps{
		Users: namedUsersRepo{username: "alice"}, Databases: f.dbs, DatabaseUsers: f.users, DatabaseGrants: f.grants, Agent: f.agent,
	})
}

// maMeta is alice's bundle: database alice_wp and database user alice_u
// (with hash) granted ALL on it.
func maMeta(hash string) *internalbackup.AccountMetadata {
	alice := "alice"
	return &internalbackup.AccountMetadata{
		User:      internalbackup.MetadataUser{ID: "u1", Username: &alice},
		Databases: []internalbackup.MetadataDatabase{{ID: "db1", Name: "alice_wp", Engine: "mariadb"}},
		DatabaseUsers: []internalbackup.MetadataDatabaseUser{{
			ID: "du1", Username: "alice_u", Engine: "mariadb", NativePasswordHash: hash,
			Grants: []internalbackup.MetadataDatabaseUserGrant{{ID: "g1", DatabaseID: "db1", DatabaseName: "alice_wp", GrantLevel: "rw", Privileges: "ALL"}},
		}},
	}
}

func TestApply_RecreatesARestoredDatabaseUsersMariaDBAccountAndGrants(t *testing.T) {
	f := newMAFixture()
	r := f.apply(maMeta(maHash))

	calls := f.agent.dbUserCalls()
	if len(calls) != 2 || calls[0].cmd != "db_user.create" || calls[1].cmd != "db_user.grant" {
		t.Fatalf("agent calls %+v (errors %v), want db_user.create then db_user.grant", calls, r.Errors)
	}
	if calls[0].params["db_user_name"] != "alice_u" || calls[0].params["password_hash"] != maHash || calls[0].params["password"] != nil ||
		calls[0].params["create_only"] != true {
		t.Fatalf("db_user.create %v: want alice_u with the backup's password hash, create_only", calls[0].params)
	}
	g := calls[1].params
	if g["db_name"] != "alice_wp" || g["db_user_name"] != "alice_u" || !sameAny(g["privileges"], "ALL") {
		t.Fatalf("db_user.grant %v: want ALL on alice_wp to alice_u", g)
	}
	if len(r.Errors) != 0 || f.users.rows["du1"] == nil || f.grants.rows["g1"] == nil {
		t.Fatalf("errors %v, rows users=%v grants=%v", r.Errors, f.users.rows, f.grants.rows)
	}
	if f.users.rows["du1"].Engine != "mariadb" {
		t.Fatalf("restored user engine %q", f.users.rows["du1"].Engine)
	}
}

// A database user the account already had keeps its password: the restore
// adds only the grants it restored.
func TestApply_LeavesAnExistingDatabaseUsersPasswordAlone(t *testing.T) {
	f := newMAFixture()
	f.users.rows["du1"] = &models.DatabaseUser{ID: "du1", UserID: "u1", Username: "alice_u", Engine: "mariadb"}
	r := f.apply(maMeta(maHash))

	calls := f.agent.dbUserCalls()
	if len(calls) != 1 || calls[0].cmd != "db_user.grant" {
		t.Fatalf("agent calls %+v (errors %v), want only db_user.grant", calls, r.Errors)
	}
}

// A backup made before the agent captured the password hash: the account is
// still recreated (with a new password) so the site can be pointed at it, and
// the report says so.
func TestApply_DatabaseUserFromAnOlderBackupGetsANewPassword(t *testing.T) {
	f := newMAFixture()
	r := f.apply(maMeta(""))

	calls := f.agent.dbUserCalls()
	if len(calls) != 2 || calls[0].cmd != "db_user.create" {
		t.Fatalf("agent calls %+v", calls)
	}
	pw, _ := calls[0].params["password"].(string)
	if len(pw) < 20 || calls[0].params["password_hash"] != nil {
		t.Fatalf("db_user.create %v: want a generated password", calls[0].params)
	}
	if !hasError(r.Errors, "db_user du1 (alice_u): restored with a new password") {
		t.Fatalf("errors %v should say the password is new", r.Errors)
	}
}

// A bundle's hash that isn't a mysql_native_password hash is never sent.
func TestApply_MalformedPasswordHashIsNotUsed(t *testing.T) {
	f := newMAFixture()
	f.apply(maMeta("*2470C0C06DEE42FD1618BB99005ADCA2EC9D1E19' OR '1'='1"))

	calls := f.agent.dbUserCalls()
	if len(calls) == 0 || calls[0].params["password_hash"] != nil {
		t.Fatalf("agent calls %+v: a malformed hash must not reach db_user.create", calls)
	}
}

// When the MariaDB account can't be created, the panel doesn't keep a
// database user or grant row that wouldn't work.
func TestApply_DatabaseUserWhoseAccountFailsIsTakenBackOut(t *testing.T) {
	f := newMAFixture()
	f.agent.fail["db_user.create"] = true
	r := f.apply(maMeta(maHash))

	if f.users.rows["du1"] != nil || f.grants.rows["g1"] != nil {
		t.Fatalf("rows left: users=%v grants=%v", f.users.rows, f.grants.rows)
	}
	for _, c := range f.agent.dbUserCalls() {
		if c.cmd == "db_user.grant" {
			t.Fatal("granted to a user whose account failed")
		}
	}
	if r.DatabaseUsers != 0 || r.DatabaseGrants != 0 || len(r.Errors) != 1 || !hasError(r.Errors, "db_user du1 (alice_u): not restored: creating its MariaDB account failed") {
		t.Fatalf("counts users=%d grants=%d errors %v", r.DatabaseUsers, r.DatabaseGrants, r.Errors)
	}
}

func TestApply_GrantThatFailsIsTakenBackOut(t *testing.T) {
	f := newMAFixture()
	f.agent.fail["db_user.grant"] = true
	r := f.apply(maMeta(maHash))

	if f.grants.rows["g1"] != nil || f.users.rows["du1"] == nil {
		t.Fatalf("rows: users=%v grants=%v", f.users.rows, f.grants.rows)
	}
	if r.DatabaseGrants != 0 || !hasError(r.Errors, "db_grant g1: not restored: granting it in MariaDB failed") {
		t.Fatalf("grants=%d errors %v", r.DatabaseGrants, r.Errors)
	}
}

// A PostgreSQL database user is never created in MariaDB — also from an older
// bundle that didn't record the engine, where its grant on a PostgreSQL
// database gives it away.
func TestApply_PostgresDatabaseUserIsNotCreatedInMariaDB(t *testing.T) {
	for _, engine := range []string{"postgres", ""} {
		f := newMAFixture()
		meta := maMeta(maHash)
		meta.Databases[0].Engine = "postgres"
		meta.DatabaseUsers[0].Engine = engine
		f.apply(meta)

		if calls := f.agent.dbUserCalls(); len(calls) != 0 {
			t.Fatalf("engine %q: agent calls %+v", engine, calls)
		}
		if u := f.users.rows["du1"]; u == nil || u.Engine != "postgres" {
			t.Fatalf("engine %q: restored row %+v, want a postgres row", engine, u)
		}
	}
}

// The per-account phpMyAdmin account is the panel's own; no restore takes it
// from a bundle.
func TestApply_DoesNotRestoreTheAccountsPhpMyAdminAccount(t *testing.T) {
	f := newMAFixture()
	meta := maMeta(maHash)
	meta.DatabaseUsers[0].Username = "alice_mysqladmin"
	r := f.apply(meta)

	if len(f.users.rows) != 0 || len(f.agent.dbUserCalls()) != 0 {
		t.Fatalf("restored %v / calls %+v", f.users.rows, f.agent.dbUserCalls())
	}
	if !hasError(r.Errors, "db_user du1 (alice_mysqladmin): not restored: it is the account's phpMyAdmin account") {
		t.Fatalf("errors %v", r.Errors)
	}
}

func sameAny(v any, want ...string) bool {
	xs, _ := v.([]any)
	if len(xs) != len(want) {
		return false
	}
	for i, x := range xs {
		if s, _ := x.(string); s != want[i] {
			return false
		}
	}
	return true
}

// SECURITY: a MariaDB account of the restored user's name that already exists
// is not the restored row's — the row would let the account's owner reset its
// password. The row is taken back out.
func TestApply_DatabaseUserWhoseNameExistsInMariaDBIsTakenBackOut(t *testing.T) {
	f := newMAFixture()
	f.agent.fail["db_user.create"] = true
	f.agent.failErr = &agentwire.AgentError{Code: agentwire.CodeAlreadyExists, Message: "exists"}
	r := f.apply(maMeta(maHash))

	if f.users.rows["du1"] != nil || f.grants.rows["g1"] != nil {
		t.Fatalf("rows left: users=%v grants=%v", f.users.rows, f.grants.rows)
	}
	if !hasError(r.Errors, "db_user du1 (alice_u): not restored: a MariaDB account with this name already exists on this server, and it is not this account's") {
		t.Fatalf("errors %v", r.Errors)
	}
}

// SECURITY: an agent that can't promise to leave an existing account alone
// creates none.
func TestApply_AgentWithoutCreateOnlyCreatesNoMariaDBAccount(t *testing.T) {
	f := newMAFixture()
	f.agent.old = true
	r := f.apply(maMeta(maHash))

	if calls := f.agent.dbUserCalls(); len(calls) != 0 {
		t.Fatalf("agent calls %+v", calls)
	}
	if f.users.rows["du1"] != nil || f.grants.rows["g1"] != nil {
		t.Fatalf("rows left: users=%v grants=%v", f.users.rows, f.grants.rows)
	}
	if !hasError(r.Errors, "db_user du1 (alice_u): not restored: this server's agent is too old") {
		t.Fatalf("errors %v", r.Errors)
	}
}

// SECURITY: a restore from this server's own backup doesn't hand the account
// a database or database user another account now holds (an older backup can
// hold one the account has since handed over).
func TestApply_AnotherAccountsDatabaseOrUserNameIsRefused(t *testing.T) {
	f := newMAFixture()
	f.dbs.rows["xdb"] = &models.Database{ID: "xdb", UserID: "u-bob", Name: "alice_wp", Engine: "mariadb"}
	f.users.rows["xdu"] = &models.DatabaseUser{ID: "xdu", UserID: "u-bob", Username: "alice_u", Engine: "mariadb"}
	r := f.apply(maMeta(maHash))

	if f.dbs.rows["db1"] != nil || f.users.rows["du1"] != nil || len(f.agent.dbUserCalls()) != 0 {
		t.Fatalf("restored dbs=%v users=%v calls=%+v", f.dbs.rows, f.users.rows, f.agent.dbUserCalls())
	}
	if !hasError(r.Errors, "database db1 (alice_wp): not restored: another account has a database with this name") ||
		!hasError(r.Errors, "db_user du1 (alice_u): not restored: another account has a database user with this name") {
		t.Fatalf("errors %v", r.Errors)
	}
}

// SECURITY: from an uploaded file, a grant on one of the account's databases
// whose data the file didn't restore isn't made: the file's author would get a
// login to data they didn't supply.
func TestApply_UploadedBackupGrantsOnlyOnDatabasesItRestored(t *testing.T) {
	f := newMAFixture()
	f.dbs.rows["db1"] = &models.Database{ID: "db1", UserID: "u1", Name: "alice_wp", Engine: "mariadb"}
	meta := maMeta(maHash)
	r := Apply(context.Background(), meta, Deps{
		Users: namedUsersRepo{username: "alice"}, Databases: f.dbs, DatabaseUsers: f.users, DatabaseGrants: f.grants, Agent: f.agent,
		Untrusted: true, RestoredDatabases: map[string]bool{},
	})

	for _, c := range f.agent.dbUserCalls() {
		if c.cmd == "db_user.grant" {
			t.Fatalf("granted %v on a database the file didn't restore", c.params)
		}
	}
	if f.grants.rows["g1"] != nil || !hasError(r.Errors, "db_grant g1: not restored: the uploaded backup didn't restore alice_wp's data") {
		t.Fatalf("grant rows %v errors %v", f.grants.rows, r.Errors)
	}

	// Restored from the file, and all its data is the file's: granted.
	f = newMAFixture()
	f.dbs.rows["db1"] = &models.Database{ID: "db1", UserID: "u1", Name: "alice_wp", Engine: "mariadb"}
	Apply(context.Background(), maMeta(maHash), Deps{
		Users: namedUsersRepo{username: "alice"}, Databases: f.dbs, DatabaseUsers: f.users, DatabaseGrants: f.grants, Agent: f.agent,
		Untrusted: true, RestoredDatabases: map[string]bool{"alice_wp": true}, ArchiveMariaDBs: map[string]bool{"alice_wp": true},
	})
	if calls := f.agent.dbUserCalls(); len(calls) != 2 || calls[1].cmd != "db_user.grant" {
		t.Fatalf("agent calls %+v, want the grant on a restored database", calls)
	}
}

// SECURITY: a database the file was loaded over still holds the tables it had
// here, so the file can't grant access to it either. Nor can it when the
// agent doesn't say which databases hold only the file's data.
func TestApply_UploadedBackupGrantsNothingOnADatabaseThatAlreadyHadData(t *testing.T) {
	for name, c := range map[string]struct {
		archive map[string]bool
		want    string
	}{
		"loaded over its data": {map[string]bool{}, "db_grant g1: not restored: alice_wp holds data that isn't the uploaded backup's (it wasn't empty before the restore, or the backup's data didn't load), so the uploaded backup can't grant access to it"},
		"an older agent":       {nil, "db_grant g1: not restored: this server's agent is too old to tell whether alice_wp holds only the uploaded backup's data; run jabali update and restore again"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newMAFixture()
			f.dbs.rows["db1"] = &models.Database{ID: "db1", UserID: "u1", Name: "alice_wp", Engine: "mariadb"}
			r := Apply(context.Background(), maMeta(maHash), Deps{
				Users: namedUsersRepo{username: "alice"}, Databases: f.dbs, DatabaseUsers: f.users, DatabaseGrants: f.grants, Agent: f.agent,
				Untrusted: true, RestoredDatabases: map[string]bool{"alice_wp": true}, ArchiveMariaDBs: c.archive,
			})
			for _, call := range f.agent.dbUserCalls() {
				if call.cmd == "db_user.grant" {
					t.Fatalf("granted %v on a database that kept data the file didn't supply", call.params)
				}
			}
			if f.grants.rows["g1"] != nil || !hasError(r.Errors, c.want) {
				t.Fatalf("grant rows %v errors %v", f.grants.rows, r.Errors)
			}
		})
	}
}
