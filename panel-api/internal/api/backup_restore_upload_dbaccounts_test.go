package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1993: restoring an uploaded account backup recreates the MariaDB
// account and grants of each database user it restores, not just their panel
// rows, so the site's database login works on the new server.

type daDBs struct {
	repository.DatabaseRepository
	rows []models.Database
}

func (r *daDBs) List(context.Context, repository.ListOptions) ([]models.Database, int64, error) {
	return r.rows, int64(len(r.rows)), nil
}
func (r *daDBs) Create(_ context.Context, d *models.Database) error {
	r.rows = append(r.rows, *d)
	return nil
}
func (r *daDBs) ListByUserID(_ context.Context, userID string, _ repository.ListOptions) ([]models.Database, int64, error) {
	var out []models.Database
	for _, d := range r.rows {
		if d.UserID == userID {
			out = append(out, d)
		}
	}
	return out, int64(len(out)), nil
}
func (r *daDBs) FindByID(_ context.Context, id string) (*models.Database, error) {
	for _, d := range r.rows {
		if d.ID == id {
			return &d, nil
		}
	}
	return nil, repository.ErrNotFound
}

type daDBUsers struct {
	repository.DatabaseUserRepository
	rows []models.DatabaseUser
}

func (r *daDBUsers) List(context.Context, repository.ListOptions) ([]models.DatabaseUser, int64, error) {
	return r.rows, int64(len(r.rows)), nil
}
func (r *daDBUsers) Create(_ context.Context, u *models.DatabaseUser) error {
	r.rows = append(r.rows, *u)
	return nil
}
func (r *daDBUsers) FindByID(_ context.Context, id string) (*models.DatabaseUser, error) {
	for _, u := range r.rows {
		if u.ID == id {
			return &u, nil
		}
	}
	return nil, repository.ErrNotFound
}

type daGrants struct {
	repository.DatabaseUserGrantRepository
	rows []models.DatabaseUserGrant
}

func (r *daGrants) Create(_ context.Context, g *models.DatabaseUserGrant) error {
	r.rows = append(r.rows, *g)
	return nil
}
func (r *daGrants) ListByDatabaseID(_ context.Context, id string) ([]models.DatabaseUserGrant, error) {
	var out []models.DatabaseUserGrant
	for _, g := range r.rows {
		if g.DatabaseID == id {
			out = append(out, g)
		}
	}
	return out, nil
}

func TestRunUploadRestore_RecreatesTheRestoredDatabaseUsersMariaDBAccount(t *testing.T) {
	const hash = "*2470C0C06DEE42FD1618BB99005ADCA2EC9D1E19"
	h, a, _ := ucUploadPasses(t, func(int) string { return "" })
	h.cfg.Databases = &daDBs{rows: []models.Database{{ID: "x", UserID: "B", Name: "bob_shop"}}}
	h.cfg.DatabaseUsers = &daDBUsers{}
	h.cfg.DatabaseGrants = &daGrants{}
	var dbCalls []string
	h.cfg.Agent.(*mockAgent).callFn = func(_ context.Context, cmd string, params any) (json.RawMessage, error) {
		switch cmd {
		case "backup.restore_from_tar":
			return json.RawMessage(`{"upload_confinement_enforced":true,"restored_databases":["alice_wp"],"archive_mariadb_databases":["alice_wp"],"metadata":` +
				`{"user":{"id":"SRC","username":"alice"},` +
				`"databases":[{"id":"d1","name":"alice_wp","engine":"mariadb"}],` +
				`"database_users":[{"id":"u1","username":"alice_u","engine":"mariadb","native_password_hash":"` + hash + `",` +
				`"grants":[{"id":"g1","database_id":"d1","database_name":"alice_wp","grant_level":"rw","privileges":"ALL"}]}]}}`), nil
		case "agent.version":
			return json.RawMessage(`{"version":"x","capabilities":["restore_upload_confinement","db_user_create_only"]}`), nil
		case "db_user.create", "db_user.grant":
			raw, _ := json.Marshal(params)
			dbCalls = append(dbCalls, cmd+" "+string(raw))
			return json.RawMessage(`{"ok":true}`), nil
		}
		return nil, fmt.Errorf("unexpected %s", cmd)
	}
	h.runUploadRestore(a)

	if len(dbCalls) != 2 ||
		!strings.HasPrefix(dbCalls[0], "db_user.create ") || !strings.Contains(dbCalls[0], `"db_user_name":"alice_u"`) || !strings.Contains(dbCalls[0], `"password_hash":"`+hash+`"`) || !strings.Contains(dbCalls[0], `"create_only":true`) ||
		!strings.HasPrefix(dbCalls[1], "db_user.grant ") || !strings.Contains(dbCalls[1], `"db_name":"alice_wp"`) {
		t.Fatalf("agent database calls %v, want alice_u created with the backup's hash, then granted on alice_wp", dbCalls)
	}
	o, err := readRestoreUploadOutcome(a.outcomePath)
	if err != nil || o.Status != "done" || len(o.Warnings) != 0 {
		t.Fatalf("outcome %+v err=%v, want done without warnings", o, err)
	}
}

// The same for a PostgreSQL database user: its role is created with the
// backup's verifier, and the grant is made on the database the agent says
// holds only the upload's data (archive_postgres_databases).
func TestRunUploadRestore_RecreatesTheRestoredDatabaseUsersPostgresRole(t *testing.T) {
	const verifier = "SCRAM-SHA-256$4096:c2FsdHNhbHRzYWx0$c3RvcmVka2V5c3RvcmVka2V5:c2VydmVya2V5c2VydmVya2V5"
	h, a, _ := ucUploadPasses(t, func(int) string { return "" })
	h.cfg.Databases = &daDBs{rows: []models.Database{{ID: "x", UserID: "B", Name: "bob_shop"}}}
	h.cfg.DatabaseUsers = &daDBUsers{}
	h.cfg.DatabaseGrants = &daGrants{}
	var dbCalls []string
	h.cfg.Agent.(*mockAgent).callFn = func(_ context.Context, cmd string, params any) (json.RawMessage, error) {
		switch cmd {
		case "backup.restore_from_tar":
			return json.RawMessage(`{"upload_confinement_enforced":true,"restored_databases":["alice_pg"],"archive_mariadb_databases":[],"archive_postgres_databases":["alice_pg"],"metadata":` +
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

	if len(dbCalls) != 2 ||
		!strings.HasPrefix(dbCalls[0], "db.postgres.create_role ") || !strings.Contains(dbCalls[0], `"role":"alice_u"`) || !strings.Contains(dbCalls[0], `"password_verifier":"`+verifier+`"`) || !strings.Contains(dbCalls[0], `"create_only":true`) ||
		!strings.HasPrefix(dbCalls[1], "db.postgres.grant ") || !strings.Contains(dbCalls[1], `"db_name":"alice_pg"`) || !strings.Contains(dbCalls[1], `"role":"alice_u"`) {
		t.Fatalf("agent database calls %v, want alice_u created with the backup's verifier, then granted on alice_pg", dbCalls)
	}
	o, err := readRestoreUploadOutcome(a.outcomePath)
	if err != nil || o.Status != "done" || len(o.Warnings) != 0 {
		t.Fatalf("outcome %+v err=%v, want done without warnings", o, err)
	}
}
