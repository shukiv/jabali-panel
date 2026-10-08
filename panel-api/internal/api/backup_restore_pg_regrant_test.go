package api

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1993: a restore loads each PostgreSQL database as a new database whose
// objects its holder role owns. Every restore door then grants the
// account's database users on it again, so the first takes the objects over.

// pgRegrantAgent answers the restore verb with reply and records each
// db.postgres.grant.
func pgRegrantAgent(verb, reply string, grants *[]string) *mockAgent {
	return &mockAgent{callFn: func(_ context.Context, cmd string, params any) (json.RawMessage, error) {
		switch cmd {
		case verb:
			return json.RawMessage(reply), nil
		case "db.postgres.grant":
			p := params.(map[string]any)
			*grants = append(*grants, fmt.Sprintf("%s %s", p["db_name"], p["role"]))
			return json.RawMessage(`{"ok":true}`), nil
		}
		return nil, fmt.Errorf("unexpected %s", cmd)
	}}
}

// pgRegrantRows is account U with PostgreSQL database alice_pg, granted to
// alice_u, and alice_bare, granted to no one.
func pgRegrantRows() (*daDBs, *daDBUsers, *daGrants) {
	return &daDBs{rows: []models.Database{
			{ID: "d1", UserID: "U", Name: "alice_pg", Engine: "postgres"},
			{ID: "d2", UserID: "U", Name: "alice_bare", Engine: "postgres"},
		}},
		&daDBUsers{rows: []models.DatabaseUser{{ID: "u1", UserID: "U", Username: "alice_u", Engine: "postgres"}}},
		&daGrants{rows: []models.DatabaseUserGrant{{ID: "g1", DatabaseID: "d1", DatabaseUserID: "u1"}}}
}

func TestRunUploadRestore_GrantsTheRestoredPostgresDatabasesUsersAgain(t *testing.T) {
	const verifier = "SCRAM-SHA-256$4096:c2FsdHNhbHRzYWx0$c3RvcmVka2V5c3RvcmVka2V5:c2VydmVya2V5c2VydmVya2V5"
	h, a, _ := ucUploadPasses(t, func(int) string { return "" })
	h.cfg.Databases = &daDBs{rows: []models.Database{{ID: "x", UserID: "B", Name: "bob_shop"}}}
	h.cfg.DatabaseUsers = &daDBUsers{}
	h.cfg.DatabaseGrants = &daGrants{}
	var dbCalls []string
	h.cfg.Agent.(*mockAgent).callFn = func(_ context.Context, cmd string, params any) (json.RawMessage, error) {
		switch cmd {
		case "backup.restore_from_tar":
			return json.RawMessage(`{"upload_confinement_enforced":true,"restored_databases":["alice_pg"],"archive_mariadb_databases":[],"archive_postgres_databases":["alice_pg"],"restored_postgres_databases":["alice_pg"],"metadata":` +
				`{"user":{"id":"SRC","username":"alice"},` +
				`"databases":[{"id":"d1","name":"alice_pg","engine":"postgres"}],` +
				`"database_users":[{"id":"u1","username":"alice_u","engine":"postgres","postgres_password_verifier":"` + verifier + `",` +
				`"grants":[{"id":"g1","database_id":"d1","database_name":"alice_pg","grant_level":"rw","privileges":"ALL"}]}]}}`), nil
		case "agent.version":
			return json.RawMessage(`{"version":"x","capabilities":["restore_upload_confinement","db_user_create_only","pg_role_create_only"]}`), nil
		case "db.postgres.create_role", "db.postgres.grant":
			raw, _ := json.Marshal(params)
			dbCalls = append(dbCalls, cmd+" "+string(raw))
			return json.RawMessage(`{"ok":true}`), nil
		}
		return nil, fmt.Errorf("unexpected %s", cmd)
	}
	h.runUploadRestore(a)

	// The metadata rebuild's grant, then the restored database's grant.
	if len(dbCalls) != 3 || !strings.HasPrefix(dbCalls[2], "db.postgres.grant ") ||
		!strings.Contains(dbCalls[2], `"db_name":"alice_pg"`) || !strings.Contains(dbCalls[2], `"role":"alice_u"`) {
		t.Fatalf("agent database calls %v, want alice_u granted on alice_pg again after the rebuild", dbCalls)
	}
	o, err := readRestoreUploadOutcome(a.outcomePath)
	if err != nil || o.Status != "done" {
		t.Fatalf("outcome %+v err=%v, want done", o, err)
	}
}

