package api

// JAB-329: the max_database_users cap is now configurable through Hosting
// Packages; prove the create handler enforces the CONFIGURED package value.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

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
