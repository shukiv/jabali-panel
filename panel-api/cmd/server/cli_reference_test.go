package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
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

const (
	readmePath     = "../../../README.md"
	readmeCLIBegin = "<!-- BEGIN generated: go test ./panel-api/cmd/server -run TestREADMECLICommands -update -->"
	readmeCLIEnd   = "<!-- END generated -->"
)

// TestREADMECLICommands keeps the README's command list equal to the Cobra
// tree, so a new command is listed there and a removed one drops out. Run
// with `-update` to rewrite the block between the markers.
func TestREADMECLICommands(t *testing.T) {
	raw, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	readme := string(raw)
	begin := strings.Index(readme, readmeCLIBegin)
	end := strings.Index(readme, readmeCLIEnd)
	if begin < 0 || end < begin {
		t.Fatalf("README.md has no %q ... %q block for the command list", readmeCLIBegin, readmeCLIEnd)
	}
	begin += len(readmeCLIBegin)
	want := "\n```\n" + renderCLICommandSummary(newRootCmd()) + "```\n"

	if *updateCLIRef {
		out := readme[:begin] + want + readme[end:]
		if err := os.WriteFile(readmePath, []byte(out), 0o644); err != nil {
			t.Fatalf("write README: %v", err)
		}
		return
	}
	if readme[begin:end] != want {
		t.Errorf("README.md command list drifted from the command tree.\n" +
			"Regenerate: go test ./panel-api/cmd/server -run TestREADMECLICommands -update")
	}
}
