package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type recGrantAgent struct {
	calls      []string
	params     []map[string]any
	failGrant  bool
	failRevoke bool
}

func (a *recGrantAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	a.calls = append(a.calls, cmd)
	m, _ := params.(map[string]any)
	a.params = append(a.params, m)
	if cmd == "db_user.grant" && a.failGrant {
		return nil, errors.New("grant boom")
	}
	if cmd == "db_user.revoke" && a.failRevoke {
		return nil, errors.New("revoke boom")
	}
	return json.RawMessage(`{}`), nil
}

type fakeDBUsers struct {
	repository.DatabaseUserRepository
	u *models.DatabaseUser
}

func (f fakeDBUsers) FindByID(_ context.Context, id string) (*models.DatabaseUser, error) {
	if f.u != nil && f.u.ID == id {
		return f.u, nil
	}
	return nil, repository.ErrNotFound
}

type fakeDatabases struct {
	repository.DatabaseRepository
	rows []models.Database
}

func (f fakeDatabases) ListByUserID(_ context.Context, userID string, _ repository.ListOptions) ([]models.Database, int64, error) {
	var out []models.Database
	for _, d := range f.rows {
		if d.UserID == userID {
			out = append(out, d)
		}
	}
	return out, int64(len(out)), nil
}

type fakeGrants struct {
	repository.DatabaseUserGrantRepository
	existing   *models.DatabaseUserGrant
	created    *models.DatabaseUserGrant
	failCreate bool
}

func (f *fakeGrants) FindByDBAndDBUser(_ context.Context, _, _ string) (*models.DatabaseUserGrant, error) {
	if f.existing != nil {
		return f.existing, nil
	}
	return nil, repository.ErrNotFound
}
func (f *fakeGrants) Create(_ context.Context, g *models.DatabaseUserGrant) error {
	if f.failCreate {
		return errors.New("create boom")
	}
	f.created = g
	return nil
}

func mariadbUser() *models.DatabaseUser {
	return &models.DatabaseUser{ID: "du1", UserID: "u1", Username: "u1_app", Engine: "mariadb"}
}

func grantDeps(a *recGrantAgent, g *fakeGrants) dbGrantDeps {
	return dbGrantDeps{
		agent:     a,
		dbUsers:   fakeDBUsers{u: mariadbUser()},
		databases: fakeDatabases{rows: []models.Database{{ID: "d1", UserID: "u1", Name: "u1_appdb"}}},
		grants:    g,
	}
}

func TestDBUserGrantCreate_PersistsRowAndGrants(t *testing.T) {
	ag := &recGrantAgent{}
	gr := &fakeGrants{}
	g, du, err := dbUserGrantCreate(context.Background(), grantDeps(ag, gr), "du1", "u1_appdb", []string{"SELECT"}, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(ag.calls) != 1 || ag.calls[0] != "db_user.grant" {
		t.Fatalf("expected one db_user.grant call, got %v", ag.calls)
	}
	if gr.created == nil || gr.created.DatabaseID != "d1" || gr.created.DatabaseUserID != "du1" {
		t.Fatalf("desired-state row not persisted correctly: %+v", gr.created)
	}
	if gr.created.Privileges != "SELECT" || gr.created.GrantLevel != "ro" {
		t.Errorf("row privileges/level wrong: %+v", gr.created)
	}
	if g.ID != gr.created.ID || du.UserID != "u1" {
		t.Errorf("return values wrong: g=%v du=%v", g, du)
	}
}

func TestDBUserGrantCreate_UnknownOrUnownedDB_NoAgentCall(t *testing.T) {
	ag := &recGrantAgent{}
	_, _, err := dbUserGrantCreate(context.Background(), grantDeps(ag, &fakeGrants{}), "du1", "someone_elses_db", []string{"SELECT"}, "")
	if err == nil {
		t.Fatal("expected error for a db not owned by the db user's owner")
	}
	if len(ag.calls) != 0 {
		t.Errorf("must not touch the engine when the db is unresolved, got %v", ag.calls)
	}
}

func TestDBUserGrantCreate_Duplicate_NoAgentCall(t *testing.T) {
	ag := &recGrantAgent{}
	gr := &fakeGrants{existing: &models.DatabaseUserGrant{ID: "gExisting"}}
	_, _, err := dbUserGrantCreate(context.Background(), grantDeps(ag, gr), "du1", "u1_appdb", []string{"SELECT"}, "")
	if err == nil {
		t.Fatal("expected duplicate error")
	}
	if len(ag.calls) != 0 {
		t.Errorf("duplicate must be caught BEFORE the agent call, got %v", ag.calls)
	}
}

func TestDBUserGrantCreate_PersistFail_CompensatesRevoke(t *testing.T) {
	ag := &recGrantAgent{}
	gr := &fakeGrants{failCreate: true}
	_, _, err := dbUserGrantCreate(context.Background(), grantDeps(ag, gr), "du1", "u1_appdb", []string{"SELECT", "INSERT"}, "")
	if err == nil {
		t.Fatal("expected persist error")
	}
	if len(ag.calls) != 2 || ag.calls[0] != "db_user.grant" || ag.calls[1] != "db_user.revoke" {
		t.Fatalf("persist failure must compensate with a revoke, got %v", ag.calls)
	}
	// The compensating revoke must target the same privileges it granted.
	revoke := ag.params[1]
	privs, _ := revoke["privileges"].([]string)
	if len(privs) != 2 || privs[0] != "SELECT" || privs[1] != "INSERT" {
		t.Errorf("revoke privileges must match the grant, got %v", revoke["privileges"])
	}
}

func TestDBUserGrantCreate_Postgres_Rejected(t *testing.T) {
	ag := &recGrantAgent{}
	d := grantDeps(ag, &fakeGrants{})
	d.dbUsers = fakeDBUsers{u: &models.DatabaseUser{ID: "du1", UserID: "u1", Username: "pg", Engine: "postgres"}}
	_, _, err := dbUserGrantCreate(context.Background(), d, "du1", "u1_appdb", []string{"SELECT"}, "")
	if err == nil {
		t.Fatal("postgres grant via CLI must be rejected in v1")
	}
	if len(ag.calls) != 0 {
		t.Errorf("must not call the engine for a rejected postgres grant, got %v", ag.calls)
	}
}
