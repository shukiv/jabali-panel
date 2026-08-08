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
// logAgentError) and NEVER echoed to the HTTP client.
//
// The check is BLOCK-scoped, not line-scoped. The original version required an
// agent error code and a raw `.Error()` to appear on the SAME source line,
// which missed every multi-line response — and multi-line is the normal shape:
//
//	c.JSON(http.StatusBadGateway, gin.H{
//	    "error":  "agent_error",
//	    "detail": agentErr.Error(),   // <- previous regex never saw this
//	})
//
// Six real leaks were live behind that gap, including one reachable by a
// non-admin owner via PATCH /users/:id (a tenant changing their password while
// the agent was failing got the root daemon's raw error text back).
//
// If this fires on new code: replace the c.JSON(...) with respondAgentErr(c,
// "<code>", err) (or respondAgentErrStatus for the status-wrapped envelope),
// which logs the detail and returns just the code.
func TestNoAgentErrorLeak(t *testing.T) {
	t.Parallel()

	// Agent-origin error codes. A response carrying one of these must not also
	// carry a raw error string.
	agentCode := regexp.MustCompile(`"(agent_error|agent_failed|agent_call_failed|apply_failed|restore_failed|stream_failed|domain_attach_failed|drop_events_failed)"`)
	// A raw error being marshalled into the response body.
	rawErr := regexp.MustCompile(`\.Error\(\)`)
	// Error variables that hold an AGENT error specifically — these must never
	// be echoed regardless of the response code, because the value itself is
	// the sensitive part.
	agentErrVar := regexp.MustCompile(`"detail":\s*(agentErr|aerr|agErr|callErr|cerr)\.Error\(\)`)

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var offenders []string
	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(src), "\n")
		for i, line := range lines {
			// Named agent-error variable echoed as detail — always a leak.
			if agentErrVar.MatchString(line) {
				offenders = append(offenders, f+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
				continue
			}
			if !strings.Contains(line, "c.JSON(") {
				continue
			}
			checked++
			// Take the response block: this line through the line whose brace
			// depth returns to zero (cap the window so an unbalanced parse
			// can't run away).
			block, depth := "", 0
			for j := i; j < len(lines) && j < i+15; j++ {
				block += lines[j] + "\n"
				depth += strings.Count(lines[j], "(") - strings.Count(lines[j], ")")
				if j > i && depth <= 0 {
					break
				}
			}
			if agentCode.MatchString(block) && rawErr.MatchString(block) {
				offenders = append(offenders, f+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line)+" … (agent code + raw .Error() in the same response)")
			}
		}
	}
	if checked == 0 {
		t.Fatal("scanned no c.JSON responses — the layout changed and this test " +
			"is no longer checking anything")
	}
	if len(offenders) > 0 {
		t.Fatalf("agent-error detail leaked to the client in %d place(s); use respondAgentErr instead:\n%s",
			len(offenders), strings.Join(offenders, "\n"))
	}
}
