package main

import (
	"strings"
	"testing"
)

// TestCLICreateDomain_ReverseProxyThroughModule source-pins that `jabali domain
// create --reverse-proxy` reserves, persists, and compensates its loopback port
// through the domainops module the REST create door uses (JAB-279 AC1/AC4).
// createDomainDirect calls initConfig / initDB and is not behaviorally testable
// in a unit test; the module itself is covered in internal/domainops.
func TestCLICreateDomain_ReverseProxyThroughModule(t *testing.T) {
	src := stripLineComments(readGoSource(t, "cli_create.go"))

	for _, want := range []string{
		"domainops.ReserveReverseProxyPort(ctx, deps, d.ID, in.ReverseProxyPort)",
		"domainops.PersistDomain(ctx, domains, ports, d)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("CLI create must route through %q", want)
		}
	}

	// The CLI's own copy of the sequence must be gone — any of these back in
	// cli_create.go means the adapter reserves, probes, or inserts on its own
	// again and can drift from the REST door.
	for _, banned := range []string{
		"AllocateReverseProxySpecific(",
		"AllocateReverseProxy(",
		"ValidateReverseProxyPort(",
		"net.loopback_listener_uid",
		"domains.Create(",
		"ports.Release(",
	} {
		if strings.Contains(src, banned) {
			t.Errorf("CLI create must not call %q directly; the domainops module owns it", banned)
		}
	}

	// A nil *agent.Client assigned to the interface field is a non-nil
	// interface that panics on Call; the adapter must guard the pointer.
	if !strings.Contains(src, "sharedAgent != nil {\n\t\t\tdeps.Agent = sharedAgent") {
		t.Error("CLI create must assign deps.Agent only when sharedAgent is non-nil")
	}
}
