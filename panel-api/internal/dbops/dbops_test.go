package dbops

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- recording fakes (JAB-275 delete-path transcript) ---

// recAgent records every command it is asked to run, in order, and can be
// told to fail one specific command. This is how the tests assert the exact
// ordered transcript both Adapters now share.
type recAgent struct {
	calls  []string
	failOn string
}

func (a *recAgent) Call(_ context.Context, command string, _ any) (json.RawMessage, error) {
	a.calls = append(a.calls, command)
	if command == a.failOn {
		return nil, errors.New(command + " boom")
	}
	return json.RawMessage(`{}`), nil
}

type recDatabases struct {
	repository.DatabaseRepository
	row     *models.Database
	deleted []string
}

func (r *recDatabases) FindByID(_ context.Context, id string) (*models.Database, error) {
	if r.row == nil || r.row.ID != id {
		return nil, repository.ErrNotFound
	}
	return r.row, nil
}
func (r *recDatabases) Delete(_ context.Context, id string) error {
	r.deleted = append(r.deleted, id)
	return nil
}

type recGrants struct {
	repository.DatabaseUserGrantRepository
	grants  []models.DatabaseUserGrant
	deleted []string
}

func (r *recGrants) ListByDatabaseID(_ context.Context, _ string) ([]models.DatabaseUserGrant, error) {
	return r.grants, nil
}
func (r *recGrants) Delete(_ context.Context, id string) error {
	r.deleted = append(r.deleted, id)
	return nil
}

type recUsers struct {
	repository.DatabaseUserRepository
	byID map[string]*models.DatabaseUser
}

func (r *recUsers) FindByID(_ context.Context, id string) (*models.DatabaseUser, error) {
	if u, ok := r.byID[id]; ok {
		return u, nil
	}
	return nil, repository.ErrNotFound
}

type recInstalls struct {
	repository.ApplicationInstallRepository
	install *models.ApplicationInstall // non-nil → the db is attached
}

func (r *recInstalls) FindByDBID(_ context.Context, _ string) (*models.ApplicationInstall, error) {
	if r.install != nil {
		return r.install, nil
	}
	return nil, repository.ErrNotFound
}

// depsFor builds a fully wired Deps around the given fakes.
func depsFor(ag AgentCaller, dbs *recDatabases, grants *recGrants, users *recUsers, installs *recInstalls) Deps {
	return Deps{
		Databases:      dbs,
		DatabaseGrants: grants,
		DatabaseUsers:  users,
		Installs:       installs,
		Agent:          ag,
	}
}

func mariaRow() *models.Database {
	return &models.Database{ID: "db1", UserID: "u1", Name: "alice_blog", Engine: "mariadb"}
}

