package main

import (
	"os"
	"strings"
	"testing"
)

// TestCLICreateDomain_RoutesOwnerGateThroughLeaf source-pins that the CLI create
// runs the shared domainops eligibility gate rather than its own inline checks.
// createDomainDirect calls initConfig / initDB and is not behaviorally testable
// in a unit test, so this source-pin is the load-bearing guard: it is the only
// thing that catches the CLI reverting to its old, drifted gate — which never
// checked owner.Suspended, so a suspended owner could get a live vhost from the
// command line (JAB-279 AC3).
func TestCLICreateDomain_RoutesOwnerGateThroughLeaf(t *testing.T) {
	src := readGoSource(t, "cli_create.go")

	if !strings.Contains(src, "domainops.CheckOwnerEligible(owner)") {
		t.Error("CLI create must run the shared domainops.CheckOwnerEligible gate")
	}
	// The suspended branch is the gap this slice closes; pin that the CLI now
	// maps it, so a future edit that drops it reddens here.
	if !strings.Contains(src, "domainops.ErrOwnerSuspended") {
		t.Error("CLI create must refuse a suspended owner (domainops.ErrOwnerSuspended)")
	}
	// The old inline admin check must be gone — its presence means the CLI is
	// gating itself again instead of routing through the leaf.
	if strings.Contains(src, "if owner.IsAdmin {") {
		t.Error("CLI create must not keep its own inline owner.IsAdmin check")
	}
}

func readGoSource(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
