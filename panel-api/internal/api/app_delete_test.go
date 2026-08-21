package api

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// Fakes embed the repo interfaces and override only the methods RunAppDelete
// exercises; anything else would nil-panic, which is the point — the test proves
// exactly which rows the lifecycle touches.

type adAgent struct {
	failApp, failDBUser, failDB bool
	calls                       []string
}

func (a *adAgent) Call(_ context.Context, cmd string, _ any) (json.RawMessage, error) {
	a.calls = append(a.calls, cmd)
	switch cmd {
	case "app.delete":
		if a.failApp {
			return nil, errors.New("app.delete boom")
		}
	case "db_user.drop":
		if a.failDBUser {
			return nil, errors.New("db_user.drop boom")
		}
	case "db.drop":
		if a.failDB {
			return nil, errors.New("db.drop boom")
		}
	}
	return json.RawMessage("{}"), nil
}

type adInstalls struct {
	repository.ApplicationInstallRepository
	status  string
	deleted bool
}

func (r *adInstalls) UpdateStatus(_ context.Context, _, status string, _, _ *string) error {
	r.status = status
	return nil
}
func (r *adInstalls) Delete(context.Context, string) error { r.deleted = true; return nil }

type adDatabases struct {
	repository.DatabaseRepository
	deleted bool
}

func (r *adDatabases) FindByID(context.Context, string) (*models.Database, error) {
	return &models.Database{Name: "db1"}, nil
}
func (r *adDatabases) Delete(context.Context, string) error { r.deleted = true; return nil }

type adDBUsers struct {
	repository.DatabaseUserRepository
	deleted bool
}

func (r *adDBUsers) Delete(context.Context, string) error { r.deleted = true; return nil }

type adGrants struct {
	repository.DatabaseUserGrantRepository
	deletedGrants int
}

func (r *adGrants) ListByDatabaseUserID(context.Context, string) ([]models.DatabaseUserGrant, error) {
	return []models.DatabaseUserGrant{{ID: "g1"}}, nil
}
func (r *adGrants) Delete(context.Context, string) error { r.deletedGrants++; return nil }

type adCron struct {
	repository.CronJobRepository
	listed bool
}

func (r *adCron) ListByUserID(context.Context, string) ([]*models.CronJob, error) {
	r.listed = true
	return nil, nil
}

func newAppDeleteFakes() (*adAgent, *adInstalls, *adDatabases, *adDBUsers, *adGrants, *adCron, AppDeleteDeps) {
	ag, inst, dbs, dbu, gr, cr := &adAgent{}, &adInstalls{}, &adDatabases{}, &adDBUsers{}, &adGrants{}, &adCron{}
	return ag, inst, dbs, dbu, gr, cr, AppDeleteDeps{
		Installs: inst, Databases: dbs, DatabaseUsers: dbu, DatabaseGrants: gr, CronJobs: cr, Agent: ag,
	}
}

func appDeleteArgs() AppDeleteArgs {
	return AppDeleteArgs{
		InstallID: "i1", UserID: "u1", AppType: "wordpress", OSUser: "alice",
		Docroot: "/home/alice/public_html", DomainName: "ex.com",
		DatabaseID: "d1", DBUserID: "du1", DBUserUsername: "alice_wp",
	}
}

// JAB-314: an agent app.delete failure must RETAIN the install row (retryable)
// and must NOT drop any DB row — the old CLI dropped everything regardless,
// which is how invisible orphans were made.
func TestRunAppDelete_AgentFailureRetainsRowNoDrops(t *testing.T) {
	ag, inst, dbs, dbu, _, _, deps := newAppDeleteFakes()
	ag.failApp = true
	if err := RunAppDelete(appDeleteArgs(), deps); err == nil {
		t.Fatal("want an error on agent app.delete failure")
	}
	if inst.status != "failed" {
		t.Errorf("install status = %q, want failed", inst.status)
	}
	if inst.deleted {
		t.Error("install row must be RETAINED on agent failure, not deleted")
	}
	if dbs.deleted || dbu.deleted {
		t.Error("no DB rows may be dropped when the agent app.delete failed")
	}
}

// JAB-314: a db_user.drop / db.drop failure must KEEP the DB rows visible (not
// deleted) so the database/user never becomes an invisible orphan.
func TestRunAppDelete_DropFailureKeepsDBRowsVisible(t *testing.T) {
	ag, inst, dbs, dbu, gr, _, deps := newAppDeleteFakes()
	ag.failDBUser = true
	if err := RunAppDelete(appDeleteArgs(), deps); err == nil {
		t.Fatal("want an error when a DB drop fails")
	}
	if !inst.deleted {
		t.Error("install row should be deleted (its files are gone)")
	}
	if dbs.deleted || dbu.deleted || gr.deletedGrants > 0 {
		t.Error("DB/user/grant rows must be KEPT visible when a drop fails, not deleted")
	}
}

// Happy path: every row removed, cron teardown attempted, no error.
func TestRunAppDelete_HappyPathRemovesEverything(t *testing.T) {
	_, inst, dbs, dbu, gr, cr, deps := newAppDeleteFakes()
	if err := RunAppDelete(appDeleteArgs(), deps); err != nil {
		t.Fatalf("want nil error, got %v", err)
	}
	if !inst.deleted || !dbs.deleted || !dbu.deleted || gr.deletedGrants == 0 {
		t.Errorf("all rows must be removed on success: install=%v db=%v dbuser=%v grants=%d",
			inst.deleted, dbs.deleted, dbu.deleted, gr.deletedGrants)
	}
	if !cr.listed {
		t.Error("cron teardown must run for a wordpress app — the CLI path never did this before")
	}
}
