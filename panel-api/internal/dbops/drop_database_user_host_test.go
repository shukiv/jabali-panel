package dbops

import (
	"context"
	"errors"
	"testing"
)

// The command name is half the GH #1013 fix for logins: db_user.drop reaches
// MariaDB, whose DROP USER IF EXISTS succeeds on a name that was never there, so
// a Postgres role sent db_user.drop is left live on the host.
func TestDropDatabaseUserCommand_DispatchesOnEngine(t *testing.T) {
	if got := DropDatabaseUserCommand("postgres"); got != "db.postgres.drop_role" {
		t.Errorf("postgres login drop = %q, want db.postgres.drop_role", got)
	}
	for _, engine := range []string{"mariadb", "", "mysql"} {
		if got := DropDatabaseUserCommand(engine); got != "db_user.drop" {
			t.Errorf("engine %q login drop = %q, want db_user.drop", engine, got)
		}
	}
}

// The other half of the fix is the payload KEY: db.postgres.drop_role reads
// "role", db_user.drop reads "db_user_name". Sending the MariaDB shape to
// Postgres leaves the role name empty — a silent no-op even when the command
// name is right — so the op must carry the engine-correct key.
func TestDropDatabaseUserHost_SendsEngineCommandWithPayloadKey(t *testing.T) {
	cases := []struct {
		engine    string
		wantCmd   string
		wantKey   string
		absentKey string
	}{
		{"postgres", "db.postgres.drop_role", "role", "db_user_name"},
		{"mariadb", "db_user.drop", "db_user_name", "role"},
		{"", "db_user.drop", "db_user_name", "role"},
	}
	for _, tc := range cases {
		ag := &recDropAgent{}
		if err := DropDatabaseUserHost(context.Background(), ag, tc.engine, "alice_role"); err != nil {
			t.Fatalf("engine %q: unexpected error %v", tc.engine, err)
		}
		if ag.method != tc.wantCmd {
			t.Errorf("engine %q: command = %q, want %q", tc.engine, ag.method, tc.wantCmd)
		}
		if ag.params[tc.wantKey] != "alice_role" {
			t.Errorf("engine %q: params = %#v, want %s=alice_role", tc.engine, ag.params, tc.wantKey)
		}
		if _, ok := ag.params[tc.absentKey]; ok {
			t.Errorf("engine %q: params = %#v must not carry %s", tc.engine, ag.params, tc.absentKey)
		}
	}
}

// An Agent failure must surface as ErrAgentFailed so best-effort teardown
// callers can tell a failed drop from a clean one and keep the panel row.
func TestDropDatabaseUserHost_WrapsAgentFailureAsErrAgentFailed(t *testing.T) {
	ag := &recDropAgent{fail: true}
	err := DropDatabaseUserHost(context.Background(), ag, "postgres", "alice_role")
	if !errors.Is(err, ErrAgentFailed) {
		t.Fatalf("got %v, want errors.Is ErrAgentFailed", err)
	}
}
