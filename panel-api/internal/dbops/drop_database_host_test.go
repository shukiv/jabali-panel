package dbops

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// recDropAgent records the single Agent call DropDatabaseHost makes and can be
// told to fail, so tests can assert both the dispatched command/payload and the
// error wrapping.
type recDropAgent struct {
	method string
	params map[string]any
	fail   bool
}

func (a *recDropAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	a.method = method
	if m, ok := params.(map[string]any); ok {
		a.params = m
	}
	if a.fail {
		return nil, errors.New("agent boom")
	}
	return json.RawMessage(`{}`), nil
}

// The command name is the whole GH #1013 fix: db.drop reaches MariaDB, whose
// DROP DATABASE IF EXISTS succeeds on a name that was never there, so a Postgres
// database sent db.drop is orphaned on the happy path.
func TestDropDatabaseCommand_DispatchesOnEngine(t *testing.T) {
	if got := DropDatabaseCommand("postgres"); got != "db.postgres.drop_db" {
		t.Errorf("postgres database drop = %q, want db.postgres.drop_db", got)
	}
	for _, engine := range []string{"mariadb", "", "mysql"} {
		if got := DropDatabaseCommand(engine); got != "db.drop" {
			t.Errorf("engine %q database drop = %q, want db.drop", engine, got)
		}
	}
}

func TestDropDatabaseHost_SendsEngineCommandWithDBName(t *testing.T) {
	cases := []struct {
		engine  string
		wantCmd string
	}{
		{"postgres", "db.postgres.drop_db"},
		{"mariadb", "db.drop"},
		{"", "db.drop"},
	}
	for _, tc := range cases {
		ag := &recDropAgent{}
		if err := DropDatabaseHost(context.Background(), ag, tc.engine, "alice_blog"); err != nil {
			t.Fatalf("engine %q: unexpected error %v", tc.engine, err)
		}
		if ag.method != tc.wantCmd {
			t.Errorf("engine %q: command = %q, want %q", tc.engine, ag.method, tc.wantCmd)
		}
		if ag.params["db_name"] != "alice_blog" {
			t.Errorf("engine %q: params = %#v, want db_name=alice_blog", tc.engine, ag.params)
		}
	}
}

// An Agent failure must surface as ErrAgentFailed so Delete's callers (and the
// existing errors.Is(err, ErrAgentFailed) mapping to HTTP 502) keep working.
func TestDropDatabaseHost_WrapsAgentFailureAsErrAgentFailed(t *testing.T) {
	ag := &recDropAgent{fail: true}
	err := DropDatabaseHost(context.Background(), ag, "postgres", "alice_blog")
	if !errors.Is(err, ErrAgentFailed) {
		t.Fatalf("got %v, want errors.Is ErrAgentFailed", err)
	}
}
