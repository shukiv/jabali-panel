package backup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// waitForFile polls until path exists or the deadline passes.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never appeared", path)
}

// runCancelled starts sh -c script through realRunner, waits for the script
// to create started, cancels its context and returns once Run has returned.
func runCancelled(t *testing.T, started string, args ...string) (time.Duration, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := realRunner{}.Run(ctx, "sh", args, nil, nil)
		done <- err
	}()
	waitForFile(t, started)
	cancelledAt := time.Now()
	cancel()
	select {
	case err := <-done:
		return time.Since(cancelledAt), err
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
		return 0, nil
	}
}

// A cancelled command gets SIGINT, not SIGKILL, so restic can run its
// cleanup and remove its repository lock. On the old exec.CommandContext
// default the process was killed outright and the trap never ran.
func TestRealRunner_CancelSendsInterrupt(t *testing.T) {
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	interrupted := filepath.Join(dir, "interrupted")
	script := `trap 'touch "$2"; exit 130' INT; touch "$1"; while :; do sleep 0.05; done`

	_, err := runCancelled(t, started, "-c", script, "sh", started, interrupted)
	if err == nil {
		t.Error("a cancelled command must still return an error")
	}
	if _, statErr := os.Stat(interrupted); statErr != nil {
		t.Fatalf("command was not sent SIGINT on cancel (no %s): %v", interrupted, statErr)
	}
}

// A command that ignores SIGINT is still killed once CancelGrace passes,
// so a hung restic cannot hold up its caller forever.
func TestRealRunner_CancelKillsAfterGrace(t *testing.T) {
	prev := CancelGrace
	CancelGrace = 300 * time.Millisecond
	t.Cleanup(func() { CancelGrace = prev })

	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	script := `trap '' INT; touch "$1"; while :; do sleep 0.05; done`

	took, err := runCancelled(t, started, "-c", script, "sh", started)
	if err == nil {
		t.Error("a killed command must return an error")
	}
	if took > 10*time.Second {
		t.Fatalf("Run took %s after cancel; want about CancelGrace (%s)", took, CancelGrace)
	}
}

// StopGracefully leaves a command made with exec.Command alone: setting
// Cancel on it would make Start fail.
func TestStopGracefully_LeavesPlainCommandRunnable(t *testing.T) {
	cmd := StopGracefully(exec.Command("true"))
	if cmd.Cancel != nil || cmd.WaitDelay != 0 {
		t.Fatalf("plain command was changed: Cancel set=%v WaitDelay=%s", cmd.Cancel != nil, cmd.WaitDelay)
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("plain command no longer runs: %v", err)
	}
}

// End to end with a real restic: a backup cancelled while it holds its
// repository lock leaves no lock behind. Skipped where restic is not
// installed.
func TestRealRunner_CancelledResticLeavesNoLock(t *testing.T) {
	bin, err := exec.LookPath("restic")
	if err != nil {
		t.Skip("restic not installed")
	}
	dir := t.TempDir()
	repo := filepath.Join(dir, "repo")
	pw := filepath.Join(dir, "pw")
	if err := os.WriteFile(pw, []byte("test-only-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cacheEnv := "RESTIC_CACHE_DIR=" + filepath.Join(dir, "cache")
	restic := func(args ...string) string {
		t.Helper()
		c := exec.Command(bin, append([]string{"-r", repo, "-p", pw}, args...)...)
		c.Env = append(os.Environ(), cacheEnv)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("restic %v: %v: %s", args, err, out)
		}
		return string(out)
	}
	countLocks := func() int {
		return len(strings.Fields(restic("--no-lock", "list", "locks")))
	}
	restic("init")

	// restic takes its lock, then blocks reading stdin, which the test
	// keeps open until after the cancel.
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdinW.Close()
	defer stdinR.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := realRunner{}.Run(ctx, bin,
			[]string{"-r", repo, "-p", pw, "backup", "--stdin", "--stdin-filename", "x"},
			[]string{cacheEnv}, stdinR)
		done <- err
	}()

	deadline := time.Now().Add(20 * time.Second)
	for countLocks() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("restic never took its lock")
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()

	// restic removes its lock as soon as it gets SIGINT, but a --stdin
	// backup only exits once its read returns, so check the lock first and
	// close stdin after.
	deadline = time.Now().Add(10 * time.Second)
	for {
		n := countLocks()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cancelled restic left %d lock(s) in the repository", n)
		}
		time.Sleep(100 * time.Millisecond)
	}
	stdinW.Close()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("restic did not exit after cancel")
	}
}
