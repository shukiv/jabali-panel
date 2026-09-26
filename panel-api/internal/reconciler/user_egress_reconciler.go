package reconciler

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// userEgressLastSeen tracks the last counter value the reconciler read
// for each user. Used to compute per-tick deltas after a `nft list
// counters` (without reset). Process-local — survives across ticks but
// not across panel-api restart; on restart we re-baseline from the
// next tick's read. The M14 burst-source threshold compares against
// the per-tick delta, so a missed reset adds at most one tick of noise.
var (
	userEgressMu       sync.Mutex
	userEgressLastSeen = map[string]uint64{}
)

// reconcileUserEgress is the M34 per-tick pass. Reads every policy +
// dispatches user.egress.apply with the snapshot, then reads counters
// (without reset — RESET would race with a runaway-loop user dropping
// 1000s/sec) and persists per-tick delta as drop_count_24h. The column
// name is historical; the value is "drops since last tick" which is
// what the burst-source signal cares about.
func (r *Reconciler) reconcileUserEgress(ctx context.Context) {
	if r.userEgressPolicies == nil {
		return
	}
	policies, err := r.userEgressPolicies.ListAllForReconcile(ctx)
	if err != nil {
		r.log.Warn("user-egress reconcile: list policies", "error", err)
		return
	}
	defaults := r.readUserEgressDefaults(ctx)
	if len(policies) == 0 {
		// No policies yet — emit an empty table so any prior table on
		// disk gets cleared. This costs one nft reload per tick on a
		// fresh install but keeps state convergent (DB is truth).
		r.applyUserEgress(ctx, nil, defaults)
		return
	}
	r.applyUserEgress(ctx, policies, defaults)
	// Build username→user_id map once from the same policies the apply
	// pass already fetched. Avoids N FindByUsername round-trips per tick.
	usernameToID := make(map[string]string, len(policies))
	for _, p := range policies {
		usernameToID[p.Username] = p.UserID
	}
	r.readUserEgressCounters(ctx, usernameToID)
}

