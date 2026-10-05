package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// GH #357: a first `pip install -r requirements.txt` takes minutes, longer
// than the panel's 120s deadline for app.python.apply. The build must run in
// the background so apply returns at once.

// fakeSudo stands in for `sudo -u <user> -H <cmd...>`: it logs each command,
// creates the venv, and holds `pip install -r` until $PYBUILD_RELEASE exists.
const fakeSudo = `
echo "$*" >> "$PYBUILD_LOG"
for a; do last="$a"; done
case " $* " in
  *" -m venv "*) mkdir -p "$last/bin" && touch "$last/bin/python" "$last/bin/pip" ;;
  *" -r "*)
    while [ ! -f "$PYBUILD_RELEASE" ]; do sleep 0.02; done
    if [ -n "$PYBUILD_PIP_FAIL" ]; then echo "Collecting brokenpkg"; echo "ERROR: build failed"; exit 1; fi
    touch "$PYBUILD_VENV/bin/gunicorn"
    echo "Successfully installed gunicorn-23.0.0 wagtail-4.2.4" ;;
  *" install "*) touch "$PYBUILD_VENV/bin/gunicorn" ;;
esac
`

type pyBuildFixture struct {
	p                                 pythonAppApplyParams
	appRoot, venv, stateDir, log, rel string
}

func newPyBuildFixture(t *testing.T, appID string) pyBuildFixture {
	t.Helper()
	dir := t.TempDir()
	f := pyBuildFixture{
		p:        pythonAppApplyParams{AppID: appID, Username: "tenant", PythonVersion: "3.11", AppType: "wsgi"},
		appRoot:  filepath.Join(dir, "app"),
		stateDir: filepath.Join(dir, "state"),
		log:      filepath.Join(dir, "commands.log"),
		rel:      filepath.Join(dir, "release"),
	}
	f.venv = filepath.Join(f.appRoot, "venv")
	for _, d := range []string{f.appRoot, f.stateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.appRoot, "requirements.txt"), []byte("wagtail==4.2.4\ngunicorn\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PYBUILD_LOG", f.log)
	t.Setenv("PYBUILD_RELEASE", f.rel)
	t.Setenv("PYBUILD_VENV", f.venv)
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "sudo" {
			return exec.CommandContext(ctx, "sh", append([]string{"-c", fakeSudo, "sh"}, args...)...)
		}
		return exec.CommandContext(ctx, "true") // python3.11 -c "import ensurepip"
	}
	t.Cleanup(func() {
		_ = os.WriteFile(f.rel, nil, 0o644) // never leave a build blocked
		waitPythonBuild(t, appID)
		execCommandContext = prev
	})
	return f
}

func (f pyBuildFixture) step() (pythonBuildState, error) {
	return pythonAppBuildStep(f.p, f.appRoot, f.venv, "gunicorn", f.stateDir)
}

// waitPythonBuild blocks until the app's background build has finished.
func waitPythonBuild(t *testing.T, appID string) {
	t.Helper()
	pythonBuilds.Lock()
	b, ok := pythonBuilds.m[appID]
	pythonBuilds.Unlock()
	if !ok {
		return
	}
	select {
	case <-b.done:
	case <-time.After(10 * time.Second):
		t.Fatal("background build did not finish")
	}
}

