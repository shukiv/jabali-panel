package sso

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	mysqldriver "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newMembersMockDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock, *sql.DB) {
	t.Helper()
	raw, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT VERSION()")).
		WillReturnRows(sqlmock.NewRows([]string{"VERSION()"}).AddRow("10.11.6-MariaDB"))
	db, err := gorm.Open(mysqldriver.New(mysqldriver.Config{Conn: raw}),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return db, mock, raw
}

// capturingAgent records the last agent Call so a test can assert the exact
// member_roles list handed to the agent.
type capturingAgent struct {
	calls       int
	lastCommand string
	lastParams  any
}

func (c *capturingAgent) Call(_ context.Context, command string, params any) (json.RawMessage, error) {
	c.calls++
	c.lastCommand = command
	c.lastParams = params
	return json.RawMessage(`{"ok":true}`), nil
}

func duRows(rows ...[3]string) *sqlmock.Rows {
	// columns: id, user_id, username, engine, password_hash, created_at, updated_at
	r := sqlmock.NewRows([]string{"id", "user_id", "username", "engine", "password_hash", "created_at", "updated_at"})
	now := time.Now()
	for _, row := range rows {
		// row = {id, username, engine}
		r.AddRow(row[0], "user1", row[1], row[2], "x", now, now)
	}
	return r
}

// GH #1406 — the membership sync must hand the agent EXACTLY the caller's own
// postgres db-user roles: no mariadb roles, nothing from another user, and the
// list must be explicit (the SQL query itself is filtered by user_id + engine).
func TestSyncPgShadowMembers_PassesOnlyOwnPostgresRoles(t *testing.T) {
	db, mock, raw := newMembersMockDB(t)
	defer raw.Close()

	// The handler filters by user_id + engine='postgres' in the query, so the
	// mock returns only the two postgres rows for user1.
	mock.ExpectQuery("SELECT .* FROM `database_users` WHERE user_id = \\? AND engine = \\?").
		WithArgs("user1", "postgres").
		WillReturnRows(duRows(
			[3]string{"du1", "alice_app", "postgres"},
			[3]string{"du2", "alice_web", "postgres"},
		))

	key := generateTestKey(t)
	agent := &capturingAgent{}
	base := NewService(db, &mockUsersForSSO{}, &mockTokensForSSO{}, agent, &key, slog.Default())
	svc := NewAdminerService(base, nil)

	svc.syncPgShadowMembers(context.Background(), "user1", "alice")

	if agent.calls != 1 {
		t.Fatalf("agent should be called once, got %d", agent.calls)
	}
	if agent.lastCommand != "db.postgres.shadowadmin.grant_members" {
		t.Fatalf("command = %q", agent.lastCommand)
	}
	params, ok := agent.lastParams.(map[string]interface{})
	if !ok {
		t.Fatalf("params type = %T", agent.lastParams)
	}
	if params["panel_username"] != "alice" {
		t.Fatalf("panel_username = %v", params["panel_username"])
	}
	roles, ok := params["member_roles"].([]string)
	if !ok {
		t.Fatalf("member_roles type = %T", params["member_roles"])
	}
	if len(roles) != 2 || roles[0] != "alice_app" || roles[1] != "alice_web" {
		t.Fatalf("member_roles = %v, want [alice_app alice_web]", roles)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// No postgres db-users → the agent is never called (nothing to grant).
func TestSyncPgShadowMembers_NoRolesSkipsAgent(t *testing.T) {
	db, mock, raw := newMembersMockDB(t)
	defer raw.Close()

	mock.ExpectQuery("SELECT .* FROM `database_users` WHERE user_id = \\? AND engine = \\?").
		WithArgs("user1", "postgres").
		WillReturnRows(duRows()) // empty

	key := generateTestKey(t)
	agent := &capturingAgent{}
	base := NewService(db, &mockUsersForSSO{}, &mockTokensForSSO{}, agent, &key, slog.Default())
	svc := NewAdminerService(base, nil)

	svc.syncPgShadowMembers(context.Background(), "user1", "alice")

	if agent.calls != 0 {
		t.Fatalf("agent must not be called when there are no postgres db-users, got %d", agent.calls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
