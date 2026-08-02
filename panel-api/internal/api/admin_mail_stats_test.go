package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type fakeMailStatsSeriesRepo struct {
	repository.MailStatsRepository
	rows    []repository.MailStatSample
	storage []repository.MailboxStorageRow
}

func (f *fakeMailStatsSeriesRepo) Series(context.Context, time.Time) ([]repository.MailStatSample, error) {
	return f.rows, nil
}
func (f *fakeMailStatsSeriesRepo) StorageDrilldown(context.Context) ([]repository.MailboxStorageRow, error) {
	return f.storage, nil
}

// GH #873: rates are positive deltas per metric, clamped at zero across a
// Stalwart-restart counter reset; points and current carry raw values;
// the storage drilldown rides along.
func TestAdminMailStats_SeriesAndDeltaClamp(t *testing.T) {
	t0 := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	repo := &fakeMailStatsSeriesRepo{
		rows: []repository.MailStatSample{
			{Metric: "message_ingest", SampledAt: t0, Value: 100},
			{Metric: "message_ingest", SampledAt: t0.Add(15 * time.Minute), Value: 130},
			{Metric: "message_ingest", SampledAt: t0.Add(30 * time.Minute), Value: 10}, // restart reset
			{Metric: "message_ingest", SampledAt: t0.Add(45 * time.Minute), Value: 25},
			{Metric: "queue_size", SampledAt: t0, Value: 4},
		},
		storage: []repository.MailboxStorageRow{
			{Username: "notary", DomainName: "n.example", Email: "a@n.example", UsageBytes: 1024, QuotaBytes: 4096},
		},
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(injectAdminClaims(true))
	api.RegisterAdminMailStatsRoutes(v1, api.AdminMailStatsHandlerConfig{Stats: repo})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/mail/stats?hours=24", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp struct {
		Points  map[string][]api.MailStatPoint `json:"points"`
		Rates   map[string][]api.MailStatPoint `json:"rates"`
		Current map[string]float64             `json:"current"`
		Storage []repository.MailboxStorageRow `json:"storage"`
	}
	assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	assert.Len(t, resp.Points["message_ingest"], 4)
	rates := resp.Rates["message_ingest"]
	if assert.Len(t, rates, 3) {
		assert.Equal(t, float64(30), rates[0].V)
		assert.Equal(t, float64(0), rates[1].V, "counter reset must clamp to 0, not go negative")
		assert.Equal(t, float64(15), rates[2].V)
	}
	assert.Equal(t, float64(25), resp.Current["message_ingest"])
	assert.Equal(t, float64(4), resp.Current["queue_size"])
	if assert.Len(t, resp.Storage, 1) {
		assert.Equal(t, "a@n.example", resp.Storage[0].Email)
	}
}