func TestPythonAppBuildStep_ReturnsWhilePipRuns(t *testing.T) {
	f := newPyBuildFixture(t, "01BUILDSLOWPIP0000000000AA")

	start := time.Now()
	state, err := f.step()
	if err != nil || state != pythonBuildRunning {
		t.Fatalf("first call: state=%v err=%v, want running", state, err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("apply waited %v for pip; it must return while the build runs", d)
	}
	if state, err := f.step(); err != nil || state != pythonBuildRunning {
		t.Fatalf("second call while pip runs: state=%v err=%v, want running", state, err)
	}

	_ = os.WriteFile(f.rel, nil, 0o644)
	waitPythonBuild(t, f.p.AppID)
	if state, err := f.step(); err != nil || state != pythonBuildFinished {
		t.Fatalf("after the build: state=%v err=%v, want finished", state, err)
	}
	if state, err := f.step(); err != nil || state != pythonBuildNotNeeded {
		t.Fatalf("converged app: state=%v err=%v, want not needed", state, err)
	}
	if b, _ := os.ReadFile(filepath.Join(f.stateDir, f.p.AppID+".reqsha")); len(b) == 0 {
		t.Fatal("a successful requirements install must write the sha marker")
	}

	log, _ := os.ReadFile(f.log)
	for _, line := range strings.Split(strings.TrimSpace(string(log)), "\n") {
		if strings.Contains(line, " install ") && !strings.Contains(line, "--disable-pip-version-check") {
			t.Errorf("pip must run with --disable-pip-version-check (sudo drops the env var): %q", line)
		}
	}
	if strings.Count(string(log), " install ") != 1 {
		t.Errorf("requirements installed gunicorn, so pip must run once; log:\n%s", log)
	}
}

func TestPythonAppBuildStep_FailureIsReportedThenBackedOff(t *testing.T) {
	f := newPyBuildFixture(t, "01BUILDFAILPIP0000000000AA")
	t.Setenv("PYBUILD_PIP_FAIL", "1")
	_ = os.WriteFile(f.rel, nil, 0o644)

	if state, err := f.step(); err != nil || state != pythonBuildRunning {
		t.Fatalf("first call: state=%v err=%v, want running", state, err)
	}
	waitPythonBuild(t, f.p.AppID)
	_, err := f.step()
	if err == nil || !strings.Contains(err.Error(), "pip install requirements") || !strings.Contains(err.Error(), "brokenpkg") {
		t.Fatalf("the finished build's failure must come back, naming the package; got %v", err)
	}

	// Same requirements inside the backoff: the cached error, no new pip run.
	before, _ := os.ReadFile(f.log)
	state, err := f.step()
	if err == nil || state == pythonBuildRunning {
		t.Fatalf("inside the backoff: state=%v err=%v, want the cached error", state, err)
	}
	after, _ := os.ReadFile(f.log)
	if string(after) != string(before) {
		t.Fatalf("pip must not run again inside the backoff; new commands:\n%s", strings.TrimPrefix(string(after), string(before)))
	}
}

// The agent hashes requirements.txt as root, at a path the tenant controls.
func TestFileSHA256_OnlyRegularFilesWithinCap(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "requirements.txt")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := fileSHA256(fifo); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a FIFO must be rejected")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hashing a FIFO blocked; it must be rejected without reading")
	}

	if _, err := fileSHA256(dir); err == nil {
		t.Fatal("a directory must be rejected")
	}
	big := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(big, make([]byte, maxRequirementsBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := fileSHA256(big); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("an oversized file must be rejected, got %v", err)
	}
}

func TestCancelPythonBuild_StopsAndForgetsTheBuild(t *testing.T) {
	f := newPyBuildFixture(t, "01BUILDCANCEL00000000000AA")
	if state, err := f.step(); err != nil || state != pythonBuildRunning {
		t.Fatalf("state=%v err=%v, want running", state, err)
	}
	pythonBuilds.Lock()
	b := pythonBuilds.m[f.p.AppID]
	pythonBuilds.Unlock()

	cancelPythonBuild(f.p.AppID)
	select {
	case <-b.done:
	case <-time.After(5 * time.Second):
		t.Fatal("a cancelled build must stop without its pip being released")
	}
	pythonBuilds.Lock()
	_, still := pythonBuilds.m[f.p.AppID]
	pythonBuilds.Unlock()
	if still {
		t.Fatal("a cancelled build must be forgotten")
	}
}

