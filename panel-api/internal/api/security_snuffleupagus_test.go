package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type fakeSnufIncidents struct {
	repository.SnuffleupagusRepository // methods these routes don't call
	rows                               []models.SnuffleupagusIncident
	deleted                            bool
}

func (f *fakeSnufIncidents) ListIncidents(context.Context, repository.IncidentListOptions) ([]models.SnuffleupagusIncident, int64, error) {
	return f.rows, int64(len(f.rows)), nil
}

func (f *fakeSnufIncidents) DeleteAllIncidents(context.Context) (int64, error) {
	n := int64(len(f.rows))
	f.rows, f.deleted = nil, true
	return n, nil
}

func snufRouter(repo repository.SnuffleupagusRepository, admin bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "test-admin", IsAdmin: admin})
		c.Next()
	})
	api.RegisterSecuritySnuffleupagusRoutes(v1, api.SecuritySnuffleupagusConfig{Repo: repo})
	return r
}

// GH #2262: Clear history deletes the incident history and says how much.
func TestSnuffleupagusClearIncidents(t *testing.T) {
	repo := &fakeSnufIncidents{rows: make([]models.SnuffleupagusIncident, 3)}

	w := httptest.NewRecorder()
	snufRouter(repo, true).ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/v1/admin/security/snuffleupagus/incidents", nil))
	if w.Code != http.StatusOK || !repo.deleted {
		t.Fatalf("status %d deleted=%v body %s", w.Code, repo.deleted, w.Body)
	}
	var body struct{ Deleted int64 }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Deleted != 3 {
		t.Fatalf("body %s, want deleted 3", w.Body)
	}
}

func TestSnuffleupagusClearIncidents_AdminOnly(t *testing.T) {
	repo := &fakeSnufIncidents{rows: make([]models.SnuffleupagusIncident, 3)}

	w := httptest.NewRecorder()
	snufRouter(repo, false).ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/v1/admin/security/snuffleupagus/incidents", nil))
	if w.Code != http.StatusForbidden || repo.deleted {
		t.Fatalf("tenant got status %d, deleted=%v; want 403 and nothing deleted", w.Code, repo.deleted)
	}
}

// The Source column read source_ip, which the API never sent, so every
// request showed as CLI. Incidents now carry it, and the domain.
func TestSnuffleupagusListIncidents_CarriesSourceIPAndDomain(t *testing.T) {
	site := "a.test"
	repo := &fakeSnufIncidents{rows: []models.SnuffleupagusIncident{{
		ID: 1, RuleName: "sp.disabled_function:system", Action: models.SnuffleupagusActionBlock,
		SourceIP: models.SnuffleupagusSourceIP("203.0.113.9"), Domain: &site,
	}}}

	w := httptest.NewRecorder()
	snufRouter(repo, true).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/admin/security/snuffleupagus/incidents", nil))
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Data) != 1 {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	if body.Data[0]["source_ip"] != "203.0.113.9" || body.Data[0]["domain"] != "a.test" {
		t.Errorf("incident %v, want source_ip 203.0.113.9 and domain a.test", body.Data[0])
	}
}
