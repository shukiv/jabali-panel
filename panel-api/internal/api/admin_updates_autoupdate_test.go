package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// stubAutoupdateRepo is an in-memory UpdateAutoupdateConfigRepository for the
// JAB-353 acknowledgement-gate tests. It starts enabled (the new default).
type stubAutoupdateRepo struct{ cfg models.UpdateAutoupdateConfig }

func newStubAutoupdate() *stubAutoupdateRepo {
	return &stubAutoupdateRepo{cfg: models.UpdateAutoupdateConfig{ID: 1, AptEnabled: true, AptTime: "03:30", JabaliTime: "04:30"}}
}
func (s *stubAutoupdateRepo) Get(context.Context) (*models.UpdateAutoupdateConfig, error) {
	c := s.cfg
	return &c, nil
}
func (s *stubAutoupdateRepo) EnsureDefault(context.Context) (*models.UpdateAutoupdateConfig, error) {
	c := s.cfg
	return &c, nil
}
func (s *stubAutoupdateRepo) Upsert(_ context.Context, c *models.UpdateAutoupdateConfig) error {
	c.ID = 1
	s.cfg = *c
	return nil
}

func newAutoupdateRouter(repo *stubAutoupdateRepo) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(injectAdminClaims(true))
	api.RegisterAdminUpdatesRoutes(v1, api.AdminUpdatesHandlerConfig{Agent: agent.NewMockClient(), Autoupdate: repo})
	return r
}

func putAutoupdate(r *gin.Engine, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPut, "/api/v1/admin/updates/autoupdate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// JAB-353: disabling OS security auto-updates without acknowledgement is
// refused, so it can never be turned off silently.
func TestAutoupdate_DisableWithoutAck_Rejected(t *testing.T) {
	repo := newStubAutoupdate()
	r := newAutoupdateRouter(repo)

	rec := putAutoupdate(r, `{"apt_enabled":false,"apt_time":"03:30","jabali_enabled":false,"jabali_time":"04:30"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "ack_required")
	// The stored config must NOT have been flipped off.
	assert.True(t, repo.cfg.AptEnabled, "apt must stay enabled when a disable is rejected")
}

// A disable WITH acknowledgement persists both the off state and the recorded
// opt-out.
func TestAutoupdate_DisableWithAck_Persists(t *testing.T) {
	repo := newStubAutoupdate()
	r := newAutoupdateRouter(repo)

	rec := putAutoupdate(r, `{"apt_enabled":false,"apt_optout_acknowledged":true,"apt_time":"03:30","jabali_enabled":false,"jabali_time":"04:30"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.False(t, repo.cfg.AptEnabled)
	assert.True(t, repo.cfg.AptOptoutAcknowledged, "an acknowledged opt-out must be recorded")
}

// Re-enabling clears the recorded opt-out (and needs no acknowledgement).
func TestAutoupdate_Enable_ClearsAck(t *testing.T) {
	repo := newStubAutoupdate()
	repo.cfg.AptEnabled = false
	repo.cfg.AptOptoutAcknowledged = true
	r := newAutoupdateRouter(repo)

	rec := putAutoupdate(r, `{"apt_enabled":true,"apt_time":"03:30","jabali_enabled":false,"jabali_time":"04:30"}`)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, repo.cfg.AptEnabled)
	assert.False(t, repo.cfg.AptOptoutAcknowledged, "re-enabling must clear the opt-out flag")
}
