package main

import (
	"strings"
	"testing"
)

// TestCLICreateDomain_SharedCertThroughModule source-pins that `jabali domain
// create` auto-attaches a covering shared certificate through the domainops
// module the REST create door uses (JAB-279 AC1). createDomainDirect calls
// initConfig / initDB and is not behaviorally testable in a unit test; the
// module itself is covered in internal/domainops.
func TestCLICreateDomain_SharedCertThroughModule(t *testing.T) {
	src := stripLineComments(readGoSource(t, "cli_create.go"))

	if !strings.Contains(src, "domainops.AttachCoveringSharedCert(ctx,") {
		t.Error("CLI create must attach through domainops.AttachCoveringSharedCert")
	}
	// Both soft failures must stay warnings, each with its own message.
	for _, want := range []string{"domainops.ErrSharedCertLookup", "domainops.ErrSharedCertAttach"} {
		if !strings.Contains(src, want) {
			t.Errorf("CLI create must map %s to a warning", want)
		}
	}
	// The CLI's own copy of the step must be gone.
	for _, banned := range []string{"ListServerWideAndOwned(", "CoveringSharedCert(certs", "SetSharedCertificate("} {
		if strings.Contains(src, banned) {
			t.Errorf("CLI create must not call %q directly; the domainops module owns it", banned)
		}
	}
}
