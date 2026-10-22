package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/filesafe"
)

// app.node.apply (ADR-0175) — converge one Node.js app: install its npm
// dependencies (and run its build script) as the owning user, render the
// per-app systemd unit and (re)start it. Same unit shape, slice, env dir,
// build slots and lifecycle commands (app.python.control/logs/remove) as a
// Python app; nginx is the panel's proxy_pass rule to 127.0.0.1:<port>.
//
// The app listens on the PORT and HOST the unit's EnvironmentFile sets. They
// are written after the tenant's own variables, so the tenant cannot move the
// app off its allocated port.

// nodeAppVersion is the only Node.js major apps run on: the host's NodeSource
// node_22.x, which the system's security updates keep patched (ADR-0175).
const nodeAppVersion = "22"

// The host's Node.js and npm. Vars so tests can point them elsewhere.
var (
	nodeBin = "/usr/bin/node"
	npmBin  = "/usr/bin/npm"
)

// maxNodeManifestBytes caps package.json and the lockfile the agent reads.
// A lockfile of a large project runs to several megabytes.
const maxNodeManifestBytes = 32 << 20

// nodeEntrypointRe: a script path under the app root (node app type), with no
// "." or ".." segment.
var nodeEntrypointRe = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*(/[A-Za-z0-9_-][A-Za-z0-9._-]*)*\.(js|mjs|cjs)$`)

// npmScriptRe: the package.json script the npm app type runs.
var npmScriptRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.-]{0,63}$`)

type nodeAppApplyParams struct {
	AppID       string            `json:"app_id"`
	Username    string            `json:"username"`
	UserID      string            `json:"user_id"`
	AppRoot     string            `json:"app_root"`
	NodeVersion string            `json:"node_version"`
	AppType     string            `json:"app_type"`   // node | npm
	Entrypoint  string            `json:"entrypoint"` // node: script path; npm: script name
	Port        int               `json:"port"`
	Env         map[string]string `json:"env,omitempty"`
	MemoryLimit string            `json:"memory_limit,omitempty"`
	CPULimit    string            `json:"cpu_limit,omitempty"`
	PIDsLimit   int               `json:"pids_limit,omitempty"`
}

func validateNodeAppParams(p nodeAppApplyParams) error {
	bad := func(msg string) error {
		return &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: msg}
	}
	if p.AppID == "" || p.Username == "" || p.AppRoot == "" || p.Entrypoint == "" || p.Port == 0 {
		return bad("app_id, username, app_root, entrypoint and port are required")
	}
	if !appIDRe.MatchString(p.AppID) {
		return bad("invalid app_id")
	}
	if !usernameRe.MatchString(p.Username) {
		return bad("invalid username")
	}
	if p.Port < 1 || p.Port > 65535 {
		return bad("invalid port")
	}
	if hasCtrl(p.AppRoot) {
		return bad("control characters not allowed")
	}
	if p.NodeVersion != nodeAppVersion {
		return bad("node_version must be " + nodeAppVersion)
	}
	switch p.AppType {
	case "node":
		if !nodeEntrypointRe.MatchString(p.Entrypoint) {
			return bad("entrypoint must be a .js, .mjs or .cjs file under the app root")
		}
	case "npm":
		if !npmScriptRe.MatchString(p.Entrypoint) {
			return bad("entrypoint must be the name of a package.json script")
		}
	default:
		return bad("app_type must be node or npm")
	}
	if p.MemoryLimit != "" && !memLimitRe.MatchString(p.MemoryLimit) {
		return bad("invalid memory_limit")
	}
	if p.CPULimit != "" && !cpuLimitRe.MatchString(p.CPULimit) {
		return bad("invalid cpu_limit")
	}
	return nil
}

func nodeAppApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p nodeAppApplyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if err := validateNodeAppParams(p); err != nil {
		return nil, err
	}
	for _, bin := range []string{nodeBin, npmBin} {
		if !fileExists(bin) {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "Node.js is not installed on this server (" + bin + " is missing)"}
		}
	}
	appRoot, scope, err := resolveAppRoot(ctx, p.UserID, p.Username, p.AppRoot)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(pythonAppEnvDir, 0o750); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("mkdir env dir: %v", err)}
	}

	// Each step converges and restarts the app only when something changed:
	// the reconciler applies every app every ~60s (GH #357).
	needsRestart := false
	switch state, err := nodeAppBuildStep(p, scope, appRoot, pythonAppEnvDir); {
	case err != nil:
		return nil, err
	case state == pythonBuildRunning:
		return pythonAppApplyResult{Building: true, Unit: pythonAppUnitName(p.AppID)}, nil
	case state == pythonBuildFinished:
		needsRestart = true
	}

	envPath := filepath.Join(pythonAppEnvDir, p.AppID+".env")
	res, err := applyAppUnit(ctx, p.AppID, envPath, renderNodeEnvFile(p), renderNodeUnit(p, appRoot, envPath), needsRestart)
	if err != nil {
		return nil, err
	}
	// A running app that does not listen on its port serves nothing: say so,
	// on a call that did not just start it (a fresh start may not have bound
	// the port yet).
	if res.Active && !res.Restarted && !nodeAppListening(p.Port) {
		res.Active = false
		res.Hint = fmt.Sprintf("it is not listening on 127.0.0.1:%d (a Node.js app has to listen on the port in the PORT environment variable)", p.Port)
	}
	return res, nil
}

// nodeAppListening reports whether something accepts connections on the app's
// loopback port. A var so tests can stub it.
var nodeAppListening = func(port int) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 2*time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// renderNodeEnvFile writes the tenant's variables, then the ones the unit
// depends on. NODE_ENV comes first, so an app can set its own; PORT and HOST
// come last, and in an EnvironmentFile the later line wins.
func renderNodeEnvFile(p nodeAppApplyParams) string {
	var b strings.Builder
	b.WriteString("NODE_ENV=production\n")
	writeEnvLines(&b, p.Env, func(k string) bool { return validEnvKey(k) && k != "PORT" && k != "HOST" })
	fmt.Fprintf(&b, "PORT=%d\nHOST=127.0.0.1\n", p.Port)
	return b.String()
}

func renderNodeUnit(p nodeAppApplyParams, appRoot, envPath string) string {
	start := nodeBin + " " + p.Entrypoint
	if p.AppType == "npm" {
		start = npmBin + " run " + p.Entrypoint
	}
	return renderAppUnit(appUnitSpec{
		Runtime: "Node.js", AppID: p.AppID, Username: p.Username, AppRoot: appRoot, EnvPath: envPath,
		Start: start, MemoryLimit: p.MemoryLimit, CPULimit: p.CPULimit, PIDsLimit: p.PIDsLimit,
	})
}

// nodeBuildPlan is what one npm build has to do, decided before it starts.
type nodeBuildPlan struct {
	scope      *filesafe.Scope
	appRoot    string
	sha        string // != "": install for package.json + lockfile with this content
	lockfile   bool   // npm ci, rather than npm install
	hasBuild   bool   // package.json has a build script: dev dependencies + npm run build
	marker     string // sha of the last successful build
	failMarker string // cached failure for the backoff
}

func nodeAppBuildStep(p nodeAppApplyParams, scope *filesafe.Scope, appRoot, stateDir string) (pythonBuildState, error) {
	var pl nodeBuildPlan
	plan := func() (bool, error) {
		var err error
		pl, err = planNodeBuild(p, scope, appRoot, stateDir)
		return err == nil && pl.sha != "", err
	}
	run := func(ctx context.Context) error { return runNodeBuild(ctx, p, pl) }
	return appBuildStep(p.AppID, p.Username, plan, run)
}

// planNodeBuild decides whether npm has to run: when package.json or the
// lockfile changed since the last successful build. An app with no
// package.json (a plain node script) has nothing to install. The files are
// read through the app root opened beneath the owner's home (scope).
func planNodeBuild(p nodeAppApplyParams, scope *filesafe.Scope, appRoot, stateDir string) (nodeBuildPlan, error) {
	pl := nodeBuildPlan{
		scope:      scope,
		appRoot:    appRoot,
		marker:     filepath.Join(stateDir, p.AppID+".npmsha"),
		failMarker: filepath.Join(stateDir, p.AppID+".npmfail"),
	}
	dir, err := scope.OpenDirInScope(appRoot)
	if err != nil {
		return pl, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("open app root: %v", err)}
	}
	defer dir.Close()
	pkg, sum, lockfile, err := nodeManifests(dir)
	if errors.Is(err, os.ErrNotExist) {
		if p.AppType == "npm" {
			return pl, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "package.json not found in the app root; the npm app type runs one of its scripts"}
		}
		return pl, nil
	}
	if err != nil {
		return pl, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	pl.lockfile = lockfile
	if prev, _ := os.ReadFile(pl.marker); strings.TrimSpace(string(prev)) == sum {
		return pl, nil
	}
	// A build that failed for these same files is not retried on every tick:
	// the cached error comes back until the files change or the backoff ends.
	if f := readReqFail(pl.failMarker); f.SHA == sum && time.Since(time.Unix(f.At, 0)) < pipRetryBackoff {
		return pl, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: f.Error}
	}
	if pl.hasBuild, err = packageHasScript(pkg, "build"); err != nil {
		return pl, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}
	pl.sha = sum
	return pl, nil
}

