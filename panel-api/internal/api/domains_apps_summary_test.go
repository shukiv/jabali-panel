package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// mockAppInstallsForSummary embeds the repo interface (auto-satisfying the
// methods the list handler never calls) and implements only ListByDomainIDs,
// the one the GH #1543 domains-list app summary uses.
type mockAppInstallsForSummary struct {
	repository.ApplicationInstallRepository
	byDomain map[string][]models.ApplicationInstall
}

func (m *mockAppInstallsForSummary) ListByDomainIDs(_ context.Context, ids []string) ([]models.ApplicationInstall, error) {
	var out []models.ApplicationInstall
	for _, id := range ids {
		out = append(out, m.byDomain[id]...)
	}
	return out, nil
}

// TestDomainList_EmbedsAppSummary asserts GET /domains denormalizes each
// domain's One-Click installs onto the row: a domain with a docroot + subdir
// install gets both (docroot first), and a domain with none ships no key.
func TestDomainList_EmbedsAppSummary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1", IsAdmin: true})
		c.Next()
	})

	base := newMockDomainRepo()
	base.Create(context.Background(), &models.Domain{ID: "d1", UserID: "u1", Name: "a.com"})
	base.Create(context.Background(), &models.Domain{ID: "d2", UserID: "u1", Name: "b.com"})
	domains := &domainListWithSeedData{mockDomainRepo: base}

	apps := &mockAppInstallsForSummary{byDomain: map[string][]models.ApplicationInstall{
		"d1": {
			{ID: "i_root", DomainID: "d1", AppType: "wordpress", Version: strptr("6.5.3"), Status: "ready", Subdirectory: ""},
			{ID: "i_blog", DomainID: "d1", AppType: "wordpress", Version: strptr("6.5.3"), Status: "installing", Subdirectory: "blog"},
		},
		// d2 has no installs.
	}}

	RegisterDomainRoutes(v1, DomainHandlerConfig{Domains: domains, AppInstalls: apps})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/domains", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Data []struct {
			ID           string `json:"id"`
			Applications []struct {
				ID           string  `json:"id"`
				AppType      string  `json:"app_type"`
				Version      *string `json:"version"`
				Status       string  `json:"status"`
				Subdirectory string  `json:"subdirectory"`
			} `json:"applications"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	byID := map[string]int{}
	for i, row := range resp.Data {
		byID[row.ID] = i
	}
	d1 := resp.Data[byID["d1"]]
	if len(d1.Applications) != 2 {
		t.Fatalf("d1 apps: got %d want 2", len(d1.Applications))
	}
	if d1.Applications[0].Subdirectory != "" {
		t.Errorf("d1 docroot install must sort first, got subdir %q", d1.Applications[0].Subdirectory)
	}
	if d1.Applications[0].AppType != "wordpress" || d1.Applications[0].Version == nil || *d1.Applications[0].Version != "6.5.3" {
		t.Errorf("d1 docroot summary wrong: %+v", d1.Applications[0])
	}
	if d1.Applications[1].Status != "installing" {
		t.Errorf("d1 subdir status: got %q want installing", d1.Applications[1].Status)
	}
	if len(resp.Data[byID["d2"]].Applications) != 0 {
		t.Errorf("d2 must ship no applications, got %+v", resp.Data[byID["d2"]].Applications)
	}
}

// TestDomainList_AppSummaryAbsentWhenUnwired: with no AppInstalls repo wired,
// the list still serves and simply omits the applications field (older clients
// and installs-off deployments stay happy).
func TestDomainList_AppSummaryAbsentWhenUnwired(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1", IsAdmin: true})
		c.Next()
	})

	base := newMockDomainRepo()
	base.Create(context.Background(), &models.Domain{ID: "d1", UserID: "u1", Name: "a.com"})
	domains := &domainListWithSeedData{mockDomainRepo: base}

	RegisterDomainRoutes(v1, DomainHandlerConfig{Domains: domains})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/domains", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200, body=%s", w.Code, w.Body.String())
	}
	// Raw body must not carry an "applications" key when unwired.
	if body := w.Body.String(); contains(body, "\"applications\"") {
		t.Errorf("applications key must be absent when AppInstalls unwired; body=%s", body)
	}
}
