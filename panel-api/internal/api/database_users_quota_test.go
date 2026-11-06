package api

// JAB-329: the max_database_users cap is now configurable through Hosting
// Packages; prove the create handler enforces the CONFIGURED package value.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type quotaUsers struct {
	repository.UserRepository
	u *models.User
}

func (f *quotaUsers) FindByID(context.Context, string) (*models.User, error) { return f.u, nil }

type quotaPkgs struct {
	repository.PackageRepository
	p *models.HostingPackage
}

func (f *quotaPkgs) FindByID(context.Context, string) (*models.HostingPackage, error) {
	return f.p, nil
}

type quotaDBUsers struct {
	repository.DatabaseUserRepository
	n int64
}

func (f *quotaDBUsers) CountByUserID(context.Context, string) (int64, error) { return f.n, nil }

func quotaHandler(maxDBUsers uint32, current int64) *databaseUserHandler {
	pkgID, uname := "pkg1", "tenant"
	return &databaseUserHandler{cfg: DatabaseUserHandlerConfig{
		Users:         &quotaUsers{u: &models.User{ID: "u1", Username: &uname, PackageID: &pkgID}},
		Packages:      &quotaPkgs{p: &models.HostingPackage{ID: pkgID, MaxDatabaseUsers: maxDBUsers}},
		DatabaseUsers: &quotaDBUsers{n: current},
		// Agent left nil: at/over cap returns 409 before any agent call.
	}}
}

func postCreateDBUser(h *databaseUserHandler) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/database-users",
		strings.NewReader(`{"username":"app_user"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1"})
	h.create(c)
	return w
}

func TestDatabaseUser_Create_AtConfiguredCap_Is409(t *testing.T) {
	// The 409 must echo the CONFIGURED package value (not a constant), so two
	// different caps produce two different limits — proving the enforcement
	// reads pkg.MaxDatabaseUsers.
	for _, cap := range []uint32{2, 5} {
		w := postCreateDBUser(quotaHandler(cap, int64(cap))) // at the cap
		if w.Code != http.StatusConflict {
			t.Fatalf("cap=%d: at the configured cap want 409, got %d: %s", cap, w.Code, w.Body.String())
		}
		body := w.Body.String()
		if !strings.Contains(body, "quota_exceeded") || !strings.Contains(body, "database_users") {
			t.Fatalf("cap=%d: body should report the db-user quota; got %s", cap, body)
		}
		if !strings.Contains(body, `"limit":`+strconv.Itoa(int(cap))) {
			t.Fatalf("cap=%d: 409 must echo the configured limit; got %s", cap, body)
		}
	}
}

type errPkgs struct{ repository.PackageRepository }

func (errPkgs) FindByID(context.Context, string) (*models.HostingPackage, error) {
	return nil, errors.New("db down")
}

// A package that is set but can't be loaded refuses the create (503) before
// any agent call; it doesn't lift the cap.
func TestDatabaseUser_Create_PackageLookupError_Is503(t *testing.T) {
	h := quotaHandler(3, 0)
	h.cfg.Packages = errPkgs{}
	w := postCreateDBUser(h) // Agent nil: reaching the agent would 500
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "quota_unavailable") {
		t.Fatalf("want 503 quota_unavailable, got %d: %s", w.Code, w.Body.String())
	}
}

// raceDBUsers passes the early count and then finds the cap reached at the
// locked insert, as when another create took the last slot in between.
type raceDBUsers struct {
	quotaDBUsers
	inserted bool
}

func (*raceDBUsers) ExistsByUserAndUsername(context.Context, string, string) (bool, error) {
	return false, nil
}
func (*raceDBUsers) CountByEngineAndUsername(context.Context, string, string) (int64, error) {
	return 0, nil
}
func (*raceDBUsers) CreateWithinCap(context.Context, *models.DatabaseUser, uint32) error {
	return repository.ErrDatabaseUserCapExceeded
}
func (r *raceDBUsers) Create(context.Context, *models.DatabaseUser) error {
	r.inserted = true
	return nil
}

func TestDatabaseUser_Create_CapReachedAtInsert_Is409AndDrops(t *testing.T) {
	h := quotaHandler(3, 2)
	dbu := &raceDBUsers{quotaDBUsers: quotaDBUsers{n: 2}}
	h.cfg.DatabaseUsers = dbu
	ag := agent.NewMockClient().On("db_user.create", map[string]any{}).On("db_user.drop", map[string]any{})
	h.cfg.Agent = ag
	w := postCreateDBUser(h)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "quota_exceeded") {
		t.Fatalf("want 409 quota_exceeded, got %d: %s", w.Code, w.Body.String())
	}
	if dbu.inserted {
		t.Fatal("row inserted without the cap check")
	}
	calls := ag.Calls()
	if len(calls) != 2 || calls[0].Command != "db_user.create" || calls[1].Command != "db_user.drop" ||
		!strings.Contains(string(calls[1].Params), "tenant_app_user") {
		t.Fatalf("want create then drop of tenant_app_user, got %+v", calls)
	}
}
