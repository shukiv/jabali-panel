package api

// GH #1415 — the PATCH /database-user-grants/:id path used to hand-roll its own
// privilege validation, send the request's (often empty) grant_level to the
// agent, and persist only privileges — leaving grant_level stuck at "custom".
// These tests pin the fixed behaviour: it now mirrors addGrant via
// CanonicalDBGrant, grants/revokes by the canonical privilege set, and persists
// the computed level.

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

type guAgentCall struct {
	cmd    string
	params map[string]any
}

type guCapturingAgent struct{ calls []guAgentCall }

func (a *guCapturingAgent) Call(_ context.Context, cmd string, params any) (json.RawMessage, error) {
	m, _ := params.(map[string]any)
	a.calls = append(a.calls, guAgentCall{cmd: cmd, params: m})
	return json.RawMessage(`{}`), nil
}

func (a *guCapturingAgent) find(cmd string) (map[string]any, bool) {
	for _, c := range a.calls {
		if c.cmd == cmd {
			return c.params, true
		}
	}
	return nil, false
}

type guGrantsRepo struct {
	repository.DatabaseUserGrantRepository
	grant          *models.DatabaseUserGrant
	created        *models.DatabaseUserGrant
	updatedPrivs   string
	updatedLevel   string
	updatedLevelOK bool
}

func (r *guGrantsRepo) FindByID(_ context.Context, _ string) (*models.DatabaseUserGrant, error) {
	g := *r.grant
	return &g, nil
}

func (r *guGrantsRepo) FindByDBAndDBUser(_ context.Context, _, _ string) (*models.DatabaseUserGrant, error) {
	return nil, nil // no existing grant — addGrant proceeds
}

func (r *guGrantsRepo) Create(_ context.Context, g *models.DatabaseUserGrant) error {
	r.created = g
	return nil
}

func (r *guGrantsRepo) UpdatePrivileges(_ context.Context, _, privileges string) error {
	r.updatedPrivs = privileges
	// Mirror the real repo, which stamps grant_level="custom" on a privilege
	// update regardless of the actual set.
	r.grant.Privileges = privileges
	r.grant.GrantLevel = "custom"
	return nil
}

func (r *guGrantsRepo) UpdateLevel(_ context.Context, _, level string) error {
	r.updatedLevel = level
	r.updatedLevelOK = true
	r.grant.GrantLevel = level
	return nil
}

type guDBUsersRepo struct {
	repository.DatabaseUserRepository
	du *models.DatabaseUser
}

func (r *guDBUsersRepo) FindByID(_ context.Context, _ string) (*models.DatabaseUser, error) {
	return r.du, nil
}

type guDatabasesRepo struct {
	repository.DatabaseRepository
	db *models.Database
}

func (r *guDatabasesRepo) FindByID(_ context.Context, _ string) (*models.Database, error) {
	return r.db, nil
}

func guHandler(ag *guCapturingAgent, grant *models.DatabaseUserGrant, engine string) (*databaseUserHandler, *guGrantsRepo) {
	gr := &guGrantsRepo{grant: grant}
	h := &databaseUserHandler{cfg: DatabaseUserHandlerConfig{
		DatabaseGrants: gr,
		DatabaseUsers:  &guDBUsersRepo{du: &models.DatabaseUser{ID: "du1", UserID: "u1", Username: "app_user", Engine: engine}},
		Databases:      &guDatabasesRepo{db: &models.Database{ID: "db1", UserID: "u1", Name: "app_db"}},
		Agent:          ag,
	}}
	return h, gr
}

func patchGrant(h *databaseUserHandler, body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: "g1"}}
	c.Request = httptest.NewRequest(http.MethodPatch, "/api/v1/database-user-grants/g1", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1", IsAdmin: true})
	h.updateGrant(c)
	return w
}

func postGrant(h *databaseUserHandler, body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: "du1"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/database-users/du1/grants", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1", IsAdmin: true})
	h.addGrant(c)
	return w
}

