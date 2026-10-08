package dbops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1993: after a restore loads a PostgreSQL database, each database user
// the panel has on it is granted again; the first takes over the restored
// objects.

type rgAgent struct {
	calls  []string
	failOn string
}

func (a *rgAgent) Call(_ context.Context, command string, params any) (json.RawMessage, error) {
	p, _ := params.(map[string]any)
	call := command + " " + p["db_name"].(string) + " " + p["role"].(string)
	a.calls = append(a.calls, call)
	if call == a.failOn {
		return nil, errors.New("boom")
	}
	return json.RawMessage(`{}`), nil
}

type rgDatabases struct {
	repository.DatabaseRepository
	rows []models.Database
}

func (r *rgDatabases) ListByUserID(_ context.Context, userID string, _ repository.ListOptions) ([]models.Database, int64, error) {
	var out []models.Database
	for _, d := range r.rows {
		if d.UserID == userID {
			out = append(out, d)
		}
	}
	return out, int64(len(out)), nil
}

type rgGrants struct {
	repository.DatabaseUserGrantRepository
	byDB map[string][]models.DatabaseUserGrant
}

func (r *rgGrants) ListByDatabaseID(_ context.Context, id string) ([]models.DatabaseUserGrant, error) {
	return r.byDB[id], nil
}

type rgUsers struct {
	repository.DatabaseUserRepository
	byID map[string]*models.DatabaseUser
}

func (r *rgUsers) FindByID(_ context.Context, id string) (*models.DatabaseUser, error) {
	if u, ok := r.byID[id]; ok {
		return u, nil
	}
	return nil, repository.ErrNotFound
}

func regrantWorld() (*rgDatabases, *rgGrants, *rgUsers) {
	dbs := &rgDatabases{rows: []models.Database{
		{ID: "d1", UserID: "acct", Name: "alice_app", Engine: "postgres"},
		{ID: "d2", UserID: "acct", Name: "alice_bare", Engine: "postgres"},
		{ID: "d3", UserID: "acct", Name: "alice_wp", Engine: "mariadb"},
		{ID: "d4", UserID: "other", Name: "bob_app", Engine: "postgres"},
		{ID: "d5", UserID: "acct", Name: "alice_left", Engine: "postgres"},
	}}
	grants := &rgGrants{byDB: map[string][]models.DatabaseUserGrant{
		"d1": {{DatabaseUserID: "u1"}, {DatabaseUserID: "u2"}},
		"d3": {{DatabaseUserID: "u3"}},
		"d4": {{DatabaseUserID: "u4"}},
		"d5": {{DatabaseUserID: "u1"}},
	}}
	users := &rgUsers{byID: map[string]*models.DatabaseUser{
		"u1": {ID: "u1", UserID: "acct", Username: "alice_owner", Engine: "postgres"},
		"u2": {ID: "u2", UserID: "acct", Username: "alice_ro", Engine: "postgres"},
		"u3": {ID: "u3", UserID: "acct", Username: "alice_wpu", Engine: "mariadb"},
		"u4": {ID: "u4", UserID: "other", Username: "bob_u", Engine: "postgres"},
	}}
	return dbs, grants, users
}

func TestRegrantRestoredPostgres_GrantsEachUserOnTheRestoredDatabasesFirstOneFirst(t *testing.T) {
	dbs, grants, users := regrantWorld()
	ag := &rgAgent{}

	errs, notes := RegrantRestoredPostgres(context.Background(), ag, dbs, grants, users, "acct",
		[]string{"alice_app", "alice_bare", "alice_wp", "bob_app"})

	want := "db.postgres.grant alice_app alice_owner|db.postgres.grant alice_app alice_ro"
	if got := strings.Join(ag.calls, "|"); got != want {
		t.Errorf("grants %q, want %q (only the account's restored PostgreSQL databases, its first user first)", got, want)
	}
	if len(errs) != 0 {
		t.Errorf("errors %v", errs)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "db alice_bare (postgres): no database user is granted on it") {
		t.Errorf("notes %v, want one for alice_bare", notes)
	}
}

func TestRegrantRestoredPostgres_ReportsAGrantThatFails(t *testing.T) {
	dbs, grants, users := regrantWorld()
	ag := &rgAgent{failOn: "db.postgres.grant alice_app alice_owner"}

	errs, _ := RegrantRestoredPostgres(context.Background(), ag, dbs, grants, users, "acct", []string{"alice_app"})

	if len(errs) != 1 || !strings.Contains(errs[0], "db alice_app (postgres): granting alice_owner on it again failed") {
		t.Errorf("errors %v, want the failed grant", errs)
	}
	if len(ag.calls) != 2 {
		t.Errorf("a failed grant stopped the rest: %v", ag.calls)
	}
}

func TestRegrantRestoredPostgres_NothingToDo(t *testing.T) {
	dbs, grants, users := regrantWorld()
	for name, c := range map[string]struct {
		account string
		names   []string
	}{
		"no databases loaded": {"acct", nil},
		"no account":          {"", []string{"alice_app"}},
	} {
		ag := &rgAgent{}
		errs, notes := RegrantRestoredPostgres(context.Background(), ag, dbs, grants, users, c.account, c.names)
		if len(ag.calls)+len(errs)+len(notes) != 0 {
			t.Errorf("%s: calls %v errs %v notes %v", name, ag.calls, errs, notes)
		}
	}
}
