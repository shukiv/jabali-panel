package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	ginctx "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type fakeNavCountsRepo struct {
	user, global repository.NavCounts
	sawGlobal    bool
}

func (f *fakeNavCountsRepo) ForUser(_ context.Context, _ string) (repository.NavCounts, error) {
	return f.user, nil
}
func (f *fakeNavCountsRepo) Global(_ context.Context) (repository.NavCounts, error) {
	f.sawGlobal = true
	return f.global, nil
}

func navCountsRouter(t *testing.T, userID string, admin bool, repo repository.NavCountsRepository) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: userID, IsAdmin: admin})
		c.Next()
	})
	RegisterNavCountsRoutes(r.Group("/api/v1"), NavCountsConfig{Counts: repo})
	return r
}

func TestNavCounts_Me(t *testing.T) {
	repo := &fakeNavCountsRepo{user: repository.NavCounts{
		WebDomains: 3, MailDomains: 2, DNSZones: 3, Databases: 5, FTPAccounts: 1, Backups: 4, CronJobs: 2,
	}}
	r := navCountsRouter(t, "u1", false, repo)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/me/nav-counts", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var got repository.NavCounts
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if got != repo.user {
		t.Errorf("me counts = %+v, want %+v", got, repo.user)
	}
}

func TestNavCounts_AdminRequiresAdmin(t *testing.T) {
	repo := &fakeNavCountsRepo{global: repository.NavCounts{WebDomains: 99}}

	// Non-admin → 403, and Global must NOT be queried.
	r := navCountsRouter(t, "u1", false, repo)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/nav-counts", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-admin want 403, got %d", w.Code)
	}
	if repo.sawGlobal {
		t.Error("Global() must not run for a non-admin caller")
	}

	// Admin → 200 + global counts.
	ra := navCountsRouter(t, "admin1", true, repo)
	wa := httptest.NewRecorder()
	ra.ServeHTTP(wa, httptest.NewRequest(http.MethodGet, "/api/v1/admin/nav-counts", nil))
	if wa.Code != http.StatusOK {
		t.Fatalf("admin want 200, got %d: %s", wa.Code, wa.Body.String())
	}
	var got repository.NavCounts
	if err := json.Unmarshal(wa.Body.Bytes(), &got); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if got.WebDomains != 99 {
		t.Errorf("admin global web_domains = %d, want 99", got.WebDomains)
	}
}