// Happy path pins the shared transcript: revoke each grant's engine user,
// drop the schema, then delete the grant rows and the database row. Both the
// REST handler and the CLI now call this one function, so proving the order
// once proves it for both (AC5).
func TestDelete_HappyPath_Transcript(t *testing.T) {
	ag := &recAgent{}
	dbs := &recDatabases{row: mariaRow()}
	grants := &recGrants{grants: []models.DatabaseUserGrant{{ID: "g1", DatabaseID: "db1", DatabaseUserID: "du1"}}}
	users := &recUsers{byID: map[string]*models.DatabaseUser{"du1": {ID: "du1", Username: "alice_app"}}}

	if err := Delete(context.Background(), depsFor(ag, dbs, grants, users, &recInstalls{}), DeleteInput{ID: "db1"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	want := []string{"db_user.revoke", "db.drop"}
	if len(ag.calls) != len(want) {
		t.Fatalf("agent transcript: got %v want %v", ag.calls, want)
	}
	for i := range want {
		if ag.calls[i] != want[i] {
			t.Fatalf("agent transcript[%d]: got %q want %q (full %v)", i, ag.calls[i], want[i], ag.calls)
		}
	}
	if len(grants.deleted) != 1 || grants.deleted[0] != "g1" {
		t.Fatalf("grant rows not deleted: %v", grants.deleted)
	}
	if len(dbs.deleted) != 1 || dbs.deleted[0] != "db1" {
		t.Fatalf("db row not deleted: %v", dbs.deleted)
	}
}

// Engine dispatch is by the row's engine — a postgres row must reach
// db.postgres.drop_db, never db.drop (GH #1013).
func TestDelete_Postgres_UsesPostgresDropCmd(t *testing.T) {
	ag := &recAgent{}
	dbs := &recDatabases{row: &models.Database{ID: "db1", Name: "alice_pg", Engine: "postgres"}}
	if err := Delete(context.Background(), depsFor(ag, dbs, &recGrants{}, &recUsers{}, &recInstalls{}), DeleteInput{ID: "db1"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(ag.calls) != 1 || ag.calls[0] != "db.postgres.drop_db" {
		t.Fatalf("postgres drop cmd: got %v want [db.postgres.drop_db]", ag.calls)
	}
}

// An attached database must be refused before any mutation — no agent call,
// no grant delete, no row delete — and the error must carry the install id.
func TestDelete_Attached_RefusesZeroMutation(t *testing.T) {
	ag := &recAgent{}
	dbs := &recDatabases{row: mariaRow()}
	grants := &recGrants{grants: []models.DatabaseUserGrant{{ID: "g1", DatabaseUserID: "du1"}}}
	installs := &recInstalls{install: &models.ApplicationInstall{ID: "app1"}}

	err := Delete(context.Background(), depsFor(ag, dbs, grants, &recUsers{}, installs), DeleteInput{ID: "db1"})
	if !errors.Is(err, ErrAttached) {
		t.Fatalf("want ErrAttached, got %v", err)
	}
	var ae *AttachedError
	if !errors.As(err, &ae) || ae.InstallID != "app1" {
		t.Fatalf("want AttachedError carrying app1, got %v", err)
	}
	if len(ag.calls) != 0 || len(grants.deleted) != 0 || len(dbs.deleted) != 0 {
		t.Fatalf("attached db mutated something: agent=%v grants=%v dbs=%v", ag.calls, grants.deleted, dbs.deleted)
	}
}

// A failed schema drop must retain the database row AND its grant metadata,
// so the row stays the addressable handle for a retry (AC "Agent failure
// retains the database and all management metadata").
func TestDelete_AgentDropFailure_RetainsRowAndMetadata(t *testing.T) {
	ag := &recAgent{failOn: "db.drop"}
	dbs := &recDatabases{row: mariaRow()}
	grants := &recGrants{grants: []models.DatabaseUserGrant{{ID: "g1", DatabaseUserID: "du1"}}}
	users := &recUsers{byID: map[string]*models.DatabaseUser{"du1": {ID: "du1", Username: "alice_app"}}}

	err := Delete(context.Background(), depsFor(ag, dbs, grants, users, &recInstalls{}), DeleteInput{ID: "db1"})
	if !errors.Is(err, ErrAgentFailed) {
		t.Fatalf("want ErrAgentFailed, got %v", err)
	}
	if len(grants.deleted) != 0 {
		t.Fatalf("grant metadata deleted despite drop failure: %v", grants.deleted)
	}
	if len(dbs.deleted) != 0 {
		t.Fatalf("db row deleted despite drop failure: %v", dbs.deleted)
	}
}

// Every delete-path collaborator is required: a nil one is ErrDeps, never a
// silent skip of the attachment or grant step.
func TestDelete_MissingDeps_ErrDeps(t *testing.T) {
	full := depsFor(&recAgent{}, &recDatabases{row: mariaRow()}, &recGrants{}, &recUsers{}, &recInstalls{})
	for name, mutate := range map[string]func(*Deps){
		"grants":   func(d *Deps) { d.DatabaseGrants = nil },
		"users":    func(d *Deps) { d.DatabaseUsers = nil },
		"installs": func(d *Deps) { d.Installs = nil },
		"agent":    func(d *Deps) { d.Agent = nil },
	} {
		d := full
		mutate(&d)
		if err := Delete(context.Background(), d, DeleteInput{ID: "db1"}); !errors.Is(err, ErrDeps) {
			t.Fatalf("nil %s: want ErrDeps, got %v", name, err)
		}
	}
}

func TestDelete_NotFound(t *testing.T) {
	d := depsFor(&recAgent{}, &recDatabases{row: mariaRow()}, &recGrants{}, &recUsers{}, &recInstalls{})
	if err := Delete(context.Background(), d, DeleteInput{ID: "missing"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
