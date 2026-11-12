package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// An app is built only when its manifests change: package.json and the
// lockfile for Node.js, requirements.txt for Python. A change to the app's
// code alone (a git pull of a Next.js app) was never built, so the app kept
// serving its old build, and start/stop/restart could not change that.
// app.python.control "rebuild" forgets the app's last build: the next apply
// installs and builds again, then restarts the app.

func TestNodeAppRebuild_CodeChangeBuildsAgain(t *testing.T) {
	f := newNodeBuildFixture(t, "01NODEREBUILDCODE000000000", map[string]string{
		"package.json":      `{"scripts":{"build":"next build","start":"next start"}}`,
		"package-lock.json": `{"lockfileVersion":3}`,
		"page.js":           `export default "v1"`,
	})
	if st, err := f.build(t); err != nil || st != pythonBuildFinished {
		t.Fatalf("first build: %v, %v", st, err)
	}
	if err := os.WriteFile(filepath.Join(f.appRoot, "page.js"), []byte(`export default "v2"`), 0o644); err != nil {
		t.Fatal(err)
	}
	// The gap: new code, same manifests, nothing to do.
	if st, err := f.step(); err != nil || st != pythonBuildNotNeeded {
		t.Fatalf("code-only change: %v, %v; want not needed", st, err)
	}

	clearAppBuildMarkers(f.stateDir, f.p.AppID)

	if st, err := f.build(t); err != nil || st != pythonBuildFinished {
		t.Fatalf("after rebuild: %v, %v; want finished (the apply restarts the app)", st, err)
	}
	cmds := f.commands(t)
	if len(cmds) != 4 || !strings.HasPrefix(cmds[2], "/usr/bin/npm ci ") || !strings.HasPrefix(cmds[3], "/usr/bin/npm run build ") {
		t.Fatalf("commands = %q, want npm ci + npm run build twice", cmds)
	}
}

// A failed build waits out its backoff before the same files are tried again;
// a rebuild tries again straight away.
func TestNodeAppRebuild_SkipsTheFailureBackoff(t *testing.T) {
	f := newNodeBuildFixture(t, "01NODEREBUILDFAIL000000000", map[string]string{"package.json": `{}`})
	t.Setenv("NODEBUILD_FAIL", "1")
	if _, err := f.build(t); err == nil {
		t.Fatal("want the build to fail")
	}
	t.Setenv("NODEBUILD_FAIL", "")
	if _, err := f.step(); err == nil {
		t.Fatal("want the cached failure inside the backoff")
	}

	clearAppBuildMarkers(f.stateDir, f.p.AppID)

	if st, err := f.build(t); err != nil || st != pythonBuildFinished {
		t.Fatalf("after rebuild: %v, %v; want finished", st, err)
	}
}

// A Python app: requirements.txt unchanged, nothing to do; after a rebuild
// pip installs it again and the apply restarts the app.
func TestPythonAppRebuild_InstallsTheRequirementsAgain(t *testing.T) {
	f := newPyBuildFixture(t, "01PYREBUILD000000000000000")
	_ = os.WriteFile(f.rel, nil, 0o644)
	build := func() (pythonBuildState, error) {
		t.Helper()
		if st, err := f.step(); err != nil || st != pythonBuildRunning {
			t.Fatalf("first step = %v, %v; want running", st, err)
		}
		waitPythonBuild(t, f.p.AppID)
		return f.step()
	}
	pipRuns := func() int {
		b, _ := os.ReadFile(f.log)
		return strings.Count(string(b), " -r ")
	}
	if st, err := build(); err != nil || st != pythonBuildFinished {
		t.Fatalf("first build: %v, %v", st, err)
	}
	if st, err := f.step(); err != nil || st != pythonBuildNotNeeded {
		t.Fatalf("converged: %v, %v; want not needed", st, err)
	}

	clearAppBuildMarkers(f.stateDir, f.p.AppID)

	if st, err := build(); err != nil || st != pythonBuildFinished {
		t.Fatalf("after rebuild: %v, %v; want finished", st, err)
	}
	if n := pipRuns(); n != 2 {
		t.Fatalf("pip install -r ran %d times, want 2", n)
	}
}

