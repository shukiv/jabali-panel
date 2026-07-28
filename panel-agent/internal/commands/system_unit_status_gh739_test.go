package commands

import (
	"strings"
	"testing"
)

// TestStripTransientNoise_GH739 uses the exact lines lxsdevcode saw at the end
// of a successful package update: the benign systemd "Failed to open
// /run/systemd/transient/…" spam must be removed, real apt/unit output kept.
func TestStripTransientNoise_GH739(t *testing.T) {
	unit := "jabali-apt-oneshot.service"
	logTail := strings.Join([]string{
		"jabali-apt-oneshot.service: Deactivated successfully.",
		"jabali-apt-oneshot.service: Consumed 2.596s CPU time, 153.5M memory peak.",
		"jabali-apt-oneshot.service: Failed to open /run/systemd/transient/jabali-apt-oneshot.service: No such file or directory",
		"jabali-apt-oneshot.service: Failed to open /run/systemd/transient/jabali-apt-oneshot.service: No such file or directory",
		"jabali-apt-oneshot.service: Failed to open /run/systemd/transient/jabali-apt-oneshot.service: No such file or directory",
		"Setting up somepkg (1.2.3) ...",
	}, "\n")

	got := stripTransientNoise(logTail, unit)
	if strings.Contains(got, "Failed to open /run/systemd/transient") {
		t.Errorf("transient-GC noise not stripped:\n%s", got)
	}
	for _, want := range []string{"Deactivated successfully", "Consumed 2.596s", "Setting up somepkg"} {
		if !strings.Contains(got, want) {
			t.Errorf("stripped real content %q:\n%s", want, got)
		}
	}
}

func TestStripTransientNoise_ScopedAndNoOp(t *testing.T) {
	unit := "jabali-apt-oneshot.service"

	// A clean log is returned unchanged (no allocation churn either).
	clean := "Setting up foo\njabali-apt-oneshot.service: Deactivated successfully.\n"
	if got := stripTransientNoise(clean, unit); got != clean {
		t.Errorf("clean log changed:\n%q", got)
	}

	// Scoped: a DIFFERENT unit's transient line must NOT be stripped.
	other := "other.service: Failed to open /run/systemd/transient/other.service: No such file or directory\n"
	if got := stripTransientNoise(other, unit); got != other {
		t.Errorf("stripped an unrelated unit's line:\n%q", got)
	}
}
