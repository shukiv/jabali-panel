package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1411: the public /branding endpoint must surface the operator-
// configured panel hostname so the login page can detect IP/wrong-host
// access and offer a one-click link to the correct host. It must be
// normalised (lowercase, no trailing dot) and empty when unset.
func TestPublicBrandingPanelHostname(t *testing.T) {
	gin.SetMode(gin.TestMode)

	fetch := func(t *testing.T, repo repository.ServerSettingsRepository) map[string]any {
		t.Helper()
		r := gin.New()
		RegisterPublicBrandingRoutes(r.Group("/"), BrandingHandlerConfig{Repo: repo})
		req := httptest.NewRequest(http.MethodGet, "/branding", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return out
	}

	t.Run("normalises hostname", func(t *testing.T) {
		out := fetch(t, &mockServerSettingsRepo{
			getResult: &models.ServerSettings{Hostname: "Panel.Example.COM."},
		})
		if got := out["panel_hostname"]; got != "panel.example.com" {
			t.Fatalf("panel_hostname = %v, want panel.example.com", got)
		}
	})

	t.Run("empty when unset", func(t *testing.T) {
		out := fetch(t, &mockServerSettingsRepo{
			getResult: &models.ServerSettings{Hostname: ""},
		})
		if got := out["panel_hostname"]; got != "" {
			t.Fatalf("panel_hostname = %v, want empty", got)
		}
	})

	t.Run("empty when settings row absent", func(t *testing.T) {
		out := fetch(t, &mockServerSettingsRepo{getErr: repository.ErrNotFound})
		if got := out["panel_hostname"]; got != "" {
			t.Fatalf("panel_hostname = %v, want empty on ErrNotFound", got)
		}
	})
}
