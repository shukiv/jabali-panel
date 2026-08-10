package reconciler

import (
	"context"
	"encoding/json"
	"time"
)

// reconcileRecursorForward calls pdns.recursor_add_zone on the agent for
// the given zone. Idempotent on the agent side (Manager.AddZone is
// Changed=false on no-op), so calling per-tick is safe.
//
// Non-fatal: logs on error and continues. Next tick retries.
//
// The forwarder target is the jabali convention: 127.0.0.1:5300 (pdns-
// server's loopback bind after M6.3's split-port flip). The agent
// handler defaults port=5300 when omitted, but we pass it explicitly
// so the wire payload is self-documenting.
func (r *Reconciler) reconcileRecursorForward(ctx context.Context, zone string) {
	if zone == "" {
		return
	}
	// 30s (was 10s): the agent's AddZone now retries the post-add probe with a
	// short backoff so a freshly-created zone's forwarder isn't rolled back on
	// the first racy probe (GH #896). The retry budget must fit inside this
	// timeout. Existing zones still confirm on the first probe (~1-2s).
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := r.agent.Call(cctx, "pdns.recursor_add_zone", map[string]any{
		"zone": zone,
		"addr": "127.0.0.1",
		"port": 5300,
	})
	if err != nil {
		// Don't spam — the agent RPC is per-domain per-tick. A transient
		// agent-down surfaces already via domain.create failures above.
		r.log.Warn("recursor_add_zone failed", "zone", zone, "err", err)
		return
	}
	// Decode to pick up Changed for logging. Silent on no-op.
	var resp struct {
		Zone    string `json:"zone"`
		Changed bool   `json:"changed"`
	}
	if uErr := json.Unmarshal(raw, &resp); uErr == nil && resp.Changed {
		r.log.Info("recursor forwarder added", "zone", zone)
	}
}

// reconcileRecursorForwardRemove removes a zone's forwarder entry. Called
// from the orphan branch of ReconcileAll when a site exists on the agent
// but has no DB row. Idempotent (remove of absent zone is no-op).
func (r *Reconciler) reconcileRecursorForwardRemove(ctx context.Context, zone string) {
	if zone == "" {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	raw, err := r.agent.Call(cctx, "pdns.recursor_remove_zone", map[string]any{
		"zone": zone,
	})
	if err != nil {
		r.log.Warn("recursor_remove_zone failed", "zone", zone, "err", err)
		return
	}
	var resp struct {
		Zone    string `json:"zone"`
		Changed bool   `json:"changed"`
	}
	if uErr := json.Unmarshal(raw, &resp); uErr == nil && resp.Changed {
		r.log.Info("recursor forwarder removed", "zone", zone)
	}
}

// reconcileRecursorSelfZone walks the panel's own hostname into the
// recursor's forwarders. The self-zone is bootstrapped in
// install.sh:bootstrap_pdns_self_zone — NOT a row in the domains
// table — so the regular enabled-domains loop doesn't cover it.
//
// Called once per ReconcileAll pass, before the enabled-domains loop.
// Idempotent: returns silently if ServerSettings.Hostname is empty
// (broken-install fallback).
func (r *Reconciler) reconcileRecursorSelfZone(ctx context.Context) {
	if r.serverSettings == nil {
		return
	}
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	srv, err := r.serverSettings.Get(sctx)
	if err != nil || srv == nil || srv.Hostname == "" {
		return
	}
	r.reconcileRecursorForward(ctx, srv.Hostname)
}
