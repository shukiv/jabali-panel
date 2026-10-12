package commands

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// userdelRecorder fakes every command runUserdel runs. userdel exits with the
// next code of exits (the last one repeats); everything else succeeds.
func userdelRecorder(t *testing.T, exits ...int) *[]string {
	t.Helper()
	// The pool reap removes files: keep it off the host.
	t.Setenv("JABALI_FPM_CONFIG_ROOT", t.TempDir())
	t.Setenv("JABALI_SYSTEMD_ROOT", t.TempDir())
	prevPhpver, prevRules, prevEtc := userPhpverDir, phpDefensePoolRulesDir, phpEtcRoot
	userPhpverDir, phpDefensePoolRulesDir, phpEtcRoot = t.TempDir(), t.TempDir(), t.TempDir()
	prevWait := userdelRetryWait
	userdelRetryWait = 0
	var cmds []string
	n := 0
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cmds = append(cmds, strings.Join(append([]string{name}, args...), " "))
		if name == "userdel" {
			code := exits[len(exits)-1]
			if n < len(exits) {
				code = exits[n]
			}
			n++
			if code != 0 {
				return exec.CommandContext(ctx, "sh", "-c", "exit "+strconv.Itoa(code))
			}
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() {
		execCommandContext = prev
		userdelRetryWait = prevWait
		userPhpverDir, phpDefensePoolRulesDir, phpEtcRoot = prevPhpver, prevRules, prevEtc
	})
	return &cmds
}

func countPrefix(cmds []string, prefix string) int {
	n := 0
	for _, c := range cmds {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// The reconciler can start the user's PHP pool again while user.delete runs,
// and userdel then exits 8 ("user ... is currently used by process"). The
// delete stops the pools, ends the user's processes and runs userdel again.
func TestRunUserdel_RetriesWhileTheAccountIsInUse(t *testing.T) {
	cmds := userdelRecorder(t, 8, 0)

	if _, err := runUserdel(context.Background(), "udretry", true); err != nil {
		t.Fatalf("runUserdel: %v (commands %q)", err, *cmds)
	}

	if got := countPrefix(*cmds, "userdel --remove udretry"); got != 2 {
		t.Fatalf("userdel ran %d times, want 2: %q", got, *cmds)
	}
	stop, kill := -1, -1
	for i, c := range *cmds {
		switch c {
		case "systemctl stop jabali-fpm@udretry.service":
			stop = i
		case "pkill -KILL -u udretry":
			kill = i
		}
	}
	last := len(*cmds) - 1
	if stop < 1 || kill < 1 || stop >= last || kill >= last || (*cmds)[last] != "userdel --remove udretry" {
		t.Errorf("between the two userdel runs, want the pool stopped and the user's processes killed: %q", *cmds)
	}
}

func TestRunUserdel_GivesUpAfterThreeTries(t *testing.T) {
	cmds := userdelRecorder(t, 8)

	_, err := runUserdel(context.Background(), "udretry", false)

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 8 {
		t.Fatalf("err = %v, want userdel's exit 8", err)
	}
	if got := countPrefix(*cmds, "userdel udretry"); got != 3 {
		t.Errorf("userdel ran %d times, want 3: %q", got, *cmds)
	}
}

// Any other failure is not retried: exit 12 has its own fallback in the
// handler, and the rest are errors.
func TestRunUserdel_DoesNotRetryOtherFailures(t *testing.T) {
	cmds := userdelRecorder(t, 12)

	_, err := runUserdel(context.Background(), "udretry", true)

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 12 {
		t.Fatalf("err = %v, want userdel's exit 12", err)
	}
	if len(*cmds) != 1 {
		t.Errorf("commands = %q, want userdel once", *cmds)
	}
}
