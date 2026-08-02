package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateCLIRef = flag.Bool("update", false, "regenerate the committed CLI reference golden file")

const cliReferencePath = "../../../docs/site/platform/cli-reference.md"

// TestCLIReferenceGolden regenerates the CLI reference from the live Cobra tree
// and fails if it drifts from the committed file (Gitea #536). Run with
// `-update` to regenerate after adding/changing commands or flags.
func TestCLIReferenceGolden(t *testing.T) {
	got := renderCLIReference(newRootCmd())

	if *updateCLIRef {
		if err := os.WriteFile(cliReferencePath, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("wrote %s (%d bytes)", filepath.Base(cliReferencePath), len(got))
		return
	}

	want, err := os.ReadFile(cliReferencePath)
	if err != nil {
		t.Fatalf("read golden (run with -update to create): %v", err)
	}
	if string(want) != got {
		t.Errorf("CLI reference drifted from the command tree.\n" +
			"Regenerate: go test ./panel-api/cmd/server -run TestCLIReferenceGolden -update")
	}
}