// Every runtime's build markers go; the app's env file and other apps'
// markers stay.
func TestClearAppBuildMarkers(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	for _, ext := range []string{".env", ".reqsha", ".reqfail", ".npmsha", ".npmfail"} {
		write("01APP" + ext)
	}
	write("01OTHER.npmsha")

	clearAppBuildMarkers(dir, "01APP")

	for _, ext := range []string{".reqsha", ".reqfail", ".npmsha", ".npmfail"} {
		if _, err := os.Stat(filepath.Join(dir, "01APP"+ext)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("01APP%s kept", ext)
		}
	}
	for _, name := range []string{"01APP.env", "01OTHER.npmsha"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s removed", name)
		}
	}
}

func rebuild(t *testing.T, appID string) (any, error) {
	t.Helper()
	return pythonAppControlHandler(context.Background(), json.RawMessage(`{"app_id":"`+appID+`","action":"rebuild"}`))
}

// A rebuild needs no unit (an app whose first build failed has none yet) and
// does not touch the running app: the apply restarts it once the build is done.
func TestPythonAppControl_RebuildLeavesTheUnitAlone(t *testing.T) {
	calls := removeExec(t, "active")
	if _, err := rebuild(t, "01REBUILDNOUNIT0000000000"); err != nil {
		t.Fatal(err)
	}
	for _, c := range calls() {
		if strings.HasPrefix(c, "systemctl start ") || strings.HasPrefix(c, "systemctl restart ") || strings.HasPrefix(c, "systemctl stop ") {
			t.Fatalf("rebuild ran %q", c)
		}
	}
}

// A build that is running would record its result when it ends and so undo
// the rebuild: refuse until it has finished.
func TestPythonAppControl_RebuildRefusedWhileABuildRuns(t *testing.T) {
	removeExec(t, "active")
	const id = "01REBUILDBUSY000000000000"
	pythonBuilds.Lock()
	pythonBuilds.m[id] = &pythonBuild{done: make(chan struct{})}
	pythonBuilds.Unlock()
	t.Cleanup(func() {
		pythonBuilds.Lock()
		delete(pythonBuilds.m, id)
		pythonBuilds.Unlock()
	})

	_, err := rebuild(t, id)
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeFailedPrecondition || !strings.Contains(ae.Message, "already running") {
		t.Fatalf("err = %v, want failed precondition: a build is already running", err)
	}
}

// The handler clears the markers in the env dir, where the applies keep them.
// (pythonAppEnvDir is a constant, so the handler can't be pointed at a test
// directory; the call itself is pinned.)
func TestPythonAppControl_RebuildClearsTheEnvDirMarkers(t *testing.T) {
	src, err := os.ReadFile("python_app_lifecycle.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	i := strings.Index(s, `case "rebuild":`)
	j := strings.Index(s, `case "status":`)
	if i < 0 || j < i || !strings.Contains(s[i:j], "clearAppBuildMarkers(pythonAppEnvDir, p.AppID)") {
		t.Fatal(`the "rebuild" action no longer clears the app's build markers in pythonAppEnvDir`)
	}
}

// A build that has ended but was not collected yet is not running.
func TestPythonAppControl_RebuildAfterAFinishedBuild(t *testing.T) {
	removeExec(t, "active")
	const id = "01REBUILDDONE000000000000"
	done := make(chan struct{})
	close(done)
	pythonBuilds.Lock()
	pythonBuilds.m[id] = &pythonBuild{done: done}
	pythonBuilds.Unlock()
	t.Cleanup(func() {
		pythonBuilds.Lock()
		delete(pythonBuilds.m, id)
		pythonBuilds.Unlock()
	})

	if _, err := rebuild(t, id); err != nil {
		t.Fatal(err)
	}
}
