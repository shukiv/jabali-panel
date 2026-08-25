package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- fakes (JAB-282 CLI db-identity creation) ---

type recCreateAgent struct {
	calls  []string
	params []map[string]any
	failOn map[string]bool // command name -> return an error
}

func (a *recCreateAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	a.calls = append(a.calls, cmd)
	m, _ := params.(map[string]any)
	a.params = append(a.params, m)
	if a.failOn[cmd] {
		return nil, errors.New(cmd + " boom")
	}
	return json.RawMessage(`{}`), nil
}

type fakeCreateDBUsers struct {
	repository.DatabaseUserRepository
	count      int64
	exists     bool
	failCreate bool
	created    *models.DatabaseUser
}

func (f *fakeCreateDBUsers) ExistsByUserAndUsername(_ context.Context, _, _ string) (bool, error) {
	return f.exists, nil
}
func (f *fakeCreateDBUsers) CountByUserID(_ context.Context, _ string) (int64, error) {
	return f.count, nil
}
func (f *fakeCreateDBUsers) Create(_ context.Context, du *models.DatabaseUser) error {
	if f.failCreate {
		return errors.New("row insert boom")
	}
	f.created = du
	return nil
}

type fakeCreatePackages struct {
	repository.PackageRepository
	pkg *models.HostingPackage
}

func (f fakeCreatePackages) FindByID(_ context.Context, _ string) (*models.HostingPackage, error) {
	if f.pkg != nil {
		return f.pkg, nil
	}
	return nil, repository.ErrNotFound
}

func packagedUser() *models.User {
	return &models.User{ID: "u1", Username: strp("alice"), PackageID: strp("pkgA")}
}

func createDeps(a *recCreateAgent, dbu *fakeCreateDBUsers, pkgs repository.PackageRepository) dbUserCreateDeps {
	return dbUserCreateDeps{agent: a, dbUsers: dbu, packages: pkgs}
}

// Happy path: engine credential created, row persisted, password revealed.
func TestDBUserCreate_HappyPath(t *testing.T) {
	ag := &recCreateAgent{}
	dbu := &fakeCreateDBUsers{}
	du, pw, err := dbUserCreate(context.Background(), createDeps(ag, dbu, fakeCreatePackages{}),
		dbUserCreateInput{panelUser: packagedUser(), name: "app"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(ag.calls) != 1 || ag.calls[0] != "db_user.create" {
		t.Fatalf("expected one db_user.create call, got %v", ag.calls)
	}
	if du.Username != "alice_app" || pw == "" || dbu.created == nil {
		t.Fatalf("unexpected result: du=%+v pw=%q created=%v", du, pw, dbu.created)
	}
}

// JAB-282 bug #1: package max_database_users quota must be enforced (CLI parity
// with REST). At-limit → error BEFORE any engine credential is created.
func TestDBUserCreate_QuotaExceeded_NoEngineCall(t *testing.T) {
	ag := &recCreateAgent{}
	dbu := &fakeCreateDBUsers{count: 3}
	pkgs := fakeCreatePackages{pkg: &models.HostingPackage{MaxDatabaseUsers: 3}}
	_, _, err := dbUserCreate(context.Background(), createDeps(ag, dbu, pkgs),
		dbUserCreateInput{panelUser: packagedUser(), name: "app"})
	if err == nil {
		t.Fatal("expected quota_exceeded error at the limit")
	}
	if len(ag.calls) != 0 {
		t.Fatalf("quota must be enforced BEFORE creating the engine credential, got %v", ag.calls)
	}
}

// Under the limit still creates.
func TestDBUserCreate_UnderQuota_Creates(t *testing.T) {
	ag := &recCreateAgent{}
	dbu := &fakeCreateDBUsers{count: 2}
	pkgs := fakeCreatePackages{pkg: &models.HostingPackage{MaxDatabaseUsers: 3}}
	if _, _, err := dbUserCreate(context.Background(), createDeps(ag, dbu, pkgs),
		dbUserCreateInput{panelUser: packagedUser(), name: "app"}); err != nil {
		t.Fatalf("under quota should succeed: %v", err)
	}
	if len(ag.calls) != 1 || ag.calls[0] != "db_user.create" {
		t.Fatalf("expected create call, got %v", ag.calls)
	}
}

// JAB-282 bug #2: a row-insert failure must compensate by dropping the live
// engine credential — never leave an orphaned MariaDB user / PostgreSQL role.
func TestDBUserCreate_RowInsertFails_CompensatesDrop(t *testing.T) {
	ag := &recCreateAgent{}
	dbu := &fakeCreateDBUsers{failCreate: true}
	_, _, err := dbUserCreate(context.Background(), createDeps(ag, dbu, fakeCreatePackages{}),
		dbUserCreateInput{panelUser: packagedUser(), name: "app"})
	if err == nil {
		t.Fatal("expected row-insert error")
	}
	if len(ag.calls) != 2 || ag.calls[0] != "db_user.create" || ag.calls[1] != "db_user.drop" {
		t.Fatalf("row-insert failure must compensate with a drop, got %v", ag.calls)
	}
	if ag.params[1]["db_user_name"] != "alice_app" {
		t.Errorf("drop must target the created credential, got %v", ag.params[1])
	}
}

// Postgres row-insert failure compensates with the role-drop verb + role param.
func TestDBUserCreate_Postgres_RowInsertFails_CompensatesRoleDrop(t *testing.T) {
	ag := &recCreateAgent{}
	dbu := &fakeCreateDBUsers{failCreate: true}
	ss := okServerSettings{enabled: true}
	deps := createDeps(ag, dbu, fakeCreatePackages{})
	deps.serverSettings = ss
	_, _, err := dbUserCreate(context.Background(), deps,
		dbUserCreateInput{panelUser: packagedUser(), name: "app", engine: "postgres"})
	if err == nil {
		t.Fatal("expected row-insert error")
	}
	if len(ag.calls) != 2 || ag.calls[0] != "db.postgres.create_role" || ag.calls[1] != "db.postgres.drop_role" {
		t.Fatalf("postgres compensation must drop the role, got %v", ag.calls)
	}
	if ag.params[1]["role"] != "alice_app" {
		t.Errorf("role-drop must target the created role, got %v", ag.params[1])
	}
}

type okServerSettings struct {
	repository.ServerSettingsRepository
	enabled bool
}

func (s okServerSettings) Get(_ context.Context) (*models.ServerSettings, error) {
	return &models.ServerSettings{PostgresEnabled: s.enabled}, nil
}
