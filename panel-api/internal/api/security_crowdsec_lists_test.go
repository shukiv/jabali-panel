package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func crowdsecListsRouter(cli agent.AgentInterface, repo *mockServerSettingsRepo, admin bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "u1", IsAdmin: admin})
		c.Next()
	})
	RegisterSecurityCrowdSecRoutes(v1, cli, repo)
	return r
}

func feedsDo(r *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

const feedsPath = "/api/v1/admin/security/crowdsec/blocklists/feeds"

// PUT saves the set in catalog order and GET shows it with the agent's last
// run; nothing is sent to the agent until the reconciler runs.
func TestCrowdsecListFeeds_SaveAndShow(t *testing.T) {
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1, SSHPort: 22}}
	m := agent.NewMockClient().On("security.crowdsec.lists.status", map[string]any{"running": false, "lists": []any{}})
	r := crowdsecListsRouter(m, repo, true)

	rec := feedsDo(r, http.MethodPut, feedsPath, map[string]any{"enabled": []string{"firehol_level3", "firehol_level1"}})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "firehol_level1,firehol_level3", repo.getResult.CrowdsecBlocklists)
	for _, c := range m.Calls() {
		require.NotEqual(t, "security.crowdsec.lists.apply", c.Command, "PUT must leave the apply to the reconciler")
	}

	rec = feedsDo(r, http.MethodGet, feedsPath, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var got struct {
		Lists []struct {
			Key     string `json:"key"`
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
			URL     string `json:"url"`
		} `json:"lists"`
		Status map[string]any `json:"status"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Len(t, got.Lists, 3)
	require.True(t, got.Lists[0].Enabled)
	require.False(t, got.Lists[1].Enabled)
	require.True(t, got.Lists[2].Enabled)
	require.Empty(t, got.Lists[0].URL, "the fetch URL stays on the server")
	require.NotNil(t, got.Status)
}

func TestCrowdsecListFeeds_RefusesUnknownList(t *testing.T) {
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1, SSHPort: 22, CrowdsecBlocklists: "firehol_level1"}}
	r := crowdsecListsRouter(agent.NewMockClient(), repo, true)
	rec := feedsDo(r, http.MethodPut, feedsPath, map[string]any{"enabled": []string{"https://evil.example/list.txt"}})
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "invalid_blocklists")
	require.Equal(t, "firehol_level1", repo.getResult.CrowdsecBlocklists)
}

// Refresh sends the saved set and the public IPs straight to the agent.
func TestCrowdsecListFeeds_RefreshSendsSavedSet(t *testing.T) {
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1, SSHPort: 22,
		CrowdsecBlocklists: "firehol_level2,removed_list", PublicIPv4: "185.10.20.30", PublicIPv6: "not-an-ip"}}
	m := agent.NewMockClient().On("security.crowdsec.lists.apply", map[string]any{"queued": true})
	r := crowdsecListsRouter(m, repo, true)
	rec := feedsDo(r, http.MethodPost, feedsPath+"/refresh", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	calls := m.Calls()
	require.Len(t, calls, 1)
	require.JSONEq(t, `{"enabled":["firehol_level2"],"protect":["185.10.20.30"]}`, string(calls[0].Params))
}

func TestCrowdsecListFeeds_AdminOnly(t *testing.T) {
	repo := &mockServerSettingsRepo{getResult: &models.ServerSettings{ID: 1, SSHPort: 22}}
	r := crowdsecListsRouter(agent.NewMockClient(), repo, false)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, feedsPath}, {http.MethodPut, feedsPath}, {http.MethodPost, feedsPath + "/refresh"},
	} {
		rec := feedsDo(r, c.method, c.path, map[string]any{"enabled": []string{}})
		require.Equal(t, http.StatusForbidden, rec.Code, "%s %s", c.method, c.path)
	}
	require.Equal(t, "", repo.getResult.CrowdsecBlocklists)
}
