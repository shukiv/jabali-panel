package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	_ "time/tzdata"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/paneltime"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #2164: the panel shows every time in the Server Settings timezone, with
// the date format the admin picks next to it, and hands both to every
// signed-in user on GET /me.

func serverTimeRouter(repo *lockedSettingsRepo, isAdmin bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	mockAgent := agent.NewMockClient()
	mockAgent.On("system.set_timezone", map[string]any{})
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1", Email: "u1@example.com", IsAdmin: isAdmin})
		c.Next()
	})
	RegisterServerSettingsRoutes(v1, ServerSettingsHandlerConfig{Repo: repo, Agent: mockAgent, Log: slog.Default()})
	v1.GET("/me", meHandlerWithConfig(MeHandlerConfig{ServerSettings: repo}))
	return r
}

func serverTimeDo(t *testing.T, r *gin.Engine, method, path string, body map[string]any) (int, map[string]any) {
	t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestServerSettingsPatch_DateFormatIsOneOfThePresets(t *testing.T) {
	t.Cleanup(func() { paneltime.Set("UTC") })
	repo := &lockedSettingsRepo{row: models.ServerSettings{ID: 1, SSHPort: 22, Timezone: "UTC"}}
	r := serverTimeRouter(repo, true)

	code, out := serverTimeDo(t, r, http.MethodPatch, "/api/v1/admin/settings", map[string]any{"date_format": "YYYY-MM-DD HH:mm"})
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, "YYYY-MM-DD HH:mm", out["date_format"])
	require.Equal(t, "YYYY-MM-DD HH:mm", repo.snapshot().DateFormat)

	for _, bad := range []string{"", "%Y-%m-%d", "DD/MM/YY HH:mm[<script>]", "dd/mm/yy"} {
		code, out = serverTimeDo(t, r, http.MethodPatch, "/api/v1/admin/settings", map[string]any{"date_format": bad})
		require.Equal(t, http.StatusBadRequest, code, "date_format %q: %v", bad, out)
		require.Equal(t, "invalid_settings", out["error"])
		require.Equal(t, "YYYY-MM-DD HH:mm", repo.snapshot().DateFormat, "a refused format must not be saved")
	}
}

func TestServerSettingsPatch_TimezoneMustBeAZoneThisHostKnows(t *testing.T) {
	t.Cleanup(func() { paneltime.Set("UTC") })
	repo := &lockedSettingsRepo{row: models.ServerSettings{ID: 1, SSHPort: 22, Timezone: "UTC"}}
	r := serverTimeRouter(repo, true)

	// Well-formed (passes settingsops' name check) but not a zone.
	code, out := serverTimeDo(t, r, http.MethodPatch, "/api/v1/admin/settings", map[string]any{"timezone": "Mars/Olympus"})
	require.Equal(t, http.StatusBadRequest, code, out)
	require.Equal(t, "UTC", repo.snapshot().Timezone)

	code, out = serverTimeDo(t, r, http.MethodPatch, "/api/v1/admin/settings", map[string]any{"timezone": "Asia/Tokyo"})
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, "Asia/Tokyo", repo.snapshot().Timezone)
	require.Equal(t, "Asia/Tokyo", paneltime.Name(), "the panel clock follows the saved zone at once")
}

// GH #2320: a browser lists India as Asia/Calcutta, a name Debian 13 and
// Ubuntu 24.04 have no zone file for (timedatectl refuses it). The zone is
// saved, and handed to the OS, by its current name.
func TestServerSettingsPatch_OldZoneNameIsSavedByItsCurrentName(t *testing.T) {
	t.Cleanup(func() { paneltime.Set("UTC") })
	repo := &lockedSettingsRepo{row: models.ServerSettings{ID: 1, SSHPort: 22, Timezone: "UTC"}}
	r := serverTimeRouter(repo, true)

	code, out := serverTimeDo(t, r, http.MethodPatch, "/api/v1/admin/settings", map[string]any{"timezone": "Asia/Calcutta"})
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, "Asia/Kolkata", out["timezone"])
	require.Equal(t, "Asia/Kolkata", repo.snapshot().Timezone)
	require.Equal(t, "Asia/Kolkata", paneltime.Name())
}

// A stored zone this host no longer loads must not block saving the rest of
// Server Settings: the General tab sends the timezone back with every save.
func TestServerSettingsPatch_UnchangedStoredZoneIsNotRechecked(t *testing.T) {
	t.Cleanup(func() { paneltime.Set("UTC") })
	repo := &lockedSettingsRepo{row: models.ServerSettings{ID: 1, SSHPort: 22, Timezone: "Mars/Olympus"}}
	r := serverTimeRouter(repo, true)

	code, out := serverTimeDo(t, r, http.MethodPatch, "/api/v1/admin/settings", map[string]any{"timezone": "Mars/Olympus", "admin_email": "ops@example.com"})
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, "ops@example.com", repo.snapshot().AdminEmail)
}

func TestServerSettingsGet_DateFormatDefault(t *testing.T) {
	repo := &lockedSettingsRepo{row: models.ServerSettings{ID: 1, SSHPort: 22, Timezone: "UTC"}}
	r := serverTimeRouter(repo, true)

	code, out := serverTimeDo(t, r, http.MethodGet, "/api/v1/admin/settings", nil)
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, "DD/MM/YY HH:mm", out["date_format"], "an empty column shows the default")

	repo.row.DateFormat = "MM/DD/YYYY hh:mm A"
	_, out = serverTimeDo(t, r, http.MethodGet, "/api/v1/admin/settings", nil)
	require.Equal(t, "MM/DD/YYYY hh:mm A", out["date_format"])
}

// Every signed-in user, not only admins, gets the panel's clock on GET /me.
func TestMe_CarriesThePanelClock(t *testing.T) {
	t.Cleanup(func() { paneltime.Set("UTC") })
	paneltime.Set("Europe/Berlin")
	repo := &lockedSettingsRepo{row: models.ServerSettings{ID: 1, SSHPort: 22, Timezone: "Europe/Berlin", DateFormat: "DD.MM.YYYY HH:mm"}}
	r := serverTimeRouter(repo, false)

	code, out := serverTimeDo(t, r, http.MethodGet, "/api/v1/me", nil)
	require.Equal(t, http.StatusOK, code, out)
	require.Equal(t, "Europe/Berlin", out["timezone"])
	require.Equal(t, "DD.MM.YYYY HH:mm", out["date_format"])

	// A format this release doesn't know reads as the default.
	repo.row.DateFormat = "from-a-newer-release"
	_, out = serverTimeDo(t, r, http.MethodGet, "/api/v1/me", nil)
	require.Equal(t, "DD/MM/YY HH:mm", out["date_format"])
}
