package main

import (
	"strings"
	"testing"
)

// TestCLICreateDomain_ConfinesDocRootThroughLeaf source-pins that the CLI create
// runs the shared domainops document-root confinement before it stores or
// defaults the path. createDomainDirect calls initConfig / initDB and is not
// behaviorally testable in a unit test, so this source-pin is the load-bearing
// guard against the CLI reverting to accepting an arbitrary --doc-root — a path
// the reconciler then mkdir -p's and serves (JAB-279 AC1/AC2 confinement).
func TestCLICreateDomain_ConfinesDocRootThroughLeaf(t *testing.T) {
	src := readGoSource(t, "cli_create.go")

	// The confinement call, anchored on the owner username — that argument is the
	// whole point (confine to the OWNER's home), so pin it literally. An edit that
	// drops the call or swaps the anchor reddens here.
	if !strings.Contains(src, "domainops.ValidateDocumentRoot(docRoot, *owner.Username, in.Name)") {
		t.Error("CLI create must confine the document root via domainops.ValidateDocumentRoot(docRoot, *owner.Username, in.Name)")
	}
	// The path must be trimmed before it is validated and stored, matching the
	// REST create path (an untrimmed trailing space would otherwise be mkdir'd).
	if !strings.Contains(src, "strings.TrimSpace(in.DocRoot)") {
		t.Error("CLI create must trim the document root (strings.TrimSpace(in.DocRoot)) as the REST path does")
	}
}
