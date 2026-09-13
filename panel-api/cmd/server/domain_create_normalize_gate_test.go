package main

import (
	"strings"
	"testing"
)

// TestCLICreateDomain_NormalizesNameThroughLeaf source-pins that the CLI create
// canonicalizes the domain name through the shared domainops leaf BEFORE it
// validates, defaults the docroot, or stores it. createDomainDirect calls
// initConfig / initDB and is not behaviorally testable in a unit test, so this
// source-pin is the load-bearing guard against the CLI reverting to storing a
// raw --name: a mixed-case name is accepted by the RFC check yet stands up a
// site that never resolves and disagrees with the identity REST/automation
// persist for the same input (JAB-279 AC2 / GH #884).
func TestCLICreateDomain_NormalizesNameThroughLeaf(t *testing.T) {
	// Strip // line comments so a commented-out call cannot satisfy the pin —
	// the guard must catch the call being neutralized, not just deleted. The
	// pinned anchors below contain no "//", so stripping never removes them and
	// relative order is preserved.
	src := stripLineComments(readGoSource(t, "cli_create.go"))

	const call = "in.Name = domainops.NormalizeDomainName(in.Name)"
	normIdx := strings.Index(src, call)
	if normIdx < 0 {
		t.Fatalf("CLI create must canonicalize the name via %q", call)
	}

	// The normalization must precede the first consumer of the name (the FQDN
	// validator) so validate, docroot default, and the row insert all see the
	// canonical form — the same order the REST handler uses (normalize at 848,
	// validate at 854). An edit that normalizes AFTER validation/storage would
	// leave the stored identity un-canonicalized and reddens here.
	valIdx := strings.Index(src, "validateDomainName(in.Name)")
	if valIdx < 0 {
		t.Fatal("expected validateDomainName(in.Name) in the CLI create path")
	}
	if normIdx > valIdx {
		t.Error("CLI create must normalize the name BEFORE validateDomainName(in.Name) so the stored identity is canonical")
	}

	// It must also precede the row construction so the persisted Name is the
	// canonical string, not the operator's raw input.
	storeIdx := strings.Index(src, "d := &models.Domain{")
	if storeIdx < 0 {
		t.Fatal("expected the models.Domain construction in the CLI create path")
	}
	if normIdx > storeIdx {
		t.Error("CLI create must normalize the name BEFORE the models.Domain row is built")
	}
}

// stripLineComments removes everything from the first "//" on each line so a
// source-pin cannot be defeated by commenting the pinned call out while leaving
// its text intact. It is a deliberately simple line scanner (no block-comment
// or string-literal awareness); the pinned anchors in this test carry no "//",
// so it never removes a real match.
func stripLineComments(src string) string {
	lines := strings.Split(src, "\n")
	for i, ln := range lines {
		if idx := strings.Index(ln, "//"); idx >= 0 {
			lines[i] = ln[:idx]
		}
	}
	return strings.Join(lines, "\n")
}
