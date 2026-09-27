package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type restoreCall struct {
	verb   string
	params map[string]any
	staged []byte // the staged dump's content when the agent was called
}

type fakeRestoreAgent struct {
	calls []restoreCall
	err   error
}

func (f *fakeRestoreAgent) Call(_ context.Context, verb string, params any) (json.RawMessage, error) {
	p := params.(map[string]any)
	b, _ := os.ReadFile(p["path"].(string))
	f.calls = append(f.calls, restoreCall{verb: verb, params: p, staged: b})
	return json.RawMessage(`{"ok":true}`), f.err
}

type fakeGrantRepo struct {
	repository.DatabaseUserGrantRepository
	grants []models.DatabaseUserGrant
}

func (f fakeGrantRepo) ListByDatabaseID(context.Context, string) ([]models.DatabaseUserGrant, error) {
	return f.grants, nil
}

type fakeDBUserRepo struct {
	repository.DatabaseUserRepository
	users map[string]models.DatabaseUser
}

func (f fakeDBUserRepo) FindByID(_ context.Context, id string) (*models.DatabaseUser, error) {
	u, ok := f.users[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return &u, nil
}

func restoreFixture(t *testing.T) (dump, root string) {
	t.Helper()
	dir := t.TempDir()
	dump = filepath.Join(dir, "dump.sql")
	if err := os.WriteFile(dump, []byte("-- dump\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dump, filepath.Join(dir, "restore")
}

// A Postgres database must go to db.postgres.restore with its granted roles,
// as the REST restore sends it. The CLI used to send db.restore (MariaDB).
func TestRestoreDatabaseCLI_PostgresUsesThePostgresVerb(t *testing.T) {
	dump, root := restoreFixture(t)
	ag := &fakeRestoreAgent{}
	grants := fakeGrantRepo{grants: []models.DatabaseUserGrant{{DatabaseUserID: "du1"}}}
	users := fakeDBUserRepo{users: map[string]models.DatabaseUser{"du1": {ID: "du1", Engine: "postgres", Username: "alice_app"}}}

	err := restoreDatabaseCLI(context.Background(), ag, grants, users,
		&models.Database{ID: "db1", Name: "alice_shop", Engine: "postgres"}, dump, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(ag.calls) != 1 || ag.calls[0].verb != "db.postgres.restore" {
		t.Fatalf("calls = %+v, want one db.postgres.restore", ag.calls)
	}
	p := ag.calls[0].params
	if p["owner_role"] != "alice_app" {
		t.Errorf("owner_role = %v, want alice_app", p["owner_role"])
	}
	if roles, _ := p["grant_roles"].([]string); len(roles) != 1 || roles[0] != "alice_app" {
		t.Errorf("grant_roles = %v, want [alice_app]", p["grant_roles"])
	}
}

// The agent opens a dump only under its staging roots and deletes it after
// the load, so the CLI stages a copy there. The operator's file is never
// what the agent is given, and it survives.
func TestRestoreDatabaseCLI_StagesACopyInTheRestoreRoot(t *testing.T) {
	dump, root := restoreFixture(t)
	ag := &fakeRestoreAgent{}

	err := restoreDatabaseCLI(context.Background(), ag, nil, nil,
		&models.Database{ID: "db1", Name: "alice_shop", Engine: "mariadb"}, dump, root)
	if err != nil {
		t.Fatal(err)
	}
	c := ag.calls[0]
	if c.verb != "db.restore" {
		t.Errorf("verb = %s, want db.restore for mariadb", c.verb)
	}
	path := c.params["path"].(string)
	if path == dump || filepath.Dir(path) != root || !strings.HasSuffix(path, ".sql") {
		t.Errorf("path = %s, want a new .sql file in %s", path, root)
	}
	if string(c.staged) != "-- dump\n" {
		t.Errorf("staged content = %q, want the dump", c.staged)
	}
	if _, ok := c.params["owner_role"]; ok {
		t.Error("mariadb restore must not carry Postgres roles")
	}
	if _, err := os.Stat(dump); err != nil {
		t.Errorf("operator's dump is gone: %v", err)
	}
}

// When the agent fails, the staged copy is removed (the agent only deletes
// it on success) and the error names the verb.
func TestRestoreDatabaseCLI_AgentFailureRemovesTheCopy(t *testing.T) {
	dump, root := restoreFixture(t)
	ag := &fakeRestoreAgent{err: errors.New("restore path invalid")}

	err := restoreDatabaseCLI(context.Background(), ag, nil, nil,
		&models.Database{ID: "db1", Name: "alice_shop", Engine: "postgres"}, dump, root)
	if err == nil || !strings.Contains(err.Error(), "db.postgres.restore") {
		t.Fatalf("err = %v, want it to name db.postgres.restore", err)
	}
	left, _ := os.ReadDir(root)
	if len(left) != 0 {
		t.Errorf("staging dir still holds %d file(s) after a failed restore", len(left))
	}
}

// The CLI takes its backup and restore verbs from dbops, as the REST handler
// does. A literal MariaDB verb here is how Postgres databases were sent to
// mysqldump / mysql.
func TestDBOpsCmd_NoHardcodedBackupOrRestoreVerb(t *testing.T) {
	src, err := os.ReadFile("db_ops_cmd.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, lit := range []string{`"db.backup"`, `"db.restore"`, `"db.postgres.backup"`, `"db.postgres.restore"`} {
		if strings.Contains(string(src), lit) {
			t.Errorf("db_ops_cmd.go hardcodes %s; use dbops.BackupDatabaseCommand / RestoreDatabaseCommand", lit)
		}
	}
}
