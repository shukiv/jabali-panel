// GH #1620 — opt-in automatic PowerDNS orphan-record sweep.
//
// When server_settings.dns_orphan_autosweep_enabled is true (and DNS is
// enabled), the reconciler fires the agent RPC `dns.reap-orphans` with
// apply=true at most once per hour. Each box then self-removes PowerDNS
// backend rows whose parent domain no longer exists — the same rows the
// one-shot operator CLI (`jabali dns prune-orphan-records`) targets,
// stranded when a domain was deleted before #1629's teardown fix landed.
//
// Safety: the agent refuses the sweep outright (CodeFailedPrecondition)
// when its own domains table is empty, so an enabled-but-misconfigured
// box deletes nothing. A box with no PowerDNS backend answers
// CodeInternal. Both mean "this box can't sweep" — logged and skipped,
// not faults — because enabling the toggle fleet-wide hits non-DNS boxes.
// The lastRun stamp is set BEFORE the call, so a refusing or failing box
// waits a full hour instead of retrying every 60s tick.
package reconciler

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
)

// dnsOrphanSweepInterval bounds how often the sweep RPC fires. Orphans
// only accrue when a domain is deleted, so an hour is ample; a faster
// cadence just re-asks the agent to delete rows it already deleted.
const dnsOrphanSweepInterval = time.Hour

// reconcileDNSOrphanSweep is the per-tick DNS orphan sweep. Cheap noop
// when the toggle is off, DNS is disabled, or a required dep is nil.
func (r *Reconciler) reconcileDNSOrphanSweep(ctx context.Context) {
	if r.serverSettings == nil || r.agent == nil {
		return
	}
	srv, err := r.serverSettings.Get(ctx)
	if err != nil || srv == nil || !srv.DNSOrphanAutosweepEnabled || !srv.DNSEnabled {
		return
	}

	// Evaluate hourly, not every 60s tick. Orphans only appear on a
	// domain delete; between deletes every call would ask the agent to
	// re-delete an empty set. Set the stamp BEFORE the call so a refusing
	// or failing box backs off a full hour instead of hammering the RPC.
	r.dnsSweepMu.Lock()
	if !r.dnsSweepLastRun.IsZero() && time.Since(r.dnsSweepLastRun) < dnsOrphanSweepInterval {
		r.dnsSweepMu.Unlock()
		return
	}
	r.dnsSweepLastRun = time.Now()
	r.dnsSweepMu.Unlock()

	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	raw, err := r.agent.Call(callCtx, "dns.reap-orphans", map[string]any{"apply": true})
	if err != nil {
		// CodeFailedPrecondition (empty domains table) and CodeInternal
		// (no PowerDNS backend on this box) both mean "nothing to sweep
		// here", not a fault — a fleet-wide toggle hits both on non-DNS
		// boxes. Log at Info and move on; anything else is a real
		// failure worth a Warn.
		var ae *agent.AgentError
		if errors.As(err, &ae) && (ae.Code == agent.CodeFailedPrecondition || ae.Code == agent.CodeInternal) {
			r.log.Info("dns_orphan_sweep: skipped", "reason", ae.Message)
			return
		}
		r.log.Warn("dns_orphan_sweep: agent call failed", "err", err)
		return
	}

	var resp struct {
		Applied bool           `json:"applied"`
		Counts  map[string]int `json:"counts"`
		Deleted map[string]int `json:"deleted"`
		Names   []string       `json:"names"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		r.log.Warn("dns_orphan_sweep: parse agent reply failed", "err", err)
		return
	}

	total := 0
	for _, n := range resp.Deleted {
		total += n
	}
	if total > 0 {
		r.log.Info("dns_orphan_sweep: removed orphaned PowerDNS rows",
			"deleted", resp.Deleted, "names", len(resp.Names))
	} else {
		r.log.Debug("dns_orphan_sweep: no orphans", "counts", resp.Counts)
	}
}
