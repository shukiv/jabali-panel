package commands

import (
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/cronvalidate"
)

// GH #1435: a multi-command job renders one ExecStart= per command into the
// Type=oneshot unit, and gates on each referenced docroot exactly once.
func TestBuildCronServiceContent_MultiCommand(t *testing.T) {
	dr := "/home/u/domains/x/public_html"
	raw := "wp --path=" + dr + " keyhook-properties generate-xml --file=" + dr + "/props.xml\n" +
		"wp --path=" + dr + " all-import run 1 --force-run"
	cmds, err := cronvalidate.ValidateAnyMulti(raw, []string{dr}, nil)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}

	content := buildCronServiceContent("job1", "WP import", cmds, "u", []string{dr})

	if n := strings.Count(content, "\nExecStart="); n != 2 {
		t.Fatalf("want 2 ExecStart lines, got %d:\n%s", n, content)
	}
	if !strings.Contains(content, "'generate-xml'") || !strings.Contains(content, "'all-import'") {
		t.Fatalf("both commands should be present:\n%s", content)
	}
	// Both commands reference the same --path=<dr>; it is gated exactly once.
	if n := strings.Count(content, "ConditionPathIsDirectory="+dr); n != 1 {
		t.Fatalf("want the docroot gated once (deduped), got %d:\n%s", n, content)
	}
	// No raw newline ever appears inside a single ExecStart value.
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "ExecStart=") && strings.Count(line, "ExecStart=") != 1 {
			t.Fatalf("a command leaked across ExecStart lines: %q", line)
		}
	}
}

// Pins the two-token `--path <dir>` docroot-detection index (distinct code path
// from the glued `--path=<dir>` form).
func TestBuildCronServiceContent_TwoTokenPathGates(t *testing.T) {
	dr := "/home/u/domains/x/public_html"
	cmds, err := cronvalidate.ValidateAnyMulti("wp --path "+dr+" plugin list", []string{dr}, nil)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	content := buildCronServiceContent("job1", "list", cmds, "u", []string{dr})
	if !strings.Contains(content, "ConditionPathIsDirectory="+dr) {
		t.Fatalf("two-token --path should gate the docroot:\n%s", content)
	}
}
