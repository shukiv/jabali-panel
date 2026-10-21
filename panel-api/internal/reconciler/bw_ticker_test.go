package reconciler

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"slices"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// The bandwidth.scan_day wire contract. The agent's handler test
// (panel-agent bandwidth_scan_geo_test.go) decodes the same fixture.
func TestContract_BandwidthScanDayResponse(t *testing.T) {
	pgReownRoundTrip[bwScanResponse](t, "../agent/testdata/bandwidth_scan_day_response.json")
}

type bwtDomains struct {
	repository.DomainRepository
	byName map[string]*models.Domain
}

func (f *bwtDomains) FindByName(_ context.Context, name string) (*models.Domain, error) {
	if d, ok := f.byName[name]; ok {
		return d, nil
	}
	return nil, repository.ErrNotFound
}

type bwtRepo struct {
	repository.BWDailyRepository
	upserts   []models.BWDaily
	countries map[string][]models.BWDailyCountry
}

func (f *bwtRepo) Upsert(_ context.Context, row *models.BWDaily) error {
	f.upserts = append(f.upserts, *row)
	return nil
}

func (f *bwtRepo) ReplaceCountriesForDay(_ context.Context, domainID string, _ time.Time, rows []models.BWDailyCountry) error {
	f.countries[domainID] = rows
	return nil
}

// The scan stores each domain's day and, when the agent sent them, the day's
// countries. A domain without countries keeps whatever rows it had, and a
// code that is not two upper-case letters is dropped.
func TestScanBandwidth_StoresCountries(t *testing.T) {
	raw, err := os.ReadFile("../agent/testdata/bandwidth_scan_day_response.json")
	if err != nil {
		t.Fatal(err)
	}
	var resp map[string]any
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	first := resp["stats"].([]any)[0].(map[string]any)
	first["countries"] = append(first["countries"].([]any),
		map[string]any{"code": "<script>", "requests_total": 1, "bytes_total": 1})
	payload, _ := json.Marshal(resp)

	ag := &fakeAgent{resultByMethod: map[string]json.RawMessage{"bandwidth.scan_day": payload}}
	doms := &bwtDomains{byName: map[string]*models.Domain{
		"example.com":      {ID: "d-geo", Name: "example.com"},
		"no-geoip.example": {ID: "d-plain", Name: "no-geoip.example"},
	}}
	bw := &bwtRepo{countries: map[string][]models.BWDailyCountry{}}

	scanBandwidth(context.Background(), ag, doms, bw, slog.Default())

	if len(bw.upserts) != 2 {
		t.Fatalf("upserts = %+v, want both domains", bw.upserts)
	}
	codes := []string{}
	for _, c := range bw.countries["d-geo"] {
		codes = append(codes, c.CountryCode)
	}
	if !slices.Equal(codes, []string{"US", "BE", "ZZ"}) {
		t.Fatalf("stored countries %v, want [US BE ZZ]", codes)
	}
	if got := bw.countries["d-geo"][0]; got.RequestsTotal != 489 || got.BytesTotal != 2658828 {
		t.Fatalf("US row = %+v", got)
	}
	if _, ok := bw.countries["d-plain"]; ok {
		t.Fatal("a domain without countries had its country rows replaced")
	}
}
