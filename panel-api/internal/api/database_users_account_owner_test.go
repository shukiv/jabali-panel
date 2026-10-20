package api

// A database account is server-wide, and tenant "alice" asking for "x_app"
// names the same account as tenant "alice_x" asking for "app". Create must
// refuse a name another row has (and the sign-in accounts' names), and a row
// that does not alone name its account may not rotate or drop the account.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type ownerDBUsers struct {
	repository.DatabaseUserRepository
	rows    map[string]int64 // "<engine>|<username>" → rows naming the account
	row     *models.DatabaseUser
	created *models.DatabaseUser
	deleted string
	rotated string
}

func (f *ownerDBUsers) ExistsByUserAndUsername(context.Context, string, string) (bool, error) {
	return false, nil
}
func (f *ownerDBUsers) CountByUserID(context.Context, string) (int64, error) { return 0, nil }
func (f *ownerDBUsers) CountByEngineAndUsername(_ context.Context, engine, username string) (int64, error) {
	return f.rows[engine+"|"+username], nil
}
func (f *ownerDBUsers) Create(_ context.Context, du *models.DatabaseUser) error {
	f.created = du
	return nil
}
func (f *ownerDBUsers) FindByID(context.Context, string) (*models.DatabaseUser, error) {
	return f.row, nil
}
func (f *ownerDBUsers) Delete(_ context.Context, id string) error {
	f.deleted = id
	return nil
}
func (f *ownerDBUsers) UpdatePasswordHash(_ context.Context, id, _ string) error {
	f.rotated = id
	return nil
}

type ownerGrants struct {
	repository.DatabaseUserGrantRepository
}

func (ownerGrants) ListByDatabaseUserID(context.Context, string) ([]models.DatabaseUserGrant, error) {
	return nil, nil
}

type ownerAgent struct {
	calls  []string
	params []map[string]any
}

func (a *ownerAgent) Call(_ context.Context, cmd string, p any) (json.RawMessage, error) {
	a.calls = append(a.calls, cmd)
	m, _ := p.(map[string]any)
	a.params = append(a.params, m)
	return json.RawMessage(`{}`), nil
}

func ownerHandler(rows *ownerDBUsers, ag *ownerAgent) *databaseUserHandler {
	uname := "alice"
	return &databaseUserHandler{cfg: DatabaseUserHandlerConfig{
		Users:          &quotaUsers{u: &models.User{ID: "u1", Username: &uname}},
		DatabaseUsers:  rows,
		DatabaseGrants: ownerGrants{},
		Agent:          ag,
	}}
}

func ownerRequest(method, path, body string, run func(*gin.Context)) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: "du1"}}
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
	run(c)
	return w
}

func TestDatabaseUser_Create_RefusesAnotherTenantsAccount(t *testing.T) {
	// Tenant alice_x already has alice_x_app; alice asks for x_app.
	rows := &ownerDBUsers{rows: map[string]int64{"mariadb|alice_x_app": 1}}
	ag := &ownerAgent{}
	h := ownerHandler(rows, ag)
	w := ownerRequest(http.MethodPost, "/api/v1/database-users", `{"username":"x_app"}`, h.create)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "username_exists") {
		t.Fatalf("want 409 username_exists, got %d: %s", w.Code, w.Body.String())
	}
	if len(ag.calls) != 0 || rows.created != nil {
		t.Fatalf("nothing may be created: agent calls %v, row %v", ag.calls, rows.created)
	}
}

func TestDatabaseUser_Create_RefusesSignInAccountNames(t *testing.T) {
	for _, name := range []string{"x_mysqladmin", "x_pgadmin"} {
		rows := &ownerDBUsers{}
		ag := &ownerAgent{}
		h := ownerHandler(rows, ag)
		w := ownerRequest(http.MethodPost, "/api/v1/database-users", `{"username":"`+name+`"}`, h.create)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "reserved") {
			t.Fatalf("%s: want 400 reserved, got %d: %s", name, w.Code, w.Body.String())
		}
		if len(ag.calls) != 0 {
			t.Fatalf("%s: agent called: %v", name, ag.calls)
		}
	}
}

func TestDatabaseUser_Create_AsksTheAgentNotToAdoptAnAccount(t *testing.T) {
	rows := &ownerDBUsers{}
	ag := &ownerAgent{}
	h := ownerHandler(rows, ag)
	w := ownerRequest(http.MethodPost, "/api/v1/database-users", `{"username":"app"}`, h.create)
	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", w.Code, w.Body.String())
	}
	if len(ag.calls) != 1 || ag.calls[0] != "db_user.create" || ag.params[0]["create_only"] != true {
		t.Fatalf("want db_user.create with create_only, got %v %v", ag.calls, ag.params)
	}
}

