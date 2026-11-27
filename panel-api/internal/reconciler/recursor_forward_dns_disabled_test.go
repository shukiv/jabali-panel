package reconciler

import (
	"context"
	"testing"
)

// zonesCalled lists the zone of every call of method.
func zonesCalled(ag *fakeAgent, method string) []string {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	var zones []string
	for _, c := range ag.calls {
		if c.method != method {
			continue
		}
		if p, ok := c.params.(map[string]any); ok {
			if z, ok := p["zone"].(string); ok {
				zones = append(zones, z)
			}
		}
	}
	return zones
}

func hasZone(zones []string, zone string) bool {
	for _, z := range zones {
		if z == zone {
			return true
		}
	}
	return false
}

// A domain whose DNS is hosted elsewhere must not keep the box recursor's
// forward to the local PowerDNS: with the zone gone, the box itself resolved the
// name to nothing (empty MX/A) while the public DNS had Google/Outlook MX, so
// site mail to the owner deferred and wp-cron to itself failed.
func TestReconcileAll_DNSDisabledDomainLosesItsRecursorForward(t *testing.T) {
	r, agent, _, _ := dnsModuleFixture(t, true, true)
	r.domains.(*fakeDomainRepo).domains["domain-1"].DNSDisabled = true

	if err := r.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hasZone(zonesCalled(agent, "pdns.recursor_add_zone"), "example.com") {
		t.Error("a forward was added for a domain whose DNS is elsewhere")
	}
	if !hasZone(zonesCalled(agent, "pdns.recursor_remove_zone"), "example.com") {
		t.Error("the forward of a domain whose DNS is elsewhere was not removed")
	}
}

func TestReconcileOne_DNSDisabledDomainLosesItsRecursorForward(t *testing.T) {
	r, agent, _, _ := dnsModuleFixture(t, true, true)
	r.domains.(*fakeDomainRepo).domains["domain-1"].DNSDisabled = true

	_ = r.ReconcileOne(context.Background(), "domain-1")
	if hasZone(zonesCalled(agent, "pdns.recursor_add_zone"), "example.com") {
		t.Error("ReconcileOne force-added a forward for a domain whose DNS is elsewhere")
	}
	if !hasZone(zonesCalled(agent, "pdns.recursor_remove_zone"), "example.com") {
		t.Error("ReconcileOne did not remove the forward of a domain whose DNS is elsewhere")
	}
}

// The hosted case still gets its forward.
func TestReconcileAll_HostedDomainKeepsItsRecursorForward(t *testing.T) {
	r, agent, _, _ := dnsModuleFixture(t, true, true)
	if err := r.ReconcileAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !hasZone(zonesCalled(agent, "pdns.recursor_add_zone"), "example.com") {
		t.Error("a hosted domain must get its forward")
	}
	if hasZone(zonesCalled(agent, "pdns.recursor_remove_zone"), "example.com") {
		t.Error("a hosted domain's forward was removed")
	}
}