// extraPyBuildApp adds another app to base's fixture: same fake sudo, log and
// release file, its own app root, and the given owner.
func extraPyBuildApp(t *testing.T, base pyBuildFixture, appID, user string) pyBuildFixture {
	t.Helper()
	f := base
	f.p.AppID, f.p.Username = appID, user
	f.appRoot = filepath.Join(t.TempDir(), "app")
	f.venv = filepath.Join(f.appRoot, "venv")
	if err := os.MkdirAll(f.appRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.appRoot, "requirements.txt"), []byte("wagtail\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(f.rel, nil, 0o644); waitPythonBuild(t, appID) })
	return f
}

// startedPips waits for want pip installs to start, then a little longer so
// one more could start if the limit allowed it, and returns how many started.
func startedPips(t *testing.T, log string, want int) int {
	t.Helper()
	count := func() int { b, _ := os.ReadFile(log); return strings.Count(string(b), " -r ") }
	deadline := time.Now().Add(3 * time.Second)
	for count() < want && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	return count()
}

func TestPythonAppBuildStep_OneBuildPerAccount(t *testing.T) {
	a := newPyBuildFixture(t, "01BUILDACCTA0000000000000A")
	b := extraPyBuildApp(t, a, "01BUILDACCTB0000000000000A", a.p.Username)
	for _, f := range []pyBuildFixture{a, b} {
		if state, err := f.step(); err != nil || state != pythonBuildRunning {
			t.Fatalf("%s: state=%v err=%v, want running", f.p.AppID, state, err)
		}
	}
	if n := startedPips(t, a.log, 1); n != 1 {
		t.Fatalf("one account's builds must run one at a time; %d pip installs started", n)
	}
	_ = os.WriteFile(a.rel, nil, 0o644)
	waitPythonBuild(t, a.p.AppID)
	waitPythonBuild(t, b.p.AppID)
	if n := startedPips(t, a.log, 2); n != 2 {
		t.Fatalf("the account's second build must run after the first; %d ran", n)
	}
}

func TestPythonAppBuildStep_BoxWideCap(t *testing.T) {
	base := newPyBuildFixture(t, "01BUILDCAPA00000000000000A")
	fs := []pyBuildFixture{base}
	for i, id := range []string{"01BUILDCAPB00000000000000A", "01BUILDCAPC00000000000000A", "01BUILDCAPD00000000000000A"} {
		fs = append(fs, extraPyBuildApp(t, base, id, fmt.Sprintf("tenant%d", i+2)))
	}
	for _, f := range fs {
		if state, err := f.step(); err != nil || state != pythonBuildRunning {
			t.Fatalf("%s: state=%v err=%v, want running", f.p.AppID, state, err)
		}
	}
	if n := startedPips(t, base.log, cap(pythonBuildSlots)); n != cap(pythonBuildSlots) {
		t.Fatalf("%d pip installs running at once across accounts, want %d", n, cap(pythonBuildSlots))
	}
	_ = os.WriteFile(base.rel, nil, 0o644)
	for _, f := range fs {
		waitPythonBuild(t, f.p.AppID)
	}
	if n := startedPips(t, base.log, len(fs)); n != len(fs) {
		t.Fatalf("after a slot frees, the waiting build must run; %d of %d ran", n, len(fs))
	}
}

func TestCancelPythonBuildsForUser_StopsOnlyThatAccount(t *testing.T) {
	a := newPyBuildFixture(t, "01BUILDUSERDEL0000000000AA")
	other := extraPyBuildApp(t, a, "01BUILDUSERKEEP000000000AA", "someoneelse")
	for _, f := range []pyBuildFixture{a, other} {
		if state, err := f.step(); err != nil || state != pythonBuildRunning {
			t.Fatalf("%s: state=%v err=%v, want running", f.p.AppID, state, err)
		}
	}
	start := time.Now()
	cancelPythonBuildsForUser(a.p.Username, 5*time.Second)
	if time.Since(start) > 4*time.Second {
		t.Fatal("cancelPythonBuildsForUser must return once the account's builds have stopped")
	}
	pythonBuilds.Lock()
	_, gone := pythonBuilds.m[a.p.AppID]
	_, kept := pythonBuilds.m[other.p.AppID]
	pythonBuilds.Unlock()
	if gone || !kept {
		t.Fatalf("only the deleted account's build may be stopped (deleted still listed=%v, other kept=%v)", gone, kept)
	}
}
