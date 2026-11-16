package api

import (
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #2502: webmail shows the panel's brand text and logos with every
// domain's branding. Changing them in Server Settings sends the branding to
// webmail at once, like a domain's branding; other settings send nothing.

func brandingSenderRouter(repo *lockedSettingsRepo, sender WebmailBrandingSender) *gin.Engine {
	gin.SetMode(gin.TestMode)
	mockAgent := agent.NewMockClient()
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "adm", Email: "adm@example.com", IsAdmin: true})
		c.Next()
	})
	RegisterServerSettingsRoutes(v1, ServerSettingsHandlerConfig{Repo: repo, Agent: mockAgent, Log: slog.Default(), Webmail: sender})
	RegisterBrandingRoutes(v1, BrandingHandlerConfig{Repo: repo, Log: slog.Default(), Webmail: sender})
	return r
}

func TestServerSettingsPatch_BrandTextGoesToWebmailAtOnce(t *testing.T) {
	repo := &lockedSettingsRepo{row: models.ServerSettings{ID: 1, SSHPort: 22, Timezone: "UTC", PanelBrandText: "Old"}}
	sender := &wbSender{at: time.Now()}
	r := brandingSenderRouter(repo, sender)

	code, out := serverTimeDo(t, r, http.MethodPatch, "/api/v1/admin/settings", map[string]any{"panel_brand_text": "Acme Hosting"})
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, 1, sender.calls, "a new brand text was not sent to webmail")

	code, out = serverTimeDo(t, r, http.MethodPatch, "/api/v1/admin/settings", map[string]any{"panel_brand_text": "Acme Hosting"})
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, 1, sender.calls, "an unchanged brand text was sent again (each send can restart webmail)")

	code, out = serverTimeDo(t, r, http.MethodPatch, "/api/v1/admin/settings", map[string]any{"date_format": "YYYY-MM-DD HH:mm"})
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, 1, sender.calls, "a setting webmail doesn't show was sent to webmail")
}

func TestBrandingLogoClear_GoesToWebmailAtOnce(t *testing.T) {
	repo := &lockedSettingsRepo{row: models.ServerSettings{ID: 1, SSHPort: 22, Timezone: "UTC"}}
	sender := &wbSender{at: time.Now()}
	r := brandingSenderRouter(repo, sender)

	code, out := serverTimeDo(t, r, http.MethodDelete, "/api/v1/admin/settings/branding/logo/light", nil)
	require.Equal(t, http.StatusNoContent, code, out)
	require.Equal(t, 1, sender.calls, "a removed logo was not sent to webmail")

	code, _ = serverTimeDo(t, r, http.MethodDelete, "/api/v1/admin/settings/branding/logo/sideways", nil)
	require.Equal(t, http.StatusBadRequest, code)
	require.Equal(t, 1, sender.calls, "a refused request was sent to webmail")
}

// Without a sender the changes still save.
func TestServerSettingsPatch_BrandTextWithoutSender(t *testing.T) {
	repo := &lockedSettingsRepo{row: models.ServerSettings{ID: 1, SSHPort: 22, Timezone: "UTC"}}
	r := brandingSenderRouter(repo, nil)
	code, out := serverTimeDo(t, r, http.MethodPatch, "/api/v1/admin/settings", map[string]any{"panel_brand_text": "Acme Hosting"})
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, "Acme Hosting", repo.snapshot().PanelBrandText)
}
