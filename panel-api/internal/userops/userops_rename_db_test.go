package userops

// GH #1238 — the DB re-prefix orchestration is security-critical (a slip in the
// grant re-point is the cross-tenant hole we're closing), so pin the full
// sequence with fakes: DB renamed → DB user renamed → grant re-pointed onto the
// new names AND the stale old-name grant revoked → shadow role renamed +
// wildcard re-granted → panel rows updated.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type rdbCall struct {
	method string
	params map[string]any
}

type rdbAgent struct{ calls []rdbCall }

func (a *rdbAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	m, _ := params.(map[string]any)
	a.calls = append(a.calls, rdbCall{method, m})
	return []byte("{}"), nil
}
func (a *rdbAgent) first(method string) *rdbCall {
	for i := range a.calls {
		if a.calls[i].method == method {
			return &a.calls[i]
		}
	}
	return nil
}
func (a *rdbAgent) count(method string) int {
	n := 0
	for i := range a.calls {
		if a.calls[i].method == method {
			n++
		}
	}
	return n
}

type rdbDatabases struct {
	repository.DatabaseRepository
	rows    []models.Database
	renamed map[string]string
}

func (r *rdbDatabases) ListByUserID(context.Context, string, repository.ListOptions) ([]models.Database, int64, error) {
	return r.rows, int64(len(r.rows)), nil
}
func (r *rdbDatabases) UpdateName(_ context.Context, id, name string) error {
	if r.renamed == nil {
		r.renamed = map[string]string{}
	}
	r.renamed[id] = name
	return nil
}

type rdbDBUsers struct {
	repository.DatabaseUserRepository
	rows    []models.DatabaseUser
	renamed map[string]string
}

func (r *rdbDBUsers) ListByUserID(context.Context, string, repository.ListOptions) ([]models.DatabaseUser, int64, error) {
	return r.rows, int64(len(r.rows)), nil
}
func (r *rdbDBUsers) UpdateUsername(_ context.Context, id, username string) error {
	if r.renamed == nil {
		r.renamed = map[string]string{}
	}
	r.renamed[id] = username
	return nil
}

type rdbGrants struct {
	repository.DatabaseUserGrantRepository
	rows []models.DatabaseUserGrant
}

func (r *rdbGrants) ListByDatabaseUserIDs(context.Context, []string) ([]models.DatabaseUserGrant, error) {
	return r.rows, nil
}

type rdbUsers struct {
	repository.UserRepository
	shadowM, shadowP *string
}

func (r *rdbUsers) UpdateShadowDBUsernames(_ context.Context, _ string, m, p *string) error {
	r.shadowM, r.shadowP = m, p
	return nil
}

func TestRenameDBArtifacts_MariaDB_FullReprefix(t *testing.T) {
	agent := &rdbAgent{}
	dbs := &rdbDatabases{rows: []models.Database{{ID: "db1", UserID: "u1", Name: "alice_wp_1", Engine: "mariadb"}}}
	dus := &rdbDBUsers{rows: []models.DatabaseUser{{ID: "du1", UserID: "u1", Username: "alice_app", Engine: "mariadb"}}}
	grants := &rdbGrants{rows: []models.DatabaseUserGrant{{ID: "g1", DatabaseID: "db1", DatabaseUserID: "du1", Privileges: "ALL"}}}
	users := &rdbUsers{}
	mysqladmin := "alice_mysqladmin"
	target := &models.User{ID: "u1", MysqladminUsername: &mysqladmin}

	d := Deps{Users: users, Agent: agent}
	rd := RenameDeps{Databases: dbs, DatabaseUsers: dus, DBUserGrants: grants}
	if err := renameUserDBArtifacts(context.Background(), d, rd, target, "alice", "bob"); err != nil {
		t.Fatalf("orchestration: %v", err)
	}

	// Database moved + row repointed.
	if c := agent.first("db.rename_db"); c == nil || c.params["old_db"] != "alice_wp_1" || c.params["new_db"] != "bob_wp_1" {
		t.Fatalf("db.rename_db: %+v", c)
	}
	assert.Equal(t, "bob_wp_1", dbs.renamed["db1"])

	// DB user renamed + row repointed (one non-shadow rename + the shadow one).
	assert.Equal(t, "bob_app", dus.renamed["du1"])

	// Grant re-pointed to the new names AND the stale old-name grant revoked.
	if c := agent.first("db_user.grant"); c == nil || c.params["db_name"] != "bob_wp_1" || c.params["db_user_name"] != "bob_app" {
		t.Fatalf("db_user.grant: %+v", c)
	}
	// The revoke MUST carry the privilege list — the agent verb rejects a revoke
	// with neither privileges nor grant_level (the bug the box drill caught).
	if c := agent.first("db_user.revoke"); c == nil || c.params["db_name"] != "alice_wp_1" ||
		c.params["db_user_name"] != "bob_app" || c.params["privileges"] == nil {
		t.Fatalf("db_user.revoke (stale, with privileges): %+v", c)
	}

	// Shadow role renamed with the wildcard re-grant prefixes, row repointed.
	// Two db.rename_user calls: the per-DB user + the shadow role.
	assert.Equal(t, 2, agent.count("db.rename_user"))
	var shadow *rdbCall
	for i := range agent.calls {
		if agent.calls[i].method == "db.rename_user" && agent.calls[i].params["old_name"] == "alice_mysqladmin" {
			shadow = &agent.calls[i]
		}
	}
	if shadow == nil || shadow.params["new_name"] != "bob_mysqladmin" ||
		shadow.params["old_prefix"] != "alice" || shadow.params["new_prefix"] != "bob" {
		t.Fatalf("shadow rename_user: %+v", shadow)
	}
	if users.shadowM == nil || *users.shadowM != "bob_mysqladmin" {
		t.Fatalf("shadow username not repointed: %v", users.shadowM)
	}
}

func TestRefusePostgresArtifacts(t *testing.T) {
	dbs := &rdbDatabases{rows: []models.Database{{ID: "db1", UserID: "u1", Name: "alice_pg", Engine: "postgres"}}}
	rd := RenameDeps{Databases: dbs, DatabaseUsers: &rdbDBUsers{}}
	err := refusePostgresArtifacts(context.Background(), rd, &models.User{ID: "u1"}, "alice")
	if err == nil {
		t.Fatal("expected refusal for a PostgreSQL database")
	}
	assert.Contains(t, err.Error(), "PostgreSQL")
}
