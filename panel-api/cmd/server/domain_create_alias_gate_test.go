package main

import (
	"strings"
	"testing"
)

// TestCLICreateDomain_RunsAliasCollisionGate source-pins that the CLI create runs
// the shared cross-tenant alias-collision guard (api.AliasCollision) after it
// validates the name and before it stores the domain, and that it fails CLOSED on
// a lookup error. createDomainDirect calls initConfig / initDB and is not
// behaviorally testable in a unit test, so this source-pin is the load-bearing
// guard against the CLI reverting to creating a domain that hijacks another
// tenant's alias server_name (JAB-279 / GH #1625). The shared function's own
// fail-closed behavior is proven by TestAliasCollision_FailsClosedOnLookupError.
func TestCLICreateDomain_RunsAliasCollisionGate(t *testing.T) {
	src := stripLineComments(readGoSource(t, "cli_create.go"))

	const call = "api.AliasCollision(ctx, repository.NewWebDomainAliasRepository(sharedDB), in.Name)"
	callIdx := strings.Index(src, call)
	if callIdx < 0 {
		t.Fatalf("CLI create must run the shared guard via %q", call)
	}

	// It must inspect the returned error and fail CLOSED — an edit that discards
	// it (e.g. `_ :=`) would let a DB lookup failure proceed to create the domain
	// unchecked, the exact fail-open this slice removed.
	if !strings.Contains(src, "cerr != nil") {
		t.Error("CLI create must check the AliasCollision error and fail closed, not discard it")
	}

	// Ordering: after the FQDN validator, before the row is built — the same
	// validate -> collision precedence the REST create path uses, and before any
	// side effect.
	valIdx := strings.Index(src, "validateDomainName(in.Name)")
	if valIdx < 0 {
		t.Fatal("expected validateDomainName(in.Name) in the CLI create path")
	}
	if callIdx < valIdx {
		t.Error("alias-collision gate must run AFTER validateDomainName(in.Name)")
	}
	storeIdx := strings.Index(src, "d := &models.Domain{")
	if storeIdx < 0 {
		t.Fatal("expected the models.Domain construction in the CLI create path")
	}
	if callIdx > storeIdx {
		t.Error("alias-collision gate must run BEFORE the models.Domain row is built")
	}
}