// A custom-privileges PATCH on a MariaDB grant must grant/revoke by the
// canonical privilege set — never the empty grant_level the request omits, and
// never the stored "custom" level the agent's legacy fallback rejects.
func TestUpdateGrant_MariaDB_Custom_GrantsByPrivileges(t *testing.T) {
	ag := &guCapturingAgent{}
	grant := &models.DatabaseUserGrant{ID: "g1", DatabaseID: "db1", DatabaseUserID: "du1", GrantLevel: "rw", Privileges: "ALL"}
	h, gr := guHandler(ag, grant, "mariadb")

	w := patchGrant(h, `{"privileges":["SELECT","INSERT"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}

	gp, ok := ag.find("db_user.grant")
	if !ok {
		t.Fatal("expected a db_user.grant agent call")
	}
	if _, hasLevel := gp["grant_level"]; hasLevel {
		t.Errorf("db_user.grant must not carry grant_level (the empty-level bug); params=%v", gp)
	}
	if privs, _ := gp["privileges"].([]string); strings.Join(privs, ",") != "SELECT,INSERT" {
		t.Errorf("db_user.grant privileges = %v, want [SELECT INSERT]", gp["privileges"])
	}

	rp, ok := ag.find("db_user.revoke")
	if !ok {
		t.Fatal("expected a db_user.revoke agent call")
	}
	if _, hasLevel := rp["grant_level"]; hasLevel {
		t.Errorf("db_user.revoke must not carry grant_level (rejected for custom); params=%v", rp)
	}
	if _, hasPrivs := rp["privileges"]; !hasPrivs {
		t.Errorf("db_user.revoke must carry privileges; params=%v", rp)
	}

	if gr.updatedPrivs != "SELECT,INSERT" {
		t.Errorf("persisted privileges = %q, want SELECT,INSERT", gr.updatedPrivs)
	}
	if !gr.updatedLevelOK || gr.updatedLevel != "custom" {
		t.Errorf("persisted grant_level = %q (set=%v), want custom", gr.updatedLevel, gr.updatedLevelOK)
	}
}

// A PATCH back to rw (ALL) on a currently-custom grant must persist grant_level
// "rw", not the hard-coded "custom" UpdatePrivileges alone would leave behind.
func TestUpdateGrant_MariaDB_RW_PersistsRWLevel(t *testing.T) {
	ag := &guCapturingAgent{}
	grant := &models.DatabaseUserGrant{ID: "g1", DatabaseID: "db1", DatabaseUserID: "du1", GrantLevel: "custom", Privileges: "SELECT,INSERT"}
	h, gr := guHandler(ag, grant, "mariadb")

	w := patchGrant(h, `{"grant_level":"rw"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if !gr.updatedLevelOK || gr.updatedLevel != "rw" {
		t.Errorf("persisted grant_level = %q (set=%v), want rw", gr.updatedLevel, gr.updatedLevelOK)
	}
	if gp, _ := ag.find("db_user.grant"); gp != nil {
		if privs, _ := gp["privileges"].([]string); strings.Join(privs, ",") != "ALL" {
			t.Errorf("db_user.grant privileges = %v, want [ALL]", gp["privileges"])
		}
	}
}

// A postgres grant is always full-access (the agent runs GRANT ALL), so a
// PATCH asking for a lower level must still store rw/ALL — the label can't be
// allowed to claim less access than the role actually holds (GH #1415).
func TestUpdateGrant_Postgres_CoercedToFullAccess(t *testing.T) {
	ag := &guCapturingAgent{}
	// A pre-fix row: privileges already ALL but grant_level drifted to "ro".
	grant := &models.DatabaseUserGrant{ID: "g1", DatabaseID: "db1", DatabaseUserID: "du1", GrantLevel: "ro", Privileges: "ALL"}
	h, gr := guHandler(ag, grant, "postgres")

	w := patchGrant(h, `{"grant_level":"ro"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	// Must not no-op on the stale label: the level is repaired to rw.
	if !gr.updatedLevelOK || gr.updatedLevel != "rw" {
		t.Errorf("persisted grant_level = %q (set=%v), want rw", gr.updatedLevel, gr.updatedLevelOK)
	}
	if gr.updatedPrivs != "ALL" {
		t.Errorf("persisted privileges = %q, want ALL", gr.updatedPrivs)
	}
	// Postgres uses its own grant verb, never db_user.grant.
	if _, ok := ag.find("db.postgres.grant"); !ok {
		t.Errorf("expected db.postgres.grant; calls=%v", ag.calls)
	}
}

// The same coercion on the creation path: an API caller adding a postgres grant
// with a read-only request stores full access, matching what the agent does.
func TestAddGrant_Postgres_CoercedToFullAccess(t *testing.T) {
	ag := &guCapturingAgent{}
	h, gr := guHandler(ag, &models.DatabaseUserGrant{}, "postgres")

	w := postGrant(h, `{"database_id":"db1","grant_level":"ro"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", w.Code, w.Body.String())
	}
	if gr.created == nil {
		t.Fatal("expected a grant to be created")
	}
	if gr.created.GrantLevel != "rw" || gr.created.Privileges != "ALL" {
		t.Errorf("created grant = level %q privs %q, want rw/ALL", gr.created.GrantLevel, gr.created.Privileges)
	}
	if _, ok := ag.find("db.postgres.grant"); !ok {
		t.Errorf("expected db.postgres.grant; calls=%v", ag.calls)
	}
}
