package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// GH #357: app.python.apply ran the venv + pip build inside the RPC. The panel
// gives that call 120s, and a first `pip install -r requirements.txt` for a
// real project (Wagtail and its dependencies, building wheels for a fresh
// Python 3.11) takes longer. The agent applies the panel's deadline to the
// handler, so pip was killed at 120s and the app showed "agent: read: ... i/o
// timeout". The build now runs in a goroutine; apply returns at once and says
// whether a build is running, just finished, or not needed.

type pythonBuildState int

const (
	pythonBuildNotNeeded pythonBuildState = iota // venv, requirements and app server already in place
	pythonBuildRunning                           // a build is in progress
	pythonBuildFinished                          // a build succeeded since the last call: restart the app
)

// pythonBuildTimeout bounds one whole background build. Each pip step also has
// its own 8-minute cap in runAsUserInDir.
const pythonBuildTimeout = 30 * time.Minute

type pythonBuild struct {
	done     chan struct{}
	err      error
	cancel   context.CancelFunc
	username string
}

// pythonBuilds holds at most one build per app. An entry stays until a later
// apply call collects its result, or app.python.remove cancels it. An agent
// restart drops the map and kills the build (KillMode=control-group); the next
// apply starts it again, and pip picks up what it already installed.
var pythonBuilds = struct {
	sync.Mutex
	m map[string]*pythonBuild
}{m: map[string]*pythonBuild{}}

// pythonBuildSlots caps how many builds run at once across all accounts. The
// reconciler used to run them one at a time inside its loop; without a cap,
// one tick could start a pip for every app on the box. A build waiting for a
// slot reports building like a running one.
var pythonBuildSlots = make(chan struct{}, 3)

// pythonUserSlots allows one build per account at a time, so a single account
// (whose requirements decide how long pip runs, up to the 8-minute step cap)
// can hold at most one of the box-wide slots and never starve the others.
// Guarded by pythonBuilds' lock.
var pythonUserSlots = map[string]chan struct{}{}

func pythonUserSlot(username string) chan struct{} {
	ch, ok := pythonUserSlots[username]
	if !ok {
		ch = make(chan struct{}, 1)
		pythonUserSlots[username] = ch
	}
	return ch
}

// pythonBuildPlan is what one build has to do, decided before it starts.
type pythonBuildPlan struct {
	venv, server, pip string
	createVenv        bool
	reqPath, reqSHA   string // reqSHA != "": install requirements.txt with this content
	reqMarker         string // sha of the last successful requirements install
	reqFailMarker     string // cached failure for the backoff
}

func (pl pythonBuildPlan) needed() bool {
	return pl.createVenv || pl.reqSHA != "" || !fileExists(filepath.Join(pl.venv, "bin", pl.server))
}

// pythonAppBuildStep collects a finished build, reports a running one, or
// starts one when the venv, requirements or app server need work. A failed
// build, or a cached requirements failure inside its backoff, comes back as
// the error.
func pythonAppBuildStep(p pythonAppApplyParams, appRoot, venv, server, stateDir string) (pythonBuildState, error) {
	pythonBuilds.Lock()
	if b, ok := pythonBuilds.m[p.AppID]; ok {
		defer pythonBuilds.Unlock()
		select {
		case <-b.done:
			delete(pythonBuilds.m, p.AppID)
			if b.err != nil {
				return pythonBuildNotNeeded, b.err
			}
			return pythonBuildFinished, nil
		default:
			return pythonBuildRunning, nil
		}
	}
	pythonBuilds.Unlock()

	// Planning reads the tenant's requirements.txt, so it runs outside the
	// lock: a slow read must not hold up every other app's apply.
	pl, err := planPythonBuild(p, appRoot, venv, server, stateDir)
	if err != nil {
		return pythonBuildNotNeeded, err
	}
	if !pl.needed() {
		return pythonBuildNotNeeded, nil
	}

	pythonBuilds.Lock()
	defer pythonBuilds.Unlock()
	if _, ok := pythonBuilds.m[p.AppID]; ok {
		return pythonBuildRunning, nil // another call started it meanwhile
	}
	ctx, cancel := context.WithTimeout(context.Background(), pythonBuildTimeout)
	b := &pythonBuild{done: make(chan struct{}), cancel: cancel, username: p.Username}
	pythonBuilds.m[p.AppID] = b
	userSlot := pythonUserSlot(p.Username)
	go func() {
		defer cancel()
		defer close(b.done)
		notStarted := func() {
			b.err = &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "python app build did not start: " + ctx.Err().Error()}
		}
		select {
		case userSlot <- struct{}{}:
			defer func() { <-userSlot }()
		case <-ctx.Done():
			notStarted()
			return
		}
		select {
		case pythonBuildSlots <- struct{}{}:
			defer func() { <-pythonBuildSlots }()
		case <-ctx.Done():
			notStarted()
			return
		}
		b.err = runPythonBuild(ctx, p, pl)
	}()
	return pythonBuildRunning, nil
}

