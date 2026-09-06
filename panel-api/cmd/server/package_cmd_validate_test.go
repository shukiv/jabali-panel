package main

import (
	"os"
	"strings"
	"testing"
)

// TestPackageCLI_ValidatesBeforePersisting pins that both direct-DB write paths
// (`package create` and `package edit`) run packageops.Validate on the row
// BEFORE handing it to the repository — the JAB-306 fix. Without the ordering
// the CLI would persist a limit the REST API rejects with 422. This is a
// source-pin (the transport-neutral validator has no HTTP/agent seam to unit
// test through the CLI without a live DB); it is non-vacuous because the
// packageops.Validate call did not exist on main. Falsify by deleting either
// call.
func TestPackageCLI_ValidatesBeforePersisting(t *testing.T) {
	src, err := os.ReadFile("package_cmd.go")
	if err != nil {
		t.Fatalf("read package_cmd.go: %v", err)
	}
	s := string(src)

	firstValidate := strings.Index(s, "packageops.Validate(p)")
	lastValidate := strings.LastIndex(s, "packageops.Validate(p)")
	createCall := strings.Index(s, "Create(ctx, p)")
	updateCall := strings.Index(s, "repo.Update(ctx, p)")

	if firstValidate < 0 || createCall < 0 || updateCall < 0 {
		t.Fatalf("expected packageops.Validate(p) + Create(ctx, p) + repo.Update(ctx, p) to all be present; got validate=%d create=%d update=%d", firstValidate, createCall, updateCall)
	}
	if firstValidate == lastValidate {
		t.Fatalf("expected packageops.Validate(p) on BOTH the create and edit paths, found only one occurrence")
	}
	// create path: the first Validate must precede the repository Create.
	if !(firstValidate < createCall) {
		t.Errorf("create path: packageops.Validate(p) (idx %d) must come before Create(ctx, p) (idx %d)", firstValidate, createCall)
	}
	// edit path: the second Validate sits after the create call and before the
	// repository Update.
	if !(createCall < lastValidate && lastValidate < updateCall) {
		t.Errorf("edit path: packageops.Validate(p) (idx %d) must sit between the create call (idx %d) and repo.Update(ctx, p) (idx %d)", lastValidate, createCall, updateCall)
	}
}
