package api

import (
	"encoding/json"
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

func newIPRouterWithAgent(repo *fakeIPRepo, a agent.AgentInterface) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		ginctx.SetClaims(c, &auth.AccessClaims{UserID: "test-admin", IsAdmin: true})
		c.Next()
	})
	RegisterIPRoutes(v1, IPHandlerConfig{Repo: repo, Agent: a, Log: slog.Default()})
	return r
}

func defaultListenCalls(a *agent.MockClient) []map[string]any {
	var out []map[string]any
	for _, c := range a.Calls() {
		if c.Command == "nginx.default_vhost_listen" {
			var p map[string]any
			_ = json.Unmarshal(c.Params, &p)
			out = append(out, p)
		}
	}
	return out
}

// Promoting an IPv4 to the default moves the panel's default vhost to it:
// tenant vhosts follow the default on the next reconcile pass, and without
// this the old address kept the default server.
func TestIPHandler_PromoteIPv4Default_MovesTheDefaultVhost(t *testing.T) {
	repo := newFakeIPRepo()
	repo.rows = []models.ManagedIP{
		{ID: 1, Address: "203.0.113.121", Family: "ipv4", IsDefault: true},
		{ID: 2, Address: "203.0.113.9", Family: "ipv4"},
	}
	a := agent.NewMockClient().On("nginx.default_vhost_listen", map[string]any{"rewritten": true})
	r := newIPRouterWithAgent(repo, a)

	w := doJSON(t, r, "PATCH", "/api/v1/admin/ips/2", map[string]any{"is_default": true})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Eventually(t, func() bool { return len(defaultListenCalls(a)) == 1 }, 2*time.Second, 5*time.Millisecond)
	require.Equal(t, map[string]any{"ipv4": "203.0.113.9"}, defaultListenCalls(a)[0])
}

// A label edit, a re-promote of the current default and an IPv6 promotion
// leave the default vhost alone (its specific listen lines are IPv4).
func TestIPHandler_NoDefaultVhostCallWithoutAnIPv4Promotion(t *testing.T) {
	repo := newFakeIPRepo()
	repo.rows = []models.ManagedIP{
		{ID: 1, Address: "203.0.113.121", Family: "ipv4", IsDefault: true},
		{ID: 2, Address: "2001:db8::1", Family: "ipv6", IsDefault: true},
		{ID: 3, Address: "2001:db8::2", Family: "ipv6"},
	}
	a := agent.NewMockClient().On("nginx.default_vhost_listen", map[string]any{"rewritten": true})
	r := newIPRouterWithAgent(repo, a)

	for _, req := range []struct {
		path string
		body map[string]any
	}{
		{"/api/v1/admin/ips/1", map[string]any{"label": "main"}},
		{"/api/v1/admin/ips/1", map[string]any{"is_default": true}},
		{"/api/v1/admin/ips/3", map[string]any{"is_default": true}},
	} {
		w := doJSON(t, r, "PATCH", req.path, req.body)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	}
	time.Sleep(50 * time.Millisecond)
	require.Empty(t, defaultListenCalls(a))
}
