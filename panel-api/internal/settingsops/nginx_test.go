package settingsops

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// populatedNginx is a ServerSettings with every nginx field set, used to pin the
// exact agent wire the two adapters used to build by hand.
func populatedNginx() *models.ServerSettings {
	return &models.ServerSettings{
		NginxClientMaxBodySize:   "50m",
		NginxKeepaliveTimeout:    "65s",
		NginxServerTokens:        true,
		NginxGzip:                true,
		NginxClientBodyTimeout:   "60s",
		NginxClientHeaderTimeout: "60s",
		NginxSendTimeout:         "60s",
		NginxProxyConnectTimeout: "60s",
		NginxProxyReadTimeout:    "60s",
		NginxProxySendTimeout:    "60s",
		NginxWorkerProcesses:     "auto",
		NginxWorkerConnections:   1024,
		NginxCustomHTTP:          "# hi",
		NginxCacheMaxSizeGB:      4,
		NginxCacheKeyzoneMB:      32,
		NginxCacheInactiveMin:    90,
	}
}

// TestNginxEffects_TunablesWire_Golden pins the nginx.tunables.apply wire: exact
// method, 13-key params (native model types), and 60s timeout.
func TestNginxEffects_TunablesWire_Golden(t *testing.T) {
	after := populatedNginx()
	plan := NginxEffects(&models.ServerSettings{}, after, NginxTouched{Tunables: true})

	require.NotNil(t, plan.Tunables.Call)
	call := plan.Tunables.Call
	require.Equal(t, "nginx.tunables.apply", call.Method)
	require.Equal(t, 60*time.Second, call.Timeout)
	require.Equal(t, map[string]any{
		"client_max_body_size":  "50m",
		"keepalive_timeout":     "65s",
		"server_tokens":         true,
		"gzip":                  true,
		"client_body_timeout":   "60s",
		"client_header_timeout": "60s",
		"send_timeout":          "60s",
		"proxy_connect_timeout": "60s",
		"proxy_read_timeout":    "60s",
		"proxy_send_timeout":    "60s",
		"worker_processes":      "auto",
		"worker_connections":    uint32(1024),
		"custom_http":           "# hi",
	}, call.Params)
}

// TestNginxEffects_CacheWire_Golden pins the nginx.cache.capacity_apply wire.
func TestNginxEffects_CacheWire_Golden(t *testing.T) {
	after := populatedNginx()
	plan := NginxEffects(&models.ServerSettings{}, after, NginxTouched{Cache: true})

	require.NotNil(t, plan.Cache.Call)
	call := plan.Cache.Call
	require.Equal(t, "nginx.cache.capacity_apply", call.Method)
	require.Equal(t, 60*time.Second, call.Timeout)
	require.Equal(t, map[string]any{
		"max_size_gb":  4,
		"keyzone_mb":   32,
		"inactive_min": 90,
	}, call.Params)
}

// TestNginxEffects_Untouched_NoOp: nothing touched → no dispatch for either verb.
func TestNginxEffects_Untouched_NoOp(t *testing.T) {
	plan := NginxEffects(populatedNginx(), populatedNginx(), NginxTouched{})
	require.Equal(t, NoOp, plan.Tunables.Kind)
	require.Nil(t, plan.Tunables.Call)
	require.Equal(t, NoOp, plan.Cache.Kind)
	require.Nil(t, plan.Cache.Call)
}

// TestNginxEffects_Kind_ChangedVsReapply: touched dispatches either way, but the
// kind distinguishes a real value change from an explicit re-apply.
func TestNginxEffects_Kind_ChangedVsReapply(t *testing.T) {
	before := populatedNginx()

	// Same values, but touched → Reapply, and STILL dispatched (Call non-nil),
	// preserving the original re-sync semantics.
	same := populatedNginx()
	reapply := NginxEffects(before, same, NginxTouched{Tunables: true, Cache: true})
	require.Equal(t, Reapply, reapply.Tunables.Kind)
	require.NotNil(t, reapply.Tunables.Call, "reapply must still dispatch")
	require.Equal(t, Reapply, reapply.Cache.Kind)
	require.NotNil(t, reapply.Cache.Call)

	// Changed tunable value.
	changed := populatedNginx()
	changed.NginxKeepaliveTimeout = "30s"
	changedPlan := NginxEffects(before, changed, NginxTouched{Tunables: true})
	require.Equal(t, Changed, changedPlan.Tunables.Kind)

	// Changed cache value.
	changedCache := populatedNginx()
	changedCache.NginxCacheMaxSizeGB = 8
	changedCachePlan := NginxEffects(before, changedCache, NginxTouched{Cache: true})
	require.Equal(t, Changed, changedCachePlan.Cache.Kind)
}

// TestNginxEffects_NoRollback: nginx has no panel-side rollback (the agent's
// nginx -t gate reverts). The seam field stays nil.
func TestNginxEffects_NoRollback(t *testing.T) {
	plan := NginxEffects(&models.ServerSettings{}, populatedNginx(), NginxTouched{Tunables: true, Cache: true})
	require.Nil(t, plan.Tunables.Rollback)
	require.Nil(t, plan.Cache.Rollback)
}
