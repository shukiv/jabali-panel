package settingsops

import (
	"reflect"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// nginxApplyTimeout is the per-call agent timeout for both nginx settings verbs
// (matches the pre-extraction REST goroutine and CLI callAgentTimeout budgets).
const nginxApplyTimeout = 60 * time.Second

// Kind classifies a settings effect so an adapter (or its tests) can tell a
// genuine value change from an explicit re-apply of the same value. The dispatch
// decision does NOT depend on Kind — it depends only on whether the field family
// was touched (Call != nil) — so the re-sync semantics of the original handlers
// are preserved exactly. Kind exists for reporting and for the AC's
// "no-op / changed / explicitly re-applied are distinguishable".
type Kind int

const (
	// NoOp: the field family was not touched; nothing to dispatch.
	NoOp Kind = iota
	// Changed: touched, and at least one value differs before→after.
	Changed
	// Reapply: touched, but every value equals its previous value (an explicit
	// re-sync — still dispatched, matching the original behavior).
	Reapply
)

// AgentCall is one agent dispatch: the verb, its parameter map (the exact agent
// wire), and the per-call timeout. This is the single owner of the nginx agent
// wire that both adapters execute.
type AgentCall struct {
	Method  string
	Params  map[string]any
	Timeout time.Duration
}

// Effect is one settings side effect. Call is non-nil iff Kind != NoOp; an
// adapter dispatches Call under its own execution policy. Rollback is the
// compensating call if the apply fails; nil for nginx (the agent's own `nginx -t`
// gate reverts a bad config, so there is no panel-side rollback). The field is
// the seam JAB-295 (SSH) will populate.
//
// RevertOnEnableFailure marks an ENABLE transition whose persisted flag must be
// cleared if Call fails (tenant Docker on unprivileged LXC — GH #272). The
// module owns the decision (which module reverts, and only on the enable
// direction); the adapter owns the execution — reverting the flag is a
// persistence write against the adapter's own repo handle, not an agent call, so
// it cannot be expressed as Rollback. Only DockerTenant sets this today.
type Effect struct {
	Kind                  Kind
	Call                  *AgentCall
	Rollback              *AgentCall
	RevertOnEnableFailure bool
}

// NginxTouched reports which nginx field families the adapter merged this
// request. REST computes it from which request fields were present; the CLI from
// which setters ran. Kept as adapter input (not re-derived here) so the module
// preserves the exact touched-based re-sync semantics of both handlers.
type NginxTouched struct {
	Tunables bool
	Cache    bool
}

// NginxPlan is the declarative plan for the nginx settings family: at most one
// dispatch per verb (tunables + cache capacity).
type NginxPlan struct {
	Tunables Effect
	Cache    Effect
}

// NginxEffects derives the nginx settings effect plan from the validated
// before/after settings and the touched families. before is the pre-merge
// snapshot (old values); after is the merged settings that will be persisted.
func NginxEffects(before, after *models.ServerSettings, touched NginxTouched) NginxPlan {
	return NginxPlan{
		Tunables: effect(touched.Tunables, "nginx.tunables.apply",
			nginxTunablesParams(before), nginxTunablesParams(after)),
		Cache: effect(touched.Cache, "nginx.cache.capacity_apply",
			nginxCacheParams(before), nginxCacheParams(after)),
	}
}

// effect builds one Effect: NoOp when the family was not touched, otherwise an
// AgentCall with the after-params, classified Changed vs Reapply by comparing
// before/after.
func effect(touched bool, method string, beforeParams, afterParams map[string]any) Effect {
	if !touched {
		return Effect{Kind: NoOp}
	}
	kind := Reapply
	if !reflect.DeepEqual(beforeParams, afterParams) {
		kind = Changed
	}
	return Effect{
		Kind: kind,
		Call: &AgentCall{Method: method, Params: afterParams, Timeout: nginxApplyTimeout},
	}
}

// nginxTunablesParams is the exact nginx.tunables.apply wire (13 keys). This is
// the single source both adapters previously copied verbatim.
func nginxTunablesParams(s *models.ServerSettings) map[string]any {
	return map[string]any{
		"client_max_body_size":  s.NginxClientMaxBodySize,
		"keepalive_timeout":     s.NginxKeepaliveTimeout,
		"server_tokens":         s.NginxServerTokens,
		"gzip":                  s.NginxGzip,
		"client_body_timeout":   s.NginxClientBodyTimeout,
		"client_header_timeout": s.NginxClientHeaderTimeout,
		"send_timeout":          s.NginxSendTimeout,
		"proxy_connect_timeout": s.NginxProxyConnectTimeout,
		"proxy_read_timeout":    s.NginxProxyReadTimeout,
		"proxy_send_timeout":    s.NginxProxySendTimeout,
		"worker_processes":      s.NginxWorkerProcesses,
		"worker_connections":    s.NginxWorkerConnections,
		"custom_http":           s.NginxCustomHTTP,
	}
}

// nginxCacheParams is the exact nginx.cache.capacity_apply wire (3 keys).
func nginxCacheParams(s *models.ServerSettings) map[string]any {
	return map[string]any{
		"max_size_gb":  s.NginxCacheMaxSizeGB,
		"keyzone_mb":   s.NginxCacheKeyzoneMB,
		"inactive_min": s.NginxCacheInactiveMin,
	}
}
