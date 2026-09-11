package reconciler

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// dnsSweepReconciler builds a reconciler whose only wired deps are the
// agent and a server-settings repo carrying the two gate flags.
func dnsSweepReconciler(ag *fakeAgent, autosweep, dnsEnabled bool) *Reconciler {
	r := New(nil, nil, ag, slog.Default(), Config{})
	r.serverSettings = &fakeServerSettingsRepo{settings: &models.ServerSettings{
		DNSEnabled:                dnsEnabled,
		DNSOrphanAutosweepEnabled: autosweep,
	}}
	return r
}

// reapCallsWithApply counts dns.reap-orphans calls whose params carried
// apply == want.
func reapCallsWithApply(ag *fakeAgent, want bool) int {
	n := 0
	for _, c := range ag.calls {
		if c.method != "dns.reap-orphans" {
			continue
		}
		p, ok := c.params.(map[string]any)
		if ok && p["apply"] == want {
			n++
		}
	}
	return n
}

// okReply is a valid dns.reap-orphans apply response with one deleted row.
func okReply() json.RawMessage {
	return json.RawMessage(`{"applied":true,"counts":{"records":1},"deleted":{"records":1},"names":["stale.example.com"]}`)
}

// Toggle OFF: the sweep never calls the agent.
func TestDNSOrphanSweep_OffMakesNoCall(t *testing.T) {
	ag := &fakeAgent{resultByMethod: map[string]json.RawMessage{"dns.reap-orphans": okReply()}}
	r := dnsSweepReconciler(ag, false, true)
	r.reconcileDNSOrphanSweep(context.Background())
	if len(ag.calls) != 0 {
		t.Fatalf("toggle off issued agent calls: %v", ag.calls)
	}
}

// Autosweep ON but DNS disabled on this box: still no call. Guards the
// fleet-wide-enable case where non-DNS boxes must stay silent.
func TestDNSOrphanSweep_DNSDisabledMakesNoCall(t *testing.T) {
	ag := &fakeAgent{resultByMethod: map[string]json.RawMessage{"dns.reap-orphans": okReply()}}
	r := dnsSweepReconciler(ag, true, false)
	r.reconcileDNSOrphanSweep(context.Background())
	if len(ag.calls) != 0 {
		t.Fatalf("dns-disabled box issued agent calls: %v", ag.calls)
	}
}

// Toggle ON + DNS enabled: exactly one dns.reap-orphans call, and it must
// carry apply=true (a dry-run would delete nothing).
func TestDNSOrphanSweep_OnCallsApply(t *testing.T) {
	ag := &fakeAgent{resultByMethod: map[string]json.RawMessage{"dns.reap-orphans": okReply()}}
	r := dnsSweepReconciler(ag, true, true)
	r.reconcileDNSOrphanSweep(context.Background())
	if n := reapCallsWithApply(ag, true); n != 1 {
		t.Fatalf("apply=true reap calls = %d, want 1 (calls: %v)", n, ag.calls)
	}
	if n := reapCallsWithApply(ag, false); n != 0 {
		t.Fatalf("sent a dry-run (apply=false) call: %d", n)
	}
}

// Second invocation inside the hour is gated: still one call total.
func TestDNSOrphanSweep_HourlyGate(t *testing.T) {
	ag := &fakeAgent{resultByMethod: map[string]json.RawMessage{"dns.reap-orphans": okReply()}}
	r := dnsSweepReconciler(ag, true, true)
	r.reconcileDNSOrphanSweep(context.Background())
	r.reconcileDNSOrphanSweep(context.Background())
	if got := len(ag.calls); got != 1 {
		t.Fatalf("reap calls after two ticks = %d, want 1 (hourly gate)", got)
	}
}

// A CodeFailedPrecondition refusal (empty domains table) must not panic,
// and must still advance the gate so the box backs off a full hour
// instead of retrying every tick. This is the fail-soft discriminator.
func TestDNSOrphanSweep_RefusalIsSoftAndAdvancesGate(t *testing.T) {
	ag := &fakeAgent{errByMethod: map[string]error{
		"dns.reap-orphans": &agent.AgentError{
			Code:    agent.CodeFailedPrecondition,
			Message: "refusing to sweep: the pdns domains table is empty",
		},
	}}
	r := dnsSweepReconciler(ag, true, true)
	r.reconcileDNSOrphanSweep(context.Background()) // refused
	r.reconcileDNSOrphanSweep(context.Background()) // gated by lastRun set before the call
	if got := len(ag.calls); got != 1 {
		t.Fatalf("reap calls after refusal+retry = %d, want 1 (lastRun must be set before the call)", got)
	}
}