func TestRunTenantUploadRestore_GrantsTheRestoredPostgresDatabasesUsersAgain(t *testing.T) {
	dir := t.TempDir()
	var grants []string
	ag := pgRegrantAgent("backup.restore_from_tar",
		`{"applied":["db → alice_pg (postgres)"],"db_allowlist_enforced":true,"mail_allowlist_enforced":true,"restored_postgres_databases":["alice_pg","alice_bare"]}`, &grants)
	dbs, users, gr := pgRegrantRows()
	h := &meBackupHandler{cfg: MeBackupsHandlerConfig{Agent: ag, Databases: dbs, DatabaseUsers: users, DatabaseGrants: gr}}
	oc := filepath.Join(dir, "o.json")

	h.runTenantUploadRestore(tenantUploadRestoreArgs{
		path: filepath.Join(dir, "a.tar.zst"), outcomePath: oc, userID: "U", username: "alice",
		allowedDBs: []string{"alice_pg", "alice_bare"}, allowedDomains: []string{},
	})

	if strings.Join(grants, ",") != "alice_pg alice_u" {
		t.Errorf("grants %v, want alice_u on alice_pg", grants)
	}
	o, err := readRestoreUploadOutcome(oc)
	if err != nil || o.Status != "done" {
		t.Fatalf("outcome %+v err=%v, want done", o, err)
	}
	if !strings.Contains(strings.Join(o.Warnings, "\n"), "db alice_bare (postgres): no database user is granted on it") {
		t.Errorf("warnings %v should say alice_bare has no user to take its tables over", o.Warnings)
	}
}

func TestRunAccountRestoreJob_GrantsTheRestoredPostgresDatabasesUsersAgain(t *testing.T) {
	jobs := newSealCapture()
	var grants []string
	ag := pgRegrantAgent("backup.restore", `{"stages":[{"name":"db","status":"ok"}],"restored_postgres_databases":["alice_pg"]}`, &grants)
	dbs, users, gr := pgRegrantRows()
	h := &backupHandler{cfg: BackupHandlerConfig{Jobs: jobs, Agent: ag, Databases: dbs, DatabaseUsers: users, DatabaseGrants: gr}}

	h.runAccountRestoreJob("job-1", &models.BackupDestination{ID: "d1", Kind: "local"}, map[string]any{"target_user_id": "U"})
	jobs.wait(t)

	if strings.Join(grants, ",") != "alice_pg alice_u" {
		t.Errorf("grants %v, want alice_u on alice_pg", grants)
	}
	if jobs.status != models.BackupJobStatusSucceeded {
		t.Errorf("status %q (%s), want succeeded", jobs.status, jobs.errText)
	}
}

func TestRunSelectiveRestoreJob_GrantsTheRestoredPostgresDatabasesUsersAgain(t *testing.T) {
	jobs := newSealCapture()
	var grants []string
	ag := pgRegrantAgent("backup.restore_selective", `{"applied":["db → alice_pg (postgres)"],"skipped":[],"warnings":[],"restored_postgres_databases":["alice_pg"]}`, &grants)
	dbs, users, gr := pgRegrantRows()
	h := &meBackupHandler{cfg: MeBackupsHandlerConfig{Jobs: jobs, Agent: ag, Databases: dbs, DatabaseUsers: users, DatabaseGrants: gr}}

	h.runSelectiveRestoreJob("job-2", "snap", "alice", "U",
		meRestoreSelectiveRequest{Databases: []string{"alice_pg"}, Overwrite: true}, nil, nil)
	jobs.wait(t)

	if strings.Join(grants, ",") != "alice_pg alice_u" {
		t.Errorf("grants %v, want alice_u on alice_pg", grants)
	}
}
