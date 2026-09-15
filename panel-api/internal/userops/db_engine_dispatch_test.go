package userops

// The delete cascade sends one agent command per database and per database
// login. Both the command name and the payload key differ by engine, and
// getting either wrong is silent: `db.drop` / `db_user.drop` reach MariaDB,
// whose DROP ... IF EXISTS succeeds on a name that was never there. The
// cascade's failure guard therefore never fires, the user row is deleted,
// its metadata rows CASCADE away — and the real Postgres database or role
// survives on the host with nothing left to name it (GH #1013).
//
// The database drop and the database-LOGIN drop are now the shared
// dbops.DropDatabaseHost / dbops.DropDatabaseUserHost lifecycle operations
// (JAB-275 AC6), so both are pinned behaviorally — the cascade must send the
// engine-correct command through those operations — plus a source pin that the
// per-engine dispatch helpers were removed from userops and not re-inlined.
// (The per-user shadow-admin drops keep their fixed-engine db_user.drop /
// db.postgres.drop_role literals, so the pin checks the helpers' absence, not
// those strings.)

import (
	"context"
	"os"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// cascadeDBUsers is a one-shot DatabaseUserRepository that hands the cascade a
// fixed set of login rows to reap; only ListByUserID is exercised.
type cascadeDBUsers struct {
	repository.DatabaseUserRepository
	rows []models.DatabaseUser
}

func (r *cascadeDBUsers) ListByUserID(context.Context, string, repository.ListOptions) ([]models.DatabaseUser, int64, error) {
	return r.rows, int64(len(r.rows)), nil
}

// GH #1013, behavioral: deleting a tenant whose account still owns a Postgres
// ROLE must send db.postgres.drop_role with the "role" key, not the MariaDB
// db_user.drop{db_user_name} that would no-op and leave the live role on the
// host. This is the login half of JAB-275 AC6 — it must hold through the shared
// dbops.DropDatabaseUserHost, so flipping either the command or the payload key
// in dbops turns this red. (The command name AND the key differ by engine.)
func TestDeleteCascade_DropsUserLoginWithEngineCommand(t *testing.T) {
	cases := []struct {
		engine  string
		wantCmd string
		wantKey string
	}{
		{"postgres", "db.postgres.drop_role", "role"},
		{"mariadb", "db_user.drop", "db_user_name"},
	}
	for _, tc := range cases {
		ag := &recCascadeAgent{}
		users := &fakeCascadeUsers{}
		dbus := &cascadeDBUsers{rows: []models.DatabaseUser{{Username: "alice_app", Engine: tc.engine}}}
		target := &models.User{ID: "u1", Username: strptr("alice")}

		if err := DeleteCascade(context.Background(), Deps{Users: users, Agent: ag}, DeleteDeps{DatabaseUsers: dbus}, target, "test"); err != nil {
			t.Fatalf("engine %q: cascade: %v", tc.engine, err)
		}
		if !cascadeCalledWith(ag, tc.wantCmd, tc.wantKey, "alice_app") {
			t.Fatalf("engine %q: want %s{%s:alice_app}; calls=%v", tc.engine, tc.wantCmd, tc.wantKey, ag.callsSnapshot())
		}
		if users.deleted != "u1" {
			t.Errorf("engine %q: user row should be deleted after a clean cascade, got %q", tc.engine, users.deleted)
		}
	}
}

// cascadeDBs is a one-shot DatabaseRepository that hands the cascade a fixed
// set of rows to reap; only ListByUserID is exercised.
type cascadeDBs struct {
	repository.DatabaseRepository
	rows []models.Database
}

func (r *cascadeDBs) ListByUserID(context.Context, string, repository.ListOptions) ([]models.Database, int64, error) {
	return r.rows, int64(len(r.rows)), nil
}

// GH #1013, behavioral: deleting a tenant whose account still owns a Postgres
// database must send db.postgres.drop_db, not the MariaDB db.drop that would
// no-op and orphan the real database. This is the account-teardown half of
// JAB-275 AC6 — it must hold through dbops.DropDatabaseHost, so flipping the
// engine dispatch in dbops turns this red.
func TestDeleteCascade_DropsUserDatabaseWithEngineCommand(t *testing.T) {
	cases := []struct {
		engine  string
		wantCmd string
	}{
		{"postgres", "db.postgres.drop_db"},
		{"mariadb", "db.drop"},
	}
	for _, tc := range cases {
		ag := &recCascadeAgent{}
		users := &fakeCascadeUsers{}
		dbs := &cascadeDBs{rows: []models.Database{{Name: "alice_blog", Engine: tc.engine}}}
		target := &models.User{ID: "u1", Username: strptr("alice")}

		if err := DeleteCascade(context.Background(), Deps{Users: users, Agent: ag}, DeleteDeps{Databases: dbs}, target, "test"); err != nil {
			t.Fatalf("engine %q: cascade: %v", tc.engine, err)
		}
		if !cascadeCalledWith(ag, tc.wantCmd, "db_name", "alice_blog") {
			t.Fatalf("engine %q: want %s{db_name:alice_blog}; calls=%v", tc.engine, tc.wantCmd, ag.callsSnapshot())
		}
		if users.deleted != "u1" {
			t.Errorf("engine %q: user row should be deleted after a clean cascade, got %q", tc.engine, users.deleted)
		}
	}
}

// Source pin: both the database drop and the login drop dispatch must stay in
// dbops, never re-inlined here. The database verbs must not appear as literals;
// the login dispatch is pinned by the ABSENCE of the old per-engine helpers,
// because the fixed-engine shadow-admin drops legitimately keep the
// "db_user.drop" / "db.postgres.drop_role" strings.
func TestCascade_NoInlinedDatabaseDropLiteral(t *testing.T) {
	src, err := os.ReadFile("userops_lifecycle.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	for _, lit := range []string{`"db.drop"`, `"db.postgres.drop_db"`} {
		if strings.Contains(string(src), lit) {
			t.Errorf("userops_lifecycle.go inlines %s — route the drop through dbops.DropDatabaseHost instead (JAB-275 AC6)", lit)
		}
	}
	for _, fn := range []string{`func dbUserDropCmd(`, `func dbUserDropParams(`} {
		if strings.Contains(string(src), fn) {
			t.Errorf("userops_lifecycle.go still defines %s — the login dispatch moved to dbops.DropDatabaseUserHost (JAB-275 AC6); route through it instead of re-adding the helper", fn)
		}
	}
}