// nodeManifests reads package.json and the lockfile, if any, in the app root
// dir and hashes them. os.ErrNotExist: no package.json.
func nodeManifests(dir *os.File) (pkg []byte, sum string, lockfile bool, err error) {
	pkg, err = readTenantFile(dir, "package.json", maxNodeManifestBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", false, err
	}
	if err != nil {
		return nil, "", false, fmt.Errorf("read package.json: %w", err)
	}
	h := sha256.New()
	h.Write(pkg)
	for _, name := range []string{"package-lock.json", "npm-shrinkwrap.json"} {
		lock, err := readTenantFile(dir, name, maxNodeManifestBytes)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, "", false, fmt.Errorf("read %s: %w", name, err)
		}
		h.Write([]byte("\x00" + name + "\x00"))
		h.Write(lock)
		lockfile = true
	}
	return pkg, hex.EncodeToString(h.Sum(nil)), lockfile, nil
}

// runNodeBuild installs the dependencies as the owning user in the app root,
// in the owner's slice: npm ci with a lockfile, npm install without. Install
// scripts run (native modules need them), as the tenant, in the slice and
// under the egress firewall. An app with a build script gets its dev
// dependencies and npm run build; one without gets production dependencies
// only. npm's cache is .npm in the app root: an account's home directory is
// root-owned (chroot SFTP), so npm cannot create ~/.npm. The cache and npm's
// notices are set on the command line: sudo's env_reset drops npm_config_*
// variables.
func runNodeBuild(ctx context.Context, p nodeAppApplyParams, pl nodeBuildPlan) error {
	fail := func(step, out string, err error) error {
		msg := fmt.Sprintf("%s: %v: %s", step, err, lastLines(strings.TrimSpace(out), 20))
		writeReqFail(pl.failMarker, pl.sha, msg, time.Now())
		return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: msg}
	}
	install := []string{npmBin, "install"}
	if pl.lockfile {
		install = []string{npmBin, "ci"}
	}
	cache := "--cache=" + filepath.Join(pl.appRoot, ".npm")
	install = append(install, cache, "--no-audit", "--no-fund", "--no-update-notifier")
	if !pl.hasBuild {
		install = append(install, "--omit=dev")
	}
	if out, err := runAsUserInSlice(ctx, p.AppID, p.Username, pl.appRoot, install...); err != nil {
		return fail("npm "+install[1], out, err)
	}
	if pl.hasBuild {
		if out, err := runAsUserInSlice(ctx, p.AppID, p.Username, pl.appRoot, npmBin, "run", "build", cache, "--no-update-notifier"); err != nil {
			return fail("npm run build", out, err)
		}
	}
	// npm install writes a package-lock.json when the app has none: record
	// the manifests as the build left them, or the next apply builds again.
	sha := pl.sha
	if dir, err := pl.scope.OpenDirInScope(pl.appRoot); err == nil {
		if _, sum, _, err := nodeManifests(dir); err == nil {
			sha = sum
		}
		_ = dir.Close()
	}
	_ = os.WriteFile(pl.marker, []byte(sha), 0o640)
	_ = os.Remove(pl.failMarker)
	return nil
}

// packageHasScript reports whether package.json (its content) defines
// scripts[name].
func packageHasScript(b []byte, name string) (bool, error) {
	var pkg struct {
		Scripts map[string]json.RawMessage `json:"scripts"`
	}
	if err := json.Unmarshal(b, &pkg); err != nil {
		return false, errors.New("package.json is not valid JSON")
	}
	_, ok := pkg.Scripts[name]
	return ok, nil
}

// readTenantFile reads the file name in a tenant's directory dir, opened
// beneath the owner's home, without following a symlink or blocking on a
// FIFO, and only when it is a regular file no larger than max.
func readTenantFile(dir *os.File, name string, max int64) ([]byte, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, os.ErrNotExist
		}
		if errors.Is(err, unix.ELOOP) {
			return nil, fmt.Errorf("%s is a symbolic link", name)
		}
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", name)
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", name, max)
	}
	return b, nil
}

func init() {
	Default.Register("app.node.apply", nodeAppApplyHandler)
}
