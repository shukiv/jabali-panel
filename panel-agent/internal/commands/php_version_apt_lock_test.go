package commands

import (
	"os/exec"
	"strings"
	"testing"
)

// A PHP-version install that met the dpkg lock (unattended-upgrades, another
// apt run) failed at once, so a migration's PHP step was skipped. apt-get must
// wait for the lock instead: -o DPkg::Lock::Timeout=300 before the action.
func TestPHPVersionInstall_AptWaitsForTheDpkgLock(t *testing.T) {
	orig := execCommand
	var calls [][]string
	execCommand = func(name string, args ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, args...))
		return exec.Command("true")
	}
	t.Cleanup(func() { execCommand = orig })

	if err := runAptUpdate(); err != nil {
		t.Fatal(err)
	}
	if err := installPackages([]string{"php8.3-fpm"}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %v", calls)
	}
	for _, c := range calls {
		line := strings.Join(c, " ")
		if c[0] != "apt-get" || len(c) < 4 || c[1] != "-o" || c[2] != "DPkg::Lock::Timeout=300" {
			t.Errorf("%q: want apt-get -o DPkg::Lock::Timeout=300 before the action", line)
		}
	}
	if got := strings.Join(calls[1], " "); !strings.HasSuffix(got, "install -y --no-install-recommends php8.3-fpm") {
		t.Errorf("install argv changed: %q", got)
	}
}
