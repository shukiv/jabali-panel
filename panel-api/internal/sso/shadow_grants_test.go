package sso

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/gorm"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

type gormDBHolder struct{ db *gorm.DB }

// failingAgent fails every call, counting them.
type failingAgent struct{ calls int }

func (f *failingAgent) Call(_ context.Context, _ string, _ any) (json.RawMessage, error) {
	f.calls++
	return nil, errors.New("agent down")
}

func dbRows(names ...string) *sqlmock.Rows {
	r := sqlmock.NewRows([]string{"id", "user_id", "name", "engine"})
	for i, n := range names {
		r.AddRow(string(rune('a'+i)), "user1", n, "mariadb")
	}
	return r
}

const databasesByUserEngine = "SELECT .* FROM `databases` WHERE user_id = \\? AND engine = \\?"

// The phpMyAdmin shadow's grants are synced from the tenant's OWN MariaDB
// databases, passed by exact name — the old one-time `<user>\_%` wildcard also
// matched a sibling tenant whose username starts with "<user>_".
func TestSyncMysqlShadowGrants_PassesOwnMariaDBNames(t *testing.T) {
	db, mock, raw := newMembersMockDB(t)
	defer raw.Close()
	mock.ExpectQuery(databasesByUserEngine).WithArgs("user1", "mariadb").
		WillReturnRows(dbRows("alice_shop", "alice_blog"))

	key := generateTestKey(t)
	agent := &capturingAgent{}
	svc := NewService(db, &mockUsersForSSO{}, &mockTokensForSSO{}, agent, &key, slog.Default())

	if err := svc.syncMysqlShadowGrants(context.Background(), "user1", "alice"); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if agent.calls != 1 || agent.lastCommand != "db.mysqladmin.sync_grants" {
		t.Fatalf("calls=%d command=%q", agent.calls, agent.lastCommand)
	}
	params := agent.lastParams.(map[string]interface{})
	if params["panel_username"] != "alice" {
		t.Fatalf("panel_username = %v", params["panel_username"])
	}
	names, _ := params["db_names"].([]string)
	if len(names) != 2 || names[0] != "alice_shop" || names[1] != "alice_blog" {
		t.Fatalf("db_names = %v, want [alice_shop alice_blog]", params["db_names"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// A tenant with no databases still syncs: an empty list is what revokes a
// stale grant the shadow user already holds.
func TestSyncMysqlShadowGrants_EmptyListStillCallsAgent(t *testing.T) {
	db, mock, raw := newMembersMockDB(t)
	defer raw.Close()
	mock.ExpectQuery(databasesByUserEngine).WithArgs("user1", "mariadb").WillReturnRows(dbRows())

	key := generateTestKey(t)
	agent := &capturingAgent{}
	svc := NewService(db, &mockUsersForSSO{}, &mockTokensForSSO{}, agent, &key, slog.Default())

	if err := svc.syncMysqlShadowGrants(context.Background(), "user1", "alice"); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if agent.calls != 1 {
		t.Fatalf("agent must be called with an empty list, got %d calls", agent.calls)
	}
	names, ok := agent.lastParams.(map[string]interface{})["db_names"].([]string)
	if !ok || names == nil || len(names) != 0 {
		t.Fatalf("db_names = %#v, want an empty non-nil list", agent.lastParams.(map[string]interface{})["db_names"])
	}
}

// A failed database list must never be sent as "no databases", and the open
// must fail.
func TestSyncMysqlShadowGrants_ListErrorFailsWithoutCallingAgent(t *testing.T) {
	db, mock, raw := newMembersMockDB(t)
	defer raw.Close()
	mock.ExpectQuery(databasesByUserEngine).WithArgs("user1", "mariadb").WillReturnError(errors.New("db gone"))

	key := generateTestKey(t)
	agent := &capturingAgent{}
	svc := NewService(db, &mockUsersForSSO{}, &mockTokensForSSO{}, agent, &key, slog.Default())

	if err := svc.syncMysqlShadowGrants(context.Background(), "user1", "alice"); err == nil {
		t.Fatal("expected an error when the database list fails")
	}
	if agent.calls != 0 {
		t.Fatalf("agent must not be called on a list error, got %d", agent.calls)
	}
}

// The agent refusing the sync fails the call (the phpMyAdmin open is refused).
func TestSyncMysqlShadowGrants_AgentFailureIsAnError(t *testing.T) {
	db, mock, raw := newMembersMockDB(t)
	defer raw.Close()
	mock.ExpectQuery(databasesByUserEngine).WithArgs("user1", "mariadb").WillReturnRows(dbRows("alice_shop"))

	key := generateTestKey(t)
	agent := &failingAgent{}
	svc := NewService(db, &mockUsersForSSO{}, &mockTokensForSSO{}, agent, &key, slog.Default())

	if err := svc.syncMysqlShadowGrants(context.Background(), "user1", "alice"); err == nil {
		t.Fatal("expected the agent failure to surface")
	}
}

// Adminer: the database-level grants follow the same rules — explicit own
// list, empty still sent, list error and agent error both fail the open.
func TestSyncPgShadowSchema_EmptyListStillCallsAgent(t *testing.T) {
	db, mock, raw := newMembersMockDB(t)
	defer raw.Close()
	mock.ExpectQuery(databasesByUserEngine).WithArgs("user1", "postgres").WillReturnRows(dbRows())

	key := generateTestKey(t)
	agent := &capturingAgent{}
	svc := NewAdminerService(NewService(db, &mockUsersForSSO{}, &mockTokensForSSO{}, agent, &key, slog.Default()), nil)

	if err := svc.syncPgShadowSchema(context.Background(), "user1", "alice"); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if agent.calls != 1 || agent.lastCommand != "db.postgres.shadowadmin.grant_schema" {
		t.Fatalf("calls=%d command=%q", agent.calls, agent.lastCommand)
	}
	names, ok := agent.lastParams.(map[string]interface{})["db_names"].([]string)
	if !ok || names == nil || len(names) != 0 {
		t.Fatalf("db_names = %#v, want an empty non-nil list", agent.lastParams.(map[string]interface{})["db_names"])
	}
}

func TestSyncPgShadowSchema_PassesOwnPostgresNames(t *testing.T) {
	db, mock, raw := newMembersMockDB(t)
	defer raw.Close()
	mock.ExpectQuery(databasesByUserEngine).WithArgs("user1", "postgres").WillReturnRows(dbRows("alice_pg"))

	key := generateTestKey(t)
	agent := &capturingAgent{}
	svc := NewAdminerService(NewService(db, &mockUsersForSSO{}, &mockTokensForSSO{}, agent, &key, slog.Default()), nil)

	if err := svc.syncPgShadowSchema(context.Background(), "user1", "alice"); err != nil {
		t.Fatalf("sync: %v", err)
	}
	names, _ := agent.lastParams.(map[string]interface{})["db_names"].([]string)
	if len(names) != 1 || names[0] != "alice_pg" {
		t.Fatalf("db_names = %v, want [alice_pg]", names)
	}
}

func TestSyncPgShadowSchema_ListErrorAndAgentErrorFail(t *testing.T) {
	db, mock, raw := newMembersMockDB(t)
	defer raw.Close()
	mock.ExpectQuery(databasesByUserEngine).WithArgs("user1", "postgres").WillReturnError(errors.New("db gone"))
	key := generateTestKey(t)
	agent := &capturingAgent{}
	svc := NewAdminerService(NewService(db, &mockUsersForSSO{}, &mockTokensForSSO{}, agent, &key, slog.Default()), nil)
	if err := svc.syncPgShadowSchema(context.Background(), "user1", "alice"); err == nil || agent.calls != 0 {
		t.Fatalf("list error: err=%v calls=%d, want error and no agent call", err, agent.calls)
	}

	db2, mock2, raw2 := newMembersMockDB(t)
	defer raw2.Close()
	mock2.ExpectQuery(databasesByUserEngine).WithArgs("user1", "postgres").WillReturnRows(dbRows("alice_pg"))
	failing := &failingAgent{}
	svc2 := NewAdminerService(NewService(db2, &mockUsersForSSO{}, &mockTokensForSSO{}, failing, &key, slog.Default()), nil)
	if err := svc2.syncPgShadowSchema(context.Background(), "user1", "alice"); err == nil {
		t.Fatal("agent error must fail the sync")
	}
}

func provisionedUserMock(t *testing.T, column, encColumn string) (*mockUsersForSSO, sqlmock.Sqlmock, func(), *gormDBHolder) {
	t.Helper()
	db, mock, raw := newMembersMockDB(t)
	name := "alice"
	users := &mockUsersForSSO{users: map[string]*models.User{"user1": {ID: "user1", Username: &name}}}
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .* FROM `users`").WithArgs("user1", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", column, encColumn}).AddRow("user1", "alice_x", []byte("enc")))
	mock.ExpectCommit()
	return users, mock, func() { raw.Close() }, &gormDBHolder{db: db}
}

// The open itself fails when the grant sync fails — EnsureShadow must not
// return nil and let phpMyAdmin start with a grant that may reach another
// tenant.
func TestEnsureShadow_FailsWhenGrantSyncFails(t *testing.T) {
	users, mock, done, h := provisionedUserMock(t, "mysqladmin_username", "mysqladmin_password_enc")
	defer done()
	mock.ExpectQuery(databasesByUserEngine).WithArgs("user1", "mariadb").WillReturnRows(dbRows("alice_shop"))

	key := generateTestKey(t)
	agent := &failingAgent{}
	svc := NewService(h.db, users, &mockTokensForSSO{}, agent, &key, slog.Default())
	if err := svc.EnsureShadow(context.Background(), "user1"); err == nil {
		t.Fatal("EnsureShadow must fail when db.mysqladmin.sync_grants fails")
	}
	if agent.calls != 1 {
		t.Fatalf("agent calls = %d, want 1 (the sync)", agent.calls)
	}
}

func TestEnsurePgShadow_FailsWhenDatabaseGrantSyncFails(t *testing.T) {
	users, mock, done, h := provisionedUserMock(t, "pgadmin_username", "pgadmin_password_enc")
	defer done()
	mock.ExpectQuery("SELECT .* FROM `database_users` WHERE user_id = \\? AND engine = \\?").
		WithArgs("user1", "postgres").WillReturnRows(duRows([3]string{"du1", "alice_app", "postgres"}))
	mock.ExpectQuery(databasesByUserEngine).WithArgs("user1", "postgres").WillReturnRows(dbRows("alice_pg"))

	key := generateTestKey(t)
	agent := &failingAgent{}
	svc := NewAdminerService(NewService(h.db, users, &mockTokensForSSO{}, agent, &key, slog.Default()), nil)
	if err := svc.EnsurePgShadow(context.Background(), "user1"); err == nil {
		t.Fatal("EnsurePgShadow must fail when grant_schema fails")
	}
	if agent.calls != 2 {
		t.Fatalf("agent calls = %d, want 2 (members, then schema)", agent.calls)
	}
}
