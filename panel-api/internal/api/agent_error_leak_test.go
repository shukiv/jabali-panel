package api

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestNoAgentErrorLeak is the JAB-114 regression gate. Agent-origin errors can
// carry the root daemon's stderr, filesystem paths, and driver internals; they
// must be logged server-side (respondAgentErr / respondAgentErrStatus /
// logAgentError) and NEVER echoed to the HTTP client. This test scans the api
// package's own source and fails if any handler puts an agent-error code and a
// raw .Error() on the same response line again.
//
// If this fires on new code: replace the c.JSON(...) with respondAgentErr(c,
// "<code>", err) (or respondAgentErrStatus for the status-wrapped envelope),
// which logs the detail and returns just the code.
func TestNoAgentErrorLeak(t *testing.T) {
	// Agent-origin error codes + a raw error string on the same source line.
	leak := regexp.MustCompile(`"(agent_error|agent_failed|agent_call_failed|apply_failed)"[^\n]*\.Error\(\)`)

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var offenders []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if leak.MatchString(line) {
				offenders = append(offenders, f+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("agent-error detail leaked to the client in %d place(s); use respondAgentErr instead:\n%s",
			len(offenders), strings.Join(offenders, "\n"))
	}
}
