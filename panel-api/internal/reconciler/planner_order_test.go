package reconciler

import (
	"context"
	"strings"
	"testing"
)

// stepCalls maps a runDependencies step to the Agent methods that apply it
// and the params key that names the domain or zone they apply to. The rate
// limit fragment is shared by every domain, so it has no key.
var stepCalls = map[string]struct {
	methods []string
	key     string
}{
	PhaseNginxRateLimits.Name: {methods: []string{"nginx.ratelimits.apply"}},
	PhaseDNSZone.Name:         {methods: []string{"dns.zone.upsert"}, key: "zone"},
	"ssl":                     {methods: []string{"ssl.self_sign", "ssl.issue"}, key: "domain"},
	PhaseDomainVhost.Name:     {methods: []string{"domain.create"}, key: "domain"},
	PhaseDNSRecursor.Name:     {methods: []string{"pdns.recursor_add_zone"}, key: "zone"},
}

// firstStepIndex is the position of the first call that applies step for
// name, or -1.
func firstStepIndex(t *testing.T, calls []fakeCall, step, name string) int {
	t.Helper()
	sc, ok := stepCalls[step]
	if !ok {
		t.Fatalf("runDependencies names step %q, which stepCalls does not map to an Agent call", step)
	}
	for i, c := range calls {
		matched := false
		for _, m := range sc.methods {
			if c.method == m {
				matched = true
			}
		}
		if !matched {
			continue
		}
		if sc.key != "" {
			p, _ := c.params.(map[string]any)
			v, _ := p[sc.key].(string)
			if strings.TrimSuffix(v, ".") != name {
				continue
			}
		}
		return i
	}
	return -1
}

// orderFixture is the steady-state fixture with certificates wired and none
// issued yet, so every domain's first run self-signs a placeholder: an SSL
// step the Agent can observe without starting ACME, which is out of the
// planner's scope. Both domains take the ordinary tenant path.
func orderFixture(t *testing.T) (*Reconciler, *fakeAgent) {
	t.Helper()
	r, ag := steadyStateFixture(t, false)
	r.WithSSLCerts(newFakeSSLCertRepo())
	return r, ag
}

var orderDomains = []string{"example.com", "shop.example.net"}

// assertRunDependencies checks every edge for every domain. Each step an
// entry point is expected to send must be present, so a pass that stops
// sending a call cannot turn an ordering check into a vacuous pass.
func assertRunDependencies(t *testing.T, calls []fakeCall, sends map[string]bool) {
	t.Helper()
	for step := range sends {
		for _, name := range orderDomains {
			if firstStepIndex(t, calls, step, name) < 0 {
				t.Errorf("no %s call for %s", step, name)
			}
		}
	}
	for _, dep := range runDependencies {
		if !sends[dep.Before] || !sends[dep.After] {
			continue
		}
		for _, name := range orderDomains {
			b, a := firstStepIndex(t, calls, dep.Before, name), firstStepIndex(t, calls, dep.After, name)
			if b < 0 || a < 0 {
				continue
			}
			if b > a {
				t.Errorf("%s: %s (call %d) must precede %s (call %d): %s", name, dep.Before, b, dep.After, a, dep.Why)
			}
		}
	}
}

func agentCallsSince(ag *fakeAgent, from int) []fakeCall {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	return append([]fakeCall(nil), ag.calls[from:]...)
}

func TestRunDependencies_ReconcileAll(t *testing.T) {
	r, ag := orderFixture(t)
	if _, err := r.Run(context.Background(), RunNormal); err != nil {
		t.Fatal(err)
	}
	assertRunDependencies(t, agentCallsSince(ag, 0), map[string]bool{
		PhaseNginxRateLimits.Name: true, PhaseDNSZone.Name: true, "ssl": true,
		PhaseDomainVhost.Name: true, PhaseDNSRecursor.Name: true,
	})
}

func TestRunDependencies_ReconcileOne(t *testing.T) {
	r, ag := orderFixture(t)
	for _, id := range []string{"domain-1", "domain-2"} {
		if err := r.ReconcileOne(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	assertRunDependencies(t, agentCallsSince(ag, 0), map[string]bool{
		PhaseNginxRateLimits.Name: true, PhaseDNSZone.Name: true, "ssl": true,
		PhaseDomainVhost.Name: true, PhaseDNSRecursor.Name: true,
	})
}

// A force run re-renders DNS, SSL and the vhost for every domain; it does
// not touch the recursor.
func TestRunDependencies_ReconcileAllForce(t *testing.T) {
	r, ag := orderFixture(t)
	if _, err := r.Run(context.Background(), RunForce); err != nil {
		t.Fatal(err)
	}
	assertRunDependencies(t, agentCallsSince(ag, 0), map[string]bool{
		PhaseNginxRateLimits.Name: true, PhaseDNSZone.Name: true, "ssl": true,
		PhaseDomainVhost.Name: true,
	})
}

// Every step runDependencies names maps to an Agent call.
func TestRunDependencies_EveryStepIsObservable(t *testing.T) {
	for _, dep := range runDependencies {
		for _, step := range []string{dep.Before, dep.After} {
			if _, ok := stepCalls[step]; !ok {
				t.Errorf("step %q has no Agent call in stepCalls", step)
			}
		}
		if dep.Why == "" {
			t.Errorf("%s → %s has no reason", dep.Before, dep.After)
		}
	}
}
