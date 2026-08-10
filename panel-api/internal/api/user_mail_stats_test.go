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
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type fakeTenantStatsRepo struct {
	repository.MailStatsRepository
	gotUserID string
	rows      []repository.DomainStatSample
}

func (f *fakeTenantStatsRepo) DomainSeriesForUser(_ context.Context, _ time.Time, userID string) ([]repository.DomainStatSample, error) {
	f.gotUserID = userID
	return f.rows, nil
}

// GH #873 round 4: the tenant traffic endpoint scopes to the CALLER's domains,
// and the scope comes from the authenticated claims — there is no request
// parameter a tenant could set to see another tenant's domains.
func TestTenantMailStats_ScopedToCaller(t *testing.T) {
	t0 := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	repo := &fakeTenantStatsRepo{rows: []repository.DomainStatSample{
		{Domain: "alice.example", Metric: "sent", SampledAt: t0, Value: 7},
		{Domain: "alice.example", Metric: "received", SampledAt: t0, Value: 2},
	}}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u_alice", IsAdmin: false})
		c.Next()
	})
	api.RegisterTenantMailStatsRoutes(v1, api.TenantMailStatsHandlerConfig{Stats: repo})

	// A user param on the request must NOT change the scope.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/stats?hours=24&user_id=u_bob", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "u_alice", repo.gotUserID, "scope MUST come from claims, not the request")

	var resp struct {
		Traffic []api.DomainTrafficRow `json:"traffic"`
	}
	assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	if assert.Len(t, resp.Traffic, 1) {
		assert.Equal(t, "alice.example", resp.Traffic[0].Domain)
		assert.Equal(t, int64(7), resp.Traffic[0].Sent)
		assert.Equal(t, int64(2), resp.Traffic[0].Received)
	}
}

func TestTenantMailStats_Unauthenticated401(t *testing.T) {
	repo := &fakeTenantStatsRepo{}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1") // no claims injected
	api.RegisterTenantMailStatsRoutes(v1, api.TenantMailStatsHandlerConfig{Stats: repo})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/stats", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, repo.gotUserID, "no repo query without a caller")
}
