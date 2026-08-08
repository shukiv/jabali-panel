package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #331: the Automation API mounts outside the auth-protected v1 group, so it
// carries its own StandbyReadOnly guard (app.go). A standby must refuse an
// automation WRITE — otherwise a headless provisioning call would diverge from
// the primary and be discarded by the next sync. Reads still pass (the guard
// only blocks mutating methods).
func awStandbyRouter(t *testing.T, role string, cfg AutomationConfig) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	grp := r.Group("")
	grp.Use(middleware.StandbyReadOnly(
		&mockServerSettingsRepo{getResult: &models.ServerSettings{ServerRole: role}}, nil))
	grp.Use(func(c *gin.Context) { c.Set("jabali_automation_token", writeTok()); c.Next() })
	registerAutomationWrites(grp, cfg)
	return r
}

func TestAutomationWrite_StandbyRefuses(t *testing.T) {
	ag := &awAgent{}
	r := awStandbyRouter(t, models.ServerRoleStandby, AutomationConfig{Agent: ag, Audits: &awAudit{}})
	w := awPost(r, "/services/nginx/restart")
	if w.Code != http.StatusConflict {
		t.Fatalf("standby must 409 an automation write, got %d: %s", w.Code, w.Body.String())
	}
	if len(ag.calls) != 0 {
		t.Fatalf("standby must not reach the handler (no agent call), got %v", ag.calls)
	}
}

func TestAutomationWrite_PrimaryAllows(t *testing.T) {
	ag := &awAgent{}
	r := awStandbyRouter(t, models.ServerRolePrimary, AutomationConfig{Agent: ag, Audits: &awAudit{}})
	if code := awPost(r, "/services/nginx/restart").Code; code != http.StatusOK {
		t.Fatalf("primary must pass the automation write, got %d", code)
	}
	if len(ag.calls) != 1 {
		t.Fatalf("primary must reach the handler, agent calls = %v", ag.calls)
	}
}

// A read-only automation call is allowed even on a standby.
func TestAutomationRead_StandbyAllows(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	grp := r.Group("")
	grp.Use(middleware.StandbyReadOnly(
		&mockServerSettingsRepo{getResult: &models.ServerSettings{ServerRole: models.ServerRoleStandby}}, nil))
	hit := false
	grp.GET("/status/health", func(c *gin.Context) { hit = true; c.Status(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/status/health", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !hit {
		t.Fatalf("standby must allow an automation READ, got %d hit=%v", w.Code, hit)
	}
}
