package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// drSettingsRepo is a minimal ServerSettingsRepository returning a fixed row
// (or an error, to exercise the fail-open path).
type drSettingsRepo struct {
	repository.ServerSettingsRepository
	s   *models.ServerSettings
	err error
}

func (r drSettingsRepo) Get(context.Context) (*models.ServerSettings, error) {
	return r.s, r.err
}

func newStandbyRouter(repo repository.ServerSettingsRepository) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(StandbyReadOnly(repo, nil))
	ok := func(c *gin.Context) { c.String(http.StatusOK, "ok") }
	v1.GET("/domains", ok)
	v1.POST("/domains", ok)
	v1.DELETE("/domains/:id", ok)
	v1.POST("/admin/dr/promote", ok)
	v1.PUT("/admin/settings", ok)
	return r
}

func req(t *testing.T, r *gin.Engine, method, path string) int {
	t.Helper()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader("{}")))
	return rec.Code
}

func TestStandbyGuard_PrimaryAllowsWrites(t *testing.T) {
	r := newStandbyRouter(drSettingsRepo{s: &models.ServerSettings{ServerRole: "primary"}})
	if code := req(t, r, http.MethodPost, "/api/v1/domains"); code != http.StatusOK {
		t.Errorf("primary POST should pass, got %d", code)
	}
}

func TestStandbyGuard_StandbyBlocksTenantWrites(t *testing.T) {
	r := newStandbyRouter(drSettingsRepo{s: &models.ServerSettings{ServerRole: "standby"}})
	if code := req(t, r, http.MethodPost, "/api/v1/domains"); code != http.StatusConflict {
		t.Errorf("standby POST /domains should 409, got %d", code)
	}
	if code := req(t, r, http.MethodDelete, "/api/v1/domains/x"); code != http.StatusConflict {
		t.Errorf("standby DELETE /domains should 409, got %d", code)
	}
}

func TestStandbyGuard_StandbyAllowsReads(t *testing.T) {
	r := newStandbyRouter(drSettingsRepo{s: &models.ServerSettings{ServerRole: "standby"}})
	if code := req(t, r, http.MethodGet, "/api/v1/domains"); code != http.StatusOK {
		t.Errorf("standby GET must always pass, got %d", code)
	}
}

func TestStandbyGuard_StandbyAllowsDRAndSettings(t *testing.T) {
	r := newStandbyRouter(drSettingsRepo{s: &models.ServerSettings{ServerRole: "standby"}})
	if code := req(t, r, http.MethodPost, "/api/v1/admin/dr/promote"); code != http.StatusOK {
		t.Errorf("standby must allow DR control writes (promote), got %d", code)
	}
	if code := req(t, r, http.MethodPut, "/api/v1/admin/settings"); code != http.StatusOK {
		t.Errorf("standby must allow settings writes, got %d", code)
	}
}

func TestStandbyGuard_ReadErrorFailsOpen(t *testing.T) {
	// A settings read error must NOT freeze a box — default toward active primary.
	r := newStandbyRouter(drSettingsRepo{err: errors.New("db down")})
	if code := req(t, r, http.MethodPost, "/api/v1/domains"); code != http.StatusOK {
		t.Errorf("settings read error must fail open (allow), got %d", code)
	}
}