func TestDatabaseUser_Rotate_RefusesASharedAccount(t *testing.T) {
	rows := &ownerDBUsers{
		rows: map[string]int64{"mariadb|alice_x_app": 2},
		row:  &models.DatabaseUser{ID: "du1", UserID: "u1", Username: "alice_x_app", Engine: "mariadb"},
	}
	ag := &ownerAgent{}
	h := ownerHandler(rows, ag)
	w := ownerRequest(http.MethodPost, "/api/v1/database-users/du1/rotate-password", `{"new_password":"Placeholder-Pw-123456"}`, h.rotatePassword)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "account_shared") {
		t.Fatalf("want 409 account_shared, got %d: %s", w.Code, w.Body.String())
	}
	if len(ag.calls) != 0 || rows.rotated != "" {
		t.Fatalf("the password must not change: agent %v, row %q", ag.calls, rows.rotated)
	}
}

func TestDatabaseUser_Delete_SharedAccountDropsOnlyTheRow(t *testing.T) {
	for _, c := range []struct{ name, username string }{
		{"two rows", "alice_x_app"},
		{"sign-in account name", "alice_x_mysqladmin"},
	} {
		rows := &ownerDBUsers{
			rows: map[string]int64{"mariadb|alice_x_app": 2, "mariadb|alice_x_mysqladmin": 1},
			row:  &models.DatabaseUser{ID: "du1", UserID: "u1", Username: c.username, Engine: "mariadb"},
		}
		ag := &ownerAgent{}
		h := ownerHandler(rows, ag)
		w := ownerRequest(http.MethodDelete, "/api/v1/database-users/du1", ``, h.delete)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: want 200, got %d: %s", c.name, w.Code, w.Body.String())
		}
		for _, call := range ag.calls {
			if call == "db_user.drop" {
				t.Fatalf("%s: the account was dropped: %v", c.name, ag.calls)
			}
		}
		if rows.deleted != "du1" {
			t.Fatalf("%s: the row should be deleted, got %q", c.name, rows.deleted)
		}
	}
}

func TestDatabaseUser_Delete_SoleRowDropsTheAccount(t *testing.T) {
	rows := &ownerDBUsers{
		rows: map[string]int64{"mariadb|alice_app": 1},
		row:  &models.DatabaseUser{ID: "du1", UserID: "u1", Username: "alice_app", Engine: "mariadb"},
	}
	ag := &ownerAgent{}
	h := ownerHandler(rows, ag)
	if w := ownerRequest(http.MethodDelete, "/api/v1/database-users/du1", ``, h.delete); w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(ag.calls) != 1 || ag.calls[0] != "db_user.drop" {
		t.Fatalf("want the account dropped, got %v", ag.calls)
	}
}

type provDBs struct {
	repository.DatabaseRepository
	deleted []string
}

func (r *provDBs) ExistsByUserAndName(context.Context, string, string) (bool, error) {
	return false, nil
}
func (r *provDBs) Create(context.Context, *models.Database) error { return nil }
func (r *provDBs) Delete(_ context.Context, id string) error {
	r.deleted = append(r.deleted, id)
	return nil
}

type provGrants struct {
	repository.DatabaseUserGrantRepository
}

func (provGrants) Create(context.Context, *models.DatabaseUserGrant) error { return nil }
func (provGrants) Delete(context.Context, string) error                    { return nil }

// An app install whose login create is refused (create_only: the account
// exists, possibly another tenant's) must not drop that login while it
// unwinds; only the database it made goes.
func TestProvisionDBChain_RefusedLoginIsNeverDropped(t *testing.T) {
	ag := &dropFailAgent{failCmds: map[string]bool{"db_user.create": true}}
	dbs := &provDBs{}
	rows := &ownerDBUsers{}
	cfg := ApplicationHandlerConfig{Databases: dbs, DatabaseUsers: rows, DatabaseGrants: provGrants{}, Agent: ag}
	if _, err := provisionDBChain(context.Background(), cfg, "u1", "alice", "wordpress", "pw-123456"); err == nil {
		t.Fatal("expected the refused create to fail the install")
	}
	if ag.called("db_user.drop") {
		t.Fatalf("the refused login was dropped: %v", ag.calls)
	}
	if !ag.called("db.drop") || len(dbs.deleted) != 1 || rows.deleted == "" {
		t.Fatalf("the database and rows this call made should go: calls %v, db rows %v, user row %q", ag.calls, dbs.deleted, rows.deleted)
	}
}
