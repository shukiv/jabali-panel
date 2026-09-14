package userops

// The delete cascade sends one agent command per database and per database
// login. Both the command name and the payload key differ by engine, and
// getting either wrong is silent: `db.drop` / `db_user.drop` reach MariaDB,
// whose DROP ... IF EXISTS succeeds on a name that was never there. The
// cascade's failure guard therefore never fires, the user row is deleted,
// its metadata rows CASCADE away — and the real Postgres database or role
// survives on the host with nothing left to name it (GH #1013).
//
// The database drop is now the shared dbops.DropDatabaseHost lifecycle
// operation (JAB-275 AC6), so it is pinned behaviorally — the cascade must
// send the engine-correct command through that operation — plus a source pin
// that no engine-dispatch literal was re-inlined here. The database-LOGIN
// dispatch still lives in userops and is pinned directly.

import (
	"context"
	"os"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

func TestDBUserDropCmd_DispatchesOnEngine(t *testing.T) {
	if got := dbUserDropCmd("postgres"); got != "db.postgres.drop_role" {
		t.Errorf("postgres login drop = %q, want db.postgres.drop_role — db_user.drop reaches MariaDB and no-ops", got)
	}
	for _, engine := range []string{"mariadb", "", "mysql"} {
		if got := dbUserDropCmd(engine); got != "db_user.drop" {
			t.Errorf("engine %q login drop = %q, want db_user.drop", engine, got)
		}
	}
}

// A Postgres drop_role reads "role"; sending the MariaDB "db_user_name"
// shape leaves the role name empty, so the command is a no-op even when
// the command NAME is right.
func TestDBUserDropParams_KeyDiffersByEngine(t *testing.T) {
	pg := dbUserDropParams("postgres", "alice_app")
	if pg["role"] != "alice_app" {
		t.Errorf("postgres params = %#v, want role=alice_app", pg)
	}
	if _, ok := pg["db_user_name"]; ok {
		t.Error("postgres params must not carry db_user_name — drop_role ignores it")
	}

	my := dbUserDropParams("mariadb", "alice_app")
	if my["db_user_name"] != "alice_app" {
		t.Errorf("mariadb params = %#v, want db_user_name=alice_app", my)
	}
	if _, ok := my["role"]; ok {
		t.Error("mariadb params must not carry role")
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

// Source pin: the database-drop dispatch must stay in dbops, never re-inlined
// as a literal here. dbUserDropCmd's "db_user.drop" / "db.postgres.drop_role"
// are different strings and do not match these quoted literals.
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
}
