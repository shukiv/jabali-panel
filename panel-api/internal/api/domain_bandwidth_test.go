package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type dbwtDomains struct {
	repository.DomainRepository
	dom *models.Domain
}

func (f *dbwtDomains) FindByID(_ context.Context, id string) (*models.Domain, error) {
	if f.dom != nil && f.dom.ID == id {
		return f.dom, nil
	}
	return nil, repository.ErrNotFound
}

type dbwtBW struct {
	repository.BWDailyRepository
	countries []repository.CountryPoint
	from, to  time.Time
}

func (f *dbwtBW) SumForDomain(_ context.Context, _ string, from, to time.Time) (uint64, uint64, error) {
	f.from, f.to = from, to
	return 1000, 50, nil
}

func (f *dbwtBW) SumPerDayForDomain(context.Context, string, time.Time, time.Time) ([]repository.DailyPoint, error) {
	return nil, nil
}

func (f *dbwtBW) SumCountriesForDomain(context.Context, string, time.Time, time.Time) ([]repository.CountryPoint, error) {
	return f.countries, nil
}

func dbwtGet(t *testing.T, bw *dbwtBW, claims *auth.AccessClaims, query string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &domainHandler{cfg: DomainHandlerConfig{
		Domains: &dbwtDomains{dom: &models.Domain{ID: "d1", UserID: "alice", Name: "a.example"}},
		BWDaily: bw,
	}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/domains/d1/bandwidth"+query, nil)
	c.Params = gin.Params{{Key: "id", Value: "d1"}}
	ginctx.SetClaims(c, claims)
	h.bandwidth(c)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w, body
}

func TestDomainBandwidth_ReturnsCountries(t *testing.T) {
	bw := &dbwtBW{countries: []repository.CountryPoint{
		{CountryCode: "US", BytesTotal: 900, RequestsTotal: 40},
		{CountryCode: "DE", BytesTotal: 100, RequestsTotal: 5},
	}}
	w, body := dbwtGet(t, bw, &auth.AccessClaims{UserID: "alice"}, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	cs, _ := body["countries"].([]any)
	if len(cs) != 2 || cs[0].(map[string]any)["country_code"] != "US" {
		t.Fatalf("countries = %v", body["countries"])
	}
}

// No country rows (no GeoIP database) is an empty list, not null: the
// Statistics tab maps over it.
func TestDomainBandwidth_NoCountriesIsEmptyList(t *testing.T) {
	w, _ := dbwtGet(t, &dbwtBW{}, &auth.AccessClaims{UserID: "alice"}, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(w.Body.Bytes(), &raw)
	if string(raw["countries"]) != "[]" || string(raw["daily"]) != "[]" {
		t.Fatalf("countries = %s, daily = %s, want []", raw["countries"], raw["daily"])
	}
}

func TestDomainBandwidth_OtherTenantGets404(t *testing.T) {
	w, _ := dbwtGet(t, &dbwtBW{}, &auth.AccessClaims{UserID: "mallory"}, "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", w.Code)
	}
}

// A window longer than bwMaxWindowDays is cut to its last bwMaxWindowDays
// days.
func TestDomainBandwidth_WindowIsBounded(t *testing.T) {
	bw := &dbwtBW{}
	w, body := dbwtGet(t, bw, &auth.AccessClaims{UserID: "admin", IsAdmin: true}, "?from=2000-01-01&to=2026-10-19")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if body["from"] != "2025-10-19" || body["to"] != "2026-10-19" {
		t.Fatalf("window %v..%v, want 2025-10-19..2026-10-19", body["from"], body["to"])
	}
	if days := int(bw.to.Sub(bw.from).Hours()/24) + 1; days != bwMaxWindowDays {
		t.Fatalf("queried %d days, want %d", days, bwMaxWindowDays)
	}
}