// readUserEgressDefaults loads the operator-overridden default allowlist
// from server_settings. Returns nil when ServerSettings is unavailable
// or every column is NULL — caller forwards nil to the agent which
// falls back to CanonicalDefaults().
func (r *Reconciler) readUserEgressDefaults(ctx context.Context) map[string]any {
	if r.serverSettings == nil {
		return nil
	}
	s, err := r.settingsGet(ctx)
	if err != nil || s == nil {
		return nil
	}
	out := map[string]any{}
	if s.EgressDefaultLoopbackCIDRs != nil {
		var v []string
		if err := json.Unmarshal(*s.EgressDefaultLoopbackCIDRs, &v); err == nil {
			out["loopback4"] = v
		}
	}
	if s.EgressDefaultLoopback6CIDRs != nil {
		var v []string
		if err := json.Unmarshal(*s.EgressDefaultLoopback6CIDRs, &v); err == nil {
			out["loopback6"] = v
		}
	}
	if s.EgressDefaultPortsTCP != nil {
		var v []int
		if err := json.Unmarshal(*s.EgressDefaultPortsTCP, &v); err == nil {
			out["ports_tcp"] = v
		}
	}
	if s.EgressDefaultPortsUDP != nil {
		var v []int
		if err := json.Unmarshal(*s.EgressDefaultPortsUDP, &v); err == nil {
			out["ports_udp"] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (r *Reconciler) applyUserEgress(ctx context.Context, policies []repository.PolicyForReconcile, defaults map[string]any) {
	users := make([]map[string]any, 0, len(policies))
	for _, p := range policies {
		// Defence in depth: the agent rejects unknown states.
		if p.State != models.UserEgressStateOff &&
			p.State != models.UserEgressStateLearning &&
			p.State != models.UserEgressStateEnforced {
			continue
		}
		payload, cidrErr := buildEgressUserPayload(p)
		if cidrErr != nil {
			// Fail CLOSED: the SSH-out extras were dropped by the builder; the
			// user's base policy still applies. Surface why so the operator fixes
			// the corrupt column instead of wondering why SSH-out never took.
			r.log.Warn("user-egress: bad egress_ssh_out_cidrs — SSH-out NOT applied",
				"user_id", p.UserID, "error", cidrErr)
		}
		users = append(users, payload)
	}
	dispatchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	payload := map[string]any{"users": users}
	if defaults != nil {
		payload["defaults"] = defaults
	}
	_, agentErr := r.agent.Call(dispatchCtx, "user.egress.apply", payload)
	if agentErr != nil {
		r.log.Warn("user-egress reconcile: agent apply failed", "error", agentErr,
			"user_count", len(users))
		return
	}
}

// buildEgressUserPayload renders one user's slot of the user.egress.apply
// payload from the joined policy row. Pure (no I/O) so the GH #1798 fold-in is
// unit-testable. The base allowed_extra is copied through; the per-package
// outbound-SSH allowance (GH #1798) is appended as :22 TCP extras, one per
// scoped CIDR; and allow_ping carries the per-package ICMP allowance. A NULL
// package COALESCEs to EgressSSHOut=false / EgressICMP=false upstream, so a
// missing package grants neither. On a corrupt egress_ssh_out_cidrs the SSH-out
// extras are DROPPED (fail closed) and the parse error is returned for the
// caller to log — the payload is still valid and carries the base policy.
func buildEgressUserPayload(p repository.PolicyForReconcile) (map[string]any, error) {
	extras := make([]map[string]any, 0, len(p.AllowedExtra))
	for _, e := range p.AllowedExtra {
		row := map[string]any{
			"cidr":     e.CIDR,
			"protocol": e.Protocol,
			"comment":  e.Comment,
		}
		if e.Port != nil {
			row["port"] = *e.Port
		}
		extras = append(extras, row)
	}
	var cidrErr error
	if p.EgressSSHOut {
		cidrs, err := models.ParseEgressSSHOutCIDRs(p.EgressSSHOutCIDRs)
		if err != nil {
			cidrErr = err // fail closed — no :22 extras appended
		} else {
			const sshPort = 22
			for _, cidr := range cidrs {
				port := sshPort
				extras = append(extras, map[string]any{
					"cidr":     cidr,
					"port":     port,
					"protocol": "tcp",
					"comment":  "package SSH-out (GH #1798)",
				})
			}
		}
	}
	return map[string]any{
		"username":      p.Username,
		"state":         p.State,
		"allowed_extra": extras,
		// GH #1798: per-package ICMP echo-request (ping) allowance.
		"allow_ping": p.EgressICMP,
	}, cidrErr
}

func (r *Reconciler) readUserEgressCounters(ctx context.Context, usernameToID map[string]string) {
	dispatchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, agentErr := r.agent.Call(dispatchCtx, "user.egress.read_counters", map[string]any{
		"reset": false,
	})
	if agentErr != nil {
		r.log.Debug("user-egress reconcile: read_counters failed", "error", agentErr)
		return
	}
	var resp struct {
		Counters []struct {
			Username string `json:"username"`
			Packets  uint64 `json:"packets"`
		} `json:"counters"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		r.log.Warn("user-egress reconcile: counter parse failed", "error", err)
		return
	}

	now := time.Now().UTC()
	userEgressMu.Lock()
	defer userEgressMu.Unlock()
	for _, c := range resp.Counters {
		prev := userEgressLastSeen[c.Username]
		var delta uint64
		if c.Packets >= prev {
			delta = c.Packets - prev
		} else {
			// nft counter wrapped (extremely unlikely) or table was
			// rebuilt — treat the new value as the delta.
			delta = c.Packets
		}
		userEgressLastSeen[c.Username] = c.Packets
		// Resolve user_id from the map built by reconcileUserEgress out
		// of the same ListAllForReconcile result the apply pass used.
		// Counter rows for a just-deleted user linger one tick — harmless.
		uid, ok := usernameToID[c.Username]
		if !ok || uid == "" {
			continue
		}
		if err := r.userEgressPolicies.SetDropCount(ctx, uid, delta, now); err != nil {
			r.log.Debug("user-egress reconcile: SetDropCount failed",
				"user", c.Username, "error", err)
		}
		// M34 deep stats — persist per-tick delta so admin Egress card
		// renders a 24h sparkline. Insert is no-op when delta == 0.
		if r.userEgressDropSamples != nil && delta > 0 {
			if err := r.userEgressDropSamples.Insert(ctx, uid, now, delta); err != nil {
				r.log.Debug("user-egress reconcile: drop-sample insert failed",
					"user", c.Username, "error", err)
			}
		}
	}

	// Prune samples older than 25h once per counter-read tick. Cheap
	// (one DELETE). 1h buffer past the 24h render window so a slow
	// tick does not drop the in-progress hour.
	if r.userEgressDropSamples != nil {
		cutoff := now.Add(-25 * time.Hour)
		if err := r.userEgressDropSamples.PruneOlderThan(ctx, cutoff); err != nil {
			r.log.Debug("user-egress reconcile: drop-sample prune failed", "error", err)
		}
	}
}