// cancelPythonBuildsForUser stops every build running as username, forgets
// them, and waits up to wait for them to exit. user.delete calls it before
// userdel.
func cancelPythonBuildsForUser(username string, wait time.Duration) {
	pythonBuilds.Lock()
	var stopping []chan struct{}
	for id, b := range pythonBuilds.m {
		if b.username == username {
			b.cancel()
			stopping = append(stopping, b.done)
			delete(pythonBuilds.m, id)
		}
	}
	pythonBuilds.Unlock()
	timeout := time.After(wait)
	for _, done := range stopping {
		select {
		case <-done:
		case <-timeout:
			return
		}
	}
}

// cancelPythonBuild stops an app's build, if one is running, and forgets it.
// app.python.remove calls it so pip doesn't keep installing into an app that
// is being deleted.
func cancelPythonBuild(appID string) {
	pythonBuilds.Lock()
	defer pythonBuilds.Unlock()
	if b, ok := pythonBuilds.m[appID]; ok {
		b.cancel()
		delete(pythonBuilds.m, appID)
	}
}

func planPythonBuild(p pythonAppApplyParams, appRoot, venv, server, stateDir string) (pythonBuildPlan, error) {
	pl := pythonBuildPlan{
		venv:          venv,
		server:        server,
		pip:           filepath.Join(venv, "bin", "pip"),
		createVenv:    !fileExists(filepath.Join(venv, "bin", "python")),
		reqMarker:     filepath.Join(stateDir, p.AppID+".reqsha"),
		reqFailMarker: filepath.Join(stateDir, p.AppID+".reqfail"),
	}
	// requirements.txt (optional): only (re)install when its content changed
	// since the last SUCCESSFUL install. The marker records the sha256 we last
	// installed; a converged app skips pip entirely, and the tenant editing
	// requirements.txt is what re-triggers it.
	reqPath := filepath.Join(appRoot, "requirements.txt")
	if !fileExists(reqPath) {
		return pl, nil
	}
	sum, err := fileSHA256(reqPath)
	if err != nil {
		return pl, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("hash requirements.txt: %v", err)}
	}
	prev, _ := os.ReadFile(pl.reqMarker)
	if strings.TrimSpace(string(prev)) == sum {
		return pl, nil
	}
	// GH #357: a deterministically-failing build must not re-run pip on
	// every converge tick (~60s) — one broken app stormed pip ~95
	// times/hour. If the last FAILED attempt was for these SAME
	// requirements and we're still inside the backoff, surface the
	// cached error without touching pip. A requirements.txt edit (new
	// sha) or the backoff elapsing retries.
	if f := readReqFail(pl.reqFailMarker); f.SHA == sum && time.Since(time.Unix(f.At, 0)) < pipRetryBackoff {
		return pl, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: f.Error}
	}
	pl.reqPath, pl.reqSHA = reqPath, sum
	return pl, nil
}

// runPythonBuild does the work as the owning user. pip gets
// --disable-pip-version-check because sudo's env_reset drops the
// PIP_DISABLE_PIP_VERSION_CHECK that runAsUserInDir sets, so pip still checked
// PyPI and appended a "[notice] A new release of pip" line to its output.
func runPythonBuild(ctx context.Context, p pythonAppApplyParams, pl pythonBuildPlan) error {
	if pl.createVenv {
		// GH #357: `python<ver> -m venv` needs the version-specific
		// python<ver>-venv package on Debian/Ubuntu — the interpreter binary can
		// be present without it, and venv then dies with "ensurepip is not
		// available", leaving the app stuck pending->failed. Ensure it first.
		if err := ensurePythonVenvPackage(ctx, p.PythonVersion); err != nil {
			return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("python venv prerequisite: %v", err)}
		}
		if out, err := runAsUser(ctx, p.Username, "python"+p.PythonVersion, "-m", "venv", pl.venv); err != nil {
			return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("create venv: %v: %s", err, out)}
		}
	}
	if pl.reqSHA != "" {
		// No -q: quiet mode hides the "Collecting <pkg>" lines that name the
		// package pip was building when it failed, so a build error came back
		// as a bare traceback ("KeyError: '__version__'") with no clue which
		// dependency broke (GH #357). Success output is discarded, so the
		// only effect of dropping -q is a more useful failure message.
		if out, err := runAsUser(ctx, p.Username, pl.pip, "install", "--disable-pip-version-check", "-r", pl.reqPath); err != nil {
			msg := fmt.Sprintf("pip install requirements: %v: %s", err, pipFailureContext(out))
			writeReqFail(pl.reqFailMarker, pl.reqSHA, msg, time.Now())
			return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: msg}
		}
		_ = os.WriteFile(pl.reqMarker, []byte(pl.reqSHA), 0o640)
		_ = os.Remove(pl.reqFailMarker) // clear the cached failure on success
	}
	// App server: install only when its binary is missing from the venv.
	// Checked here, after requirements.txt, which may already have installed it.
	if !fileExists(filepath.Join(pl.venv, "bin", pl.server)) {
		if out, err := runAsUser(ctx, p.Username, pl.pip, "install", "--disable-pip-version-check", pl.server); err != nil {
			return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("pip install %s: %v: %s", pl.server, err, pipFailureContext(out))}
		}
	}
	return nil
}
