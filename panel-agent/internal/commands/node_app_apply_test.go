package commands

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/filesafe"
)

func validNodeParams() nodeAppApplyParams {
	return nodeAppApplyParams{
		AppID: "01NODEAPP00000000000000000", Username: "alice", UserID: "u1",
		AppRoot: "apps/api", NodeVersion: "22", AppType: "node", Entrypoint: "server.js", Port: 20001,
	}
}

func TestValidateNodeAppParams(t *testing.T) {
	cases := []struct {
		name string
		edit func(*nodeAppApplyParams)
		ok   bool
	}{
		{"node script", func(p *nodeAppApplyParams) {}, true},
		{"nested mjs", func(p *nodeAppApplyParams) { p.Entrypoint = "dist/server/index.mjs" }, true},
		{"npm script", func(p *nodeAppApplyParams) { p.AppType, p.Entrypoint = "npm", "start" }, true},
		{"npm namespaced script", func(p *nodeAppApplyParams) { p.AppType, p.Entrypoint = "npm", "start:prod" }, true},
		{"parent segment", func(p *nodeAppApplyParams) { p.Entrypoint = "../other/server.js" }, false},
		{"inner parent segment", func(p *nodeAppApplyParams) { p.Entrypoint = "a/../server.js" }, false},
		{"absolute path", func(p *nodeAppApplyParams) { p.Entrypoint = "/home/bob/server.js" }, false},
		{"not a script", func(p *nodeAppApplyParams) { p.Entrypoint = "server.py" }, false},
		{"space in entrypoint", func(p *nodeAppApplyParams) { p.Entrypoint = "server.js --inspect" }, false},
		{"npm script with space", func(p *nodeAppApplyParams) { p.AppType, p.Entrypoint = "npm", "start; id" }, false},
		{"other node version", func(p *nodeAppApplyParams) { p.NodeVersion = "20" }, false},
		{"unknown app type", func(p *nodeAppApplyParams) { p.AppType = "wsgi" }, false},
		{"port out of range", func(p *nodeAppApplyParams) { p.Port = 70000 }, false},
		{"bad username", func(p *nodeAppApplyParams) { p.Username = "Alice;" }, false},
	}
	for _, tc := range cases {
		p := validNodeParams()
		tc.edit(&p)
		if err := validateNodeAppParams(p); (err == nil) != tc.ok {
			t.Errorf("%s: err = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
}

// PORT and HOST come last, so a tenant's own PORT cannot move the app off its
// allocated port; NODE_ENV defaults to production and the app may override it.
func TestRenderNodeEnvFile(t *testing.T) {
	p := validNodeParams()
	p.Env = map[string]string{"PORT": "3000", "HOST": "0.0.0.0", "NODE_ENV": "development", "API_KEY": "x"}
	got := renderNodeEnvFile(p)
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if lines[0] != "NODE_ENV=production" {
		t.Fatalf("first line = %q", lines[0])
	}
	if lines[len(lines)-2] != "PORT=20001" || lines[len(lines)-1] != "HOST=127.0.0.1" {
		t.Fatalf("PORT/HOST must be the last lines:\n%s", got)
	}
	if strings.Contains(got, "PORT=3000") || strings.Contains(got, "HOST=0.0.0.0") {
		t.Fatalf("the tenant's PORT/HOST must be dropped:\n%s", got)
	}
	if !strings.Contains(got, "NODE_ENV=development\n") || !strings.Contains(got, "API_KEY=x\n") {
		t.Fatalf("the tenant's other variables must be kept:\n%s", got)
	}
}

func TestRenderNodeUnit_ExecStart(t *testing.T) {
	p := validNodeParams()
	unit := renderNodeUnit(p, "/home/alice/apps/api", "/etc/jabali/python-apps/x.env")
	if !strings.Contains(unit, "ExecStart=/usr/bin/node server.js\n") || !strings.Contains(unit, "Description=Jabali Node.js app ") {
		t.Fatalf("node unit:\n%s", unit)
	}
	p.AppType, p.Entrypoint = "npm", "start"
	unit = renderNodeUnit(p, "/home/alice/apps/api", "/etc/jabali/python-apps/x.env")
	if !strings.Contains(unit, "ExecStart=/usr/bin/npm run start\n") {
		t.Fatalf("npm unit:\n%s", unit)
	}
}

// The egress table drops connections to a Python app's port unless the
// listener runs in the owner's slice, and Node.js apps share that registry: a
// Node unit's Slice= line must be the Python unit's, byte for byte.
func TestRenderNodeUnit_SameSliceAsPython(t *testing.T) {
	slice := func(unit string) string {
		for _, l := range strings.Split(unit, "\n") {
			if strings.HasPrefix(l, "Slice=") {
				return l
			}
		}
		return ""
	}
	node := slice(renderNodeUnit(validNodeParams(), "/home/alice/apps/api", "/x.env"))
	py := slice(renderPythonUnit(pythonAppApplyParams{AppID: "01X", Username: "alice", AppType: "wsgi"}, "/home/alice/app", "/x.env", "/usr/bin/true"))
	if node == "" || node != py || node != "Slice="+filepath.Base(SlicePathFor("alice")) {
		t.Fatalf("node %q, python %q", node, py)
	}
}

type nodeBuildFixture struct {
	p                      nodeAppApplyParams
	scope                  *filesafe.Scope
	appRoot, stateDir, log string
}

// fakeNpmRun stands in for `systemd-run --scope ... -- sudo ... npm ...`: it
// logs its arguments, and fails when $NODEBUILD_FAIL is set.
const fakeNpmRun = `
echo "$*" >> "$NODEBUILD_LOG"
if [ -n "$NODEBUILD_FAIL" ]; then echo "npm ERR! code ERESOLVE"; exit 1; fi
`

func newNodeBuildFixture(t *testing.T, appID string, files map[string]string) nodeBuildFixture {
	t.Helper()
	dir := t.TempDir()
	f := nodeBuildFixture{
		p:        validNodeParams(),
		scope:    filesafe.NewScopeForTest("u1", "alice", dir),
		appRoot:  filepath.Join(dir, "app"),
		stateDir: filepath.Join(dir, "state"),
		log:      filepath.Join(dir, "commands.log"),
	}
	f.p.AppID = appID
	for _, d := range []string{f.appRoot, f.stateDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(f.appRoot, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("NODEBUILD_LOG", f.log)
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "systemd-run" {
			return exec.CommandContext(ctx, "sh", append([]string{"-c", fakeNpmRun, "sh"}, args...)...)
		}
		return exec.CommandContext(ctx, "true") // systemctl stop <build scope>
	}
	t.Cleanup(func() {
		waitPythonBuild(t, appID)
		execCommandContext = prev
	})
	return f
}

func (f nodeBuildFixture) step() (pythonBuildState, error) {
	return nodeAppBuildStep(f.p, f.scope, f.appRoot, f.stateDir)
}

// commands returns the logged npm runs, each once it has been checked to run
// in the owner's slice, as the owner, who changes into the app root itself.
func (f nodeBuildFixture) commands(t *testing.T) []string {
	t.Helper()
	prefix := regexp.MustCompile(`^--scope --quiet --collect --slice=jabali-user-alice\.slice --unit=jabali-app-build-` +
		f.p.AppID + `-[0-9]+\.scope --property=TimeoutStopSec=5 -- sudo -u alice -H /usr/bin/env -C ` +
		regexp.QuoteMeta(f.appRoot) + ` -- `)
	b, _ := os.ReadFile(f.log)
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l == "" {
			continue
		}
		loc := prefix.FindStringIndex(l)
		if loc == nil {
			t.Fatalf("npm did not run as the owner, in the owner's slice and the app root: %q", l)
		}
		out = append(out, l[loc[1]:])
	}
	return out
}

// build starts a build, waits for it and returns the step that collects it.
func (f nodeBuildFixture) build(t *testing.T) (pythonBuildState, error) {
	t.Helper()
	if st, err := f.step(); err != nil || st != pythonBuildRunning {
		t.Fatalf("first step = %v, %v; want running", st, err)
	}
	waitPythonBuild(t, f.p.AppID)
	return f.step()
}

// With a lockfile: npm ci, production dependencies only, as the tenant in the
// app root. A second apply with the same files does nothing.
func TestNodeAppBuildStep_LockfileInstallsOnce(t *testing.T) {
	f := newNodeBuildFixture(t, "01NODEBUILDCI0000000000000", map[string]string{
		"package.json":      `{"dependencies":{"express":"^4"}}`,
		"package-lock.json": `{"lockfileVersion":3}`,
	})
	if st, err := f.build(t); err != nil || st != pythonBuildFinished {
		t.Fatalf("after build: %v, %v; want finished", st, err)
	}
	cmds := f.commands(t)
	want := "/usr/bin/npm ci --no-audit --no-fund --no-update-notifier --omit=dev"
	if len(cmds) != 1 || cmds[0] != want {
		t.Fatalf("commands = %q, want [%q]", cmds, want)
	}
	if st, err := f.step(); err != nil || st != pythonBuildNotNeeded {
		t.Fatalf("converged step = %v, %v; want not needed", st, err)
	}
	if n := len(f.commands(t)); n != 1 {
		t.Fatalf("a converged app ran npm again: %d runs", n)
	}
}

// Without a lockfile npm install; a build script keeps the dev dependencies
// and runs npm run build after the install.
func TestNodeAppBuildStep_BuildScript(t *testing.T) {
	f := newNodeBuildFixture(t, "01NODEBUILDBUILD0000000000", map[string]string{
		"package.json": `{"scripts":{"build":"next build","start":"next start"}}`,
	})
	if st, err := f.build(t); err != nil || st != pythonBuildFinished {
		t.Fatalf("after build: %v, %v; want finished", st, err)
	}
	cmds := f.commands(t)
	if len(cmds) != 2 ||
		cmds[0] != "/usr/bin/npm install --no-audit --no-fund --no-update-notifier" ||
		cmds[1] != "/usr/bin/npm run build --no-update-notifier" {
		t.Fatalf("commands = %q", cmds)
	}
}

// A changed package.json builds again.
func TestNodeAppBuildStep_RebuildsOnChange(t *testing.T) {
	f := newNodeBuildFixture(t, "01NODEBUILDCHANGE000000000", map[string]string{"package.json": `{}`})
	if _, err := f.build(t); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.appRoot, "package.json"), []byte(`{"dependencies":{"ws":"^8"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.build(t); err != nil {
		t.Fatal(err)
	}
	if n := len(f.commands(t)); n != 2 {
		t.Fatalf("npm ran %d times, want 2", n)
	}
}

// A lockfile change alone builds again: it pins what npm ci installs.
func TestNodeAppBuildStep_RebuildsOnLockfileChange(t *testing.T) {
	f := newNodeBuildFixture(t, "01NODEBUILDLOCK00000000000", map[string]string{
		"package.json":      `{"dependencies":{"ws":"^8"}}`,
		"package-lock.json": `{"lockfileVersion":3,"packages":{"node_modules/ws":{"version":"8.17.0"}}}`,
	})
	if _, err := f.build(t); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.appRoot, "package-lock.json"), []byte(`{"lockfileVersion":3,"packages":{"node_modules/ws":{"version":"8.18.0"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := f.build(t); err != nil {
		t.Fatal(err)
	}
	if n := len(f.commands(t)); n != 2 {
		t.Fatalf("npm ran %d times, want 2", n)
	}
}

// A failed install comes back as the error and is not retried on the next
// apply for the same files.
func TestNodeAppBuildStep_FailureBacksOff(t *testing.T) {
	f := newNodeBuildFixture(t, "01NODEBUILDFAIL00000000000", map[string]string{"package.json": `{}`})
	t.Setenv("NODEBUILD_FAIL", "1")
	if _, err := f.build(t); err == nil || !strings.Contains(err.Error(), "ERESOLVE") {
		t.Fatalf("err = %v, want the npm output", err)
	}
	if _, err := f.step(); err == nil || !strings.Contains(err.Error(), "ERESOLVE") {
		t.Fatalf("backed-off step err = %v, want the cached error", err)
	}
	if n := len(f.commands(t)); n != 1 {
		t.Fatalf("npm ran %d times inside the backoff, want 1", n)
	}
}

func TestPlanNodeBuild(t *testing.T) {
	home := t.TempDir()
	scope := filesafe.NewScopeForTest("u1", "alice", home)
	app := filepath.Join(home, "app")
	if err := os.Mkdir(app, 0o755); err != nil {
		t.Fatal(err)
	}
	p := validNodeParams()
	// A plain node script without package.json has nothing to install.
	if pl, err := planNodeBuild(p, scope, app, home); err != nil || pl.sha != "" {
		t.Fatalf("no package.json: %+v, %v", pl, err)
	}
	// The npm app type needs a package.json.
	p.AppType, p.Entrypoint = "npm", "start"
	if _, err := planNodeBuild(p, scope, app, home); err == nil || !strings.Contains(err.Error(), "package.json not found") {
		t.Fatalf("npm without package.json: %v", err)
	}
	// A package.json that is not valid JSON is reported before npm runs.
	if err := os.WriteFile(filepath.Join(app, "package.json"), []byte(`{"scripts":`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := planNodeBuild(p, scope, app, home); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("invalid package.json: %v", err)
	}
	// A package.json that is a symlink is refused, not followed as root.
	if err := os.Remove(filepath.Join(app, "package.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(app, "package.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := planNodeBuild(p, scope, app, home); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlinked package.json: %v", err)
	}
}

// The app root is opened beneath the owner's home: a path that leads out of
// it through a symlink is refused, and nothing outside the home is read.
func TestPlanNodeBuild_AppRootStaysInHome(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "package.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(home, "apps")); err != nil {
		t.Fatal(err)
	}
	scope := filesafe.NewScopeForTest("u1", "alice", home)
	pl, err := planNodeBuild(validNodeParams(), scope, filepath.Join(home, "apps"), home)
	if err == nil || pl.sha != "" {
		t.Fatalf("app root through a symlink out of the home: %+v, %v", pl, err)
	}
}

func TestReadTenantFile_RefusesFIFOAndOversize(t *testing.T) {
	path := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(path, "package.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "package-lock.json"), make([]byte, 2048), 0o644); err != nil {
		t.Fatal(err)
	}
	dir, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	done := make(chan error, 1)
	go func() { _, err := readTenantFile(dir, "package.json", 1024); done <- err }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("fifo err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO blocked")
	}
	if _, err := readTenantFile(dir, "package-lock.json", 1024); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("oversize err = %v", err)
	}
}

func TestPackageHasScript(t *testing.T) {
	for content, want := range map[string]bool{
		`{"scripts":{"build":"tsc"}}`:    true,
		`{"scripts":{"start":"node a"}}`: false,
		`{}`:                             false,
	} {
		if got, err := packageHasScript([]byte(content), "build"); err != nil || got != want {
			t.Errorf("%s: got %v, %v; want %v", content, got, err, want)
		}
	}
	if _, err := packageHasScript([]byte(`{"scripts":`), "build"); err == nil {
		t.Fatal("invalid JSON must be an error")
	}
}
