package cronvalidate

import (
	"strings"
	"testing"
)

// TestRejectControlCharInjection pins the fix for the systemd-unit injection:
// a newline (or other control char) inside a quoted arg must be rejected, even
// though it would otherwise survive shlex + single-quoting and break out of the
// generated ExecStart= line into attacker-controlled unit directives.
func TestRejectControlCharInjection(t *testing.T) {
	dr := "/home/u/public_html"
	payloads := []string{
		// The proven PoC: newline-bearing quoted arg with a valid --path.
		"wp cron event run --path=" + dr + " '\n[Service]\nExecStartPre=/bin/sh -c \"id\"\n'",
		// CR variant.
		"wp cron event run --path=" + dr + " '\r[Service]'",
		// NUL inside quotes.
		"php /home/u/public_html/x.php '\x00'",
		// Newline in a double-quoted arg.
		"wp cron event run --path=" + dr + " \"\n[Service]\"",
	}
	for _, p := range payloads {
		if _, err := ValidateCommand(p, []string{dr}, ""); err == nil {
			t.Errorf("control-char payload was ACCEPTED (injection): %q", p)
		}
	}
}

// TestTabStillAllowed — a tab cannot break a unit line, so it stays allowed
// (don't over-reject and break legitimate commands).
func TestTabStillAllowed(t *testing.T) {
	dr := "/home/u/public_html"
	// tab between args; should not be rejected for the control-char reason.
	cmd, err := ValidateCommand("wp\tcron event run --path="+dr, []string{dr}, "")
	if err != nil {
		// It's fine if shlex/other rules reject for a different reason, but it
		// must NOT be the control-char reject path with a real tab.
		if strings.Contains(err.Error(), "control characters") {
			t.Errorf("tab wrongly rejected as control char: %v", err)
		}
		return
	}
	_ = cmd
}
