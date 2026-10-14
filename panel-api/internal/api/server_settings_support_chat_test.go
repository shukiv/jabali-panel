package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// Server Settings -> Support chat: two switches (admin panel, client panels)
// and a dismissed flag for the admin dashboard banner. While either switch is
// on, the panel's CSP must allow the chat's origin, which the PATCH applies
// through the agent's nginx.panel_csp.apply.

// lockedSettingsRepo hands out copies under a lock, so the handler's detached
// revert goroutine and the test never share a row.
type lockedSettingsRepo struct {
	mockServerSettingsRepo
	mu  sync.Mutex
	row models.ServerSettings
}

func (r *lockedSettingsRepo) Get(context.Context) (*models.ServerSettings, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := r.row
	return &cp, nil
}

func (r *lockedSettingsRepo) Upsert(_ context.Context, s *models.ServerSettings) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.row = *s
	return nil
}

func (r *lockedSettingsRepo) snapshot() models.ServerSettings {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.row
}

func supportChatPatch(t *testing.T, repo *lockedSettingsRepo, mockAgent *agent.MockClient, body map[string]any) map[string]any {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "test-admin", IsAdmin: true})
		c.Next()
	})
	RegisterServerSettingsRoutes(v1, ServerSettingsHandlerConfig{Repo: repo, Agent: mockAgent, Log: slog.Default()})

	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/admin/settings", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

func panelCSPCall(t *testing.T, mockAgent *agent.MockClient) map[string]any {
	t.Helper()
	var params map[string]any
	require.Eventually(t, func() bool {
		for _, c := range mockAgent.Calls() {
			if c.Command == "nginx.panel_csp.apply" {
				require.NoError(t, json.Unmarshal(c.Params, &params))
				return true
			}
		}
		return false
	}, 2*time.Second, 5*time.Millisecond, "nginx.panel_csp.apply was never dispatched")
	return params
}

func TestServerSettingsPatch_SupportChatOnAppliesTheCSP(t *testing.T) {
	repo := &lockedSettingsRepo{row: models.ServerSettings{ID: 1, SSHPort: 22}}
	mockAgent := agent.NewMockClient()
	mockAgent.On("nginx.panel_csp.apply", map[string]any{"no_change": false})

	out := supportChatPatch(t, repo, mockAgent, map[string]any{"support_chat_clients": true})
	require.Equal(t, true, out["support_chat_clients"])
	require.Equal(t, false, out["support_chat_admin"])
	require.Equal(t, map[string]any{"support_chat": true}, panelCSPCall(t, mockAgent))

	// A successful apply keeps the switch.
	time.Sleep(50 * time.Millisecond)
	require.True(t, repo.snapshot().SupportChatClients)
}

func TestServerSettingsPatch_SupportChatFailedEnableReverts(t *testing.T) {
	repo := &lockedSettingsRepo{row: models.ServerSettings{ID: 1, SSHPort: 22, Hostname: "keep.example.com"}}
	mockAgent := agent.NewMockClient()
	mockAgent.OnError("nginx.panel_csp.apply", errors.New("nginx -t failed"))

	supportChatPatch(t, repo, mockAgent, map[string]any{"support_chat_admin": true, "support_chat_clients": true})
	require.Eventually(t, func() bool {
		s := repo.snapshot()
		return !s.SupportChatAdmin && !s.SupportChatClients
	}, 2*time.Second, 5*time.Millisecond, "a failed enable must turn both switches back off")
	require.Equal(t, "keep.example.com", repo.snapshot().Hostname)
}

func TestServerSettingsPatch_SupportChatFailedDisableStaysOff(t *testing.T) {
	repo := &lockedSettingsRepo{row: models.ServerSettings{ID: 1, SSHPort: 22, SupportChatAdmin: true}}
	mockAgent := agent.NewMockClient()
	mockAgent.OnError("nginx.panel_csp.apply", errors.New("nginx -t failed"))

	supportChatPatch(t, repo, mockAgent, map[string]any{"support_chat_admin": false})
	require.Equal(t, map[string]any{"support_chat": false}, panelCSPCall(t, mockAgent))
	time.Sleep(50 * time.Millisecond)
	require.False(t, repo.snapshot().SupportChatAdmin, "a failed disable must not turn the chat back on")
}

func TestServerSettingsPatch_SupportChatNoticeDismissDoesNotTouchNginx(t *testing.T) {
	repo := &lockedSettingsRepo{row: models.ServerSettings{ID: 1, SSHPort: 22}}
	mockAgent := agent.NewMockClient()

	out := supportChatPatch(t, repo, mockAgent, map[string]any{"support_chat_notice_dismissed": true})
	require.Equal(t, true, out["support_chat_notice_dismissed"])
	require.True(t, repo.snapshot().SupportChatNoticeDismissed)

	supportChatPatch(t, repo, mockAgent, map[string]any{"admin_email": "ops@example.com"})
	time.Sleep(50 * time.Millisecond)
	require.False(t, nginxCommandDispatched(mockAgent, "nginx.panel_csp.apply"))
}

// Every signed-in user's layout reads the switches from
// /me/server-capabilities to decide whether to load the widget.
func TestServerCapabilities_SupportChatFlags(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct{ admin, clients bool }{{true, false}, {false, true}, {false, false}} {
		h := &meExtHandler{cfg: MeHandlerConfig{
			ServerSettings: &mockServerSettingsRepo{getResult: &models.ServerSettings{
				ID: 1, SupportChatAdmin: tc.admin, SupportChatClients: tc.clients,
			}},
		}}
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/me/server-capabilities", nil)
		h.serverCapabilities(c)
		require.Equal(t, http.StatusOK, rec.Code)
		var body map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, tc.admin, body["support_chat_admin"])
		require.Equal(t, tc.clients, body["support_chat_clients"])
	}
}
