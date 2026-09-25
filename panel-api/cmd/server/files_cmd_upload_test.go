package main

import (
	"strings"
	"testing"
)

// Source-pin the upload wiring (JAB-337 → JAB-365): the CLI resolves the
// admin-configured limits and stages + ingests through the Upload Intake module,
// so it shares the File Manager's owner identity, in-flight cap and byte budget.
// The per-owner isolation itself is tested in internal/uploadintake and by the
// CLI matrix in files_upload_intake_test.go.
func TestCLIUpload_UsesTheUploadIntakeModule(t *testing.T) {
	s := stripLineComments(readGoSource(t, "files_cmd.go"))
	for _, need := range []string{"cliResolveUploadLimits(c.Context())", "uploadintake.Stage(", "uploadintake.Ingest("} {
		if !strings.Contains(s, need) {
			t.Errorf("files_cmd.go must call %s", need)
		}
	}
	for _, banned := range []string{
		"jabali-upload-cli-",        // the old CLI-only staging identity
		"maxCLIAgentIngestBytes",    // the old 100 MiB clamp
		"cliStagingDirBytesForUser", // the old CLI-only budget
		"os.ReadFile(localPath)",    // the whole-file read the clamp existed for
		`"files.ingest"`,            // the ingest hand-off belongs to the module
	} {
		if strings.Contains(s, banned) {
			t.Errorf("files_cmd.go still contains %s — upload staging belongs to internal/uploadintake", banned)
		}
	}
}
