package commands

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/filesafe"
)

// app.python.apply (ADR-0131 / GH #203) — converge one Python app's runtime:
// ensure the virtualenv, install deps + the app server, render the per-app
// systemd unit (binding 127.0.0.1:<port>), and (re)start it. nginx is NOT
// touched here — the panel proxies via the existing proxy_pass nginx rule.
//
// Everything that touches the app tree (venv, pip) runs AS THE OWNING USER
// (sudo -u). The unit runs User=<owner>. app_root is scope-validated under
// the owner's home.

const (
	pythonAppUnitDir = "/etc/systemd/system"
	pythonAppEnvDir  = "/etc/jabali/python-apps"
)

var pythonVersionRe = regexp.MustCompile(`^3\.(?:[0-9]|1[0-9])$`)

// appIDRe / usernameRe guard the values embedded into root-written systemd
// unit + EnvironmentFile paths and directives. The agent is the security
// boundary for these root ops — never trust the caller's app_id/username
// shape, even though panel-api sends ULIDs.
var appIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var usernameRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
var memLimitRe = regexp.MustCompile(`^[0-9]+[KMGT]?$`)
var cpuLimitRe = regexp.MustCompile(`^[0-9]+%$`)
var baseURIRe = regexp.MustCompile(`^/[A-Za-z0-9._/-]*$`)

func hasCtrl(s string) bool {
	for _, c := range s {
		if c == '\n' || c == '\r' || c == 0 {
			return true
		}
	}
	return false
}

type pythonAppApplyParams struct {
	AppID         string            `json:"app_id"`
	Username      string            `json:"username"`
	UserID        string            `json:"user_id"`
	AppRoot       string            `json:"app_root"`
	PythonVersion string            `json:"python_version"`
	AppType       string            `json:"app_type"` // wsgi|asgi
	Entrypoint    string            `json:"entrypoint"`
	BaseURI       string            `json:"base_uri"`
	Port          int               `json:"port"`
	StartCommand  string            `json:"start_command,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	MemoryLimit   string            `json:"memory_limit,omitempty"`
	CPULimit      string            `json:"cpu_limit,omitempty"`
	PIDsLimit     int               `json:"pids_limit,omitempty"`
}

type pythonAppApplyResult struct {
	Active bool   `json:"active"`
	Unit   string `json:"unit"`
	// Detail carries the unit's recent journal when the app started but isn't
	// active, so the panel surfaces the real startup error (e.g. gunicorn
	// failing to import the entrypoint) instead of a generic "not active" — the
	// exact "nothing in logs" wall from GH #357.
	Detail string `json:"detail,omitempty"`
	// Hint names the account's Max tasks limit when the app is not active
	// because its slice is full (GH #1820). Kept apart from Detail, which the
	// panel presents as journal output.
	Hint string `json:"hint,omitempty"`
	// Building means the venv/pip build is still running in the background
	// (GH #357); the panel shows the app as building and asks again next tick.
	Building bool `json:"building,omitempty"`
}

func pythonAppUnitName(appID string) string { return "jabali-app-" + appID + ".service" }

func pythonAppApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p pythonAppApplyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if p.AppID == "" || p.Username == "" || p.AppRoot == "" || p.Entrypoint == "" || p.Port == 0 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "app_id, username, app_root, entrypoint and port are required"}
	}
	if !appIDRe.MatchString(p.AppID) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid app_id"}
	}
	if !usernameRe.MatchString(p.Username) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid username"}
	}
	if hasCtrl(p.StartCommand) || hasCtrl(p.Entrypoint) || hasCtrl(p.AppRoot) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "control characters not allowed"}
	}
	if p.BaseURI != "" && !baseURIRe.MatchString(p.BaseURI) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid base_uri"}
	}
	if p.MemoryLimit != "" && !memLimitRe.MatchString(p.MemoryLimit) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid memory_limit"}
	}
	if p.CPULimit != "" && !cpuLimitRe.MatchString(p.CPULimit) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid cpu_limit"}
	}
	if !pythonVersionRe.MatchString(p.PythonVersion) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "python_version must be like 3.11"}
	}
	if p.AppType != "wsgi" && p.AppType != "asgi" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "app_type must be wsgi or asgi"}
	}
	// entrypoint is module:callable — letters, digits, dot, underscore, colon.
	if !regexp.MustCompile(`^[A-Za-z0-9_.]+:[A-Za-z0-9_]+$`).MatchString(p.Entrypoint) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "entrypoint must be module:callable"}
	}

	// Scope-validate app_root under the owner's home.
	homeDir := "/home/" + p.Username
	scope, err := filesafe.NewScope(p.UserID, p.Username, []string{homeDir})
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("scope: %v", err)}
	}
	// app_root may arrive home-relative ("domains/x/app") or absolute
	// ("/home/<user>/domains/x/app"); filesafe.Resolve requires absolute.
	appRootIn := p.AppRoot
	if !filepath.IsAbs(appRootIn) {
		appRootIn = filepath.Join(homeDir, appRootIn)
	}
	appRoot, err := scope.Resolve(appRootIn)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("app_root validation failed: %v", err)}
	}
	if fi, err := os.Stat(appRoot); err != nil {
		// Fresh plain app: the dir doesn't exist yet and there is no framework
		// scaffold to create it. Make it AS THE TENANT so the venv + code land
		// tenant-owned, inside the scope-validated path under the owner's home.
		if out, mkerr := runAsUser(ctx, p.Username, "mkdir", "-p", appRoot); mkerr != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("create app_root: %v: %s", mkerr, out)}
		}
	} else if !fi.IsDir() {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "app_root exists but is not a directory"}
	}

	venv := filepath.Join(appRoot, "venv")
	server := "gunicorn"
	if p.AppType == "asgi" {
		server = "uvicorn"
	}

	// The per-app root-owned state dir holds the EnvironmentFile and the
	// requirements-hash marker. Created before the build so the marker can be
	// written.
	if err := os.MkdirAll(pythonAppEnvDir, 0o750); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("mkdir env dir: %v", err)}
	}

	// GH #357: the reconciler re-applies every app on every ~60s tick, so each
	// step here MUST be idempotent/convergent — otherwise a healthy app re-pips
	// and `systemctl restart`s every minute (dropping in-flight requests and
	// resetting systemd's Restart=on-failure backoff), and a doomed app storms
	// pip forever. Gate each expensive/disruptive step on real change and only
	// restart when something actually changed. `needsRestart` accumulates that.
	needsRestart := false

	// 1-2) venv + requirements.txt + app server, in the background. GH #357: a
	// first install of a real requirements.txt takes minutes, longer than the
	// panel's 120s deadline for this call. Run inside the call, pip was killed
	// at the deadline and the app showed "i/o timeout". This call now reports
	// building until the build finishes; the call after that collects its result.
	switch state, err := pythonAppBuildStep(p, appRoot, venv, server, pythonAppEnvDir); {
	case err != nil:
		return nil, err
	case state == pythonBuildRunning:
		return pythonAppApplyResult{Building: true, Unit: pythonAppUnitName(p.AppID)}, nil
	case state == pythonBuildFinished:
		needsRestart = true
	}

	// 3) EnvironmentFile (root-owned 0640; values never logged). ALWAYS write it
	// so the file the unit references exists — renderEnvFile is empty for a
	// minimal ASGI app with no env vars, and systemd hard-fails the unit
	// ("Failed to load environment files: No such file or directory") when the
	// EnvironmentFile is missing. A content change OR a previously-absent file
	// counts as a restart trigger (the latter recovers an app broken by that
	// missing file); an unchanged, present env is a no-op restart-wise.
	envPath := filepath.Join(pythonAppEnvDir, p.AppID+".env")
	envContent := renderEnvFile(p)
	if prev, rerr := os.ReadFile(envPath); os.IsNotExist(rerr) || string(prev) != envContent {
		needsRestart = true
	}
	if err := os.WriteFile(envPath, []byte(envContent), 0o640); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write env file: %v", err)}
	}

	// 4) systemd unit — always write so it exists; a changed (or absent) unit
	// also needs a daemon-reload before restart.
	start := p.StartCommand
	if strings.TrimSpace(start) == "" {
		start = derivePythonStart(venv, p, server)
	}
	unitPath := filepath.Join(pythonAppUnitDir, pythonAppUnitName(p.AppID))
	unitContent := renderPythonUnit(p, appRoot, envPath, start)
	unitChanged := false
	if prev, rerr := os.ReadFile(unitPath); os.IsNotExist(rerr) || string(prev) != unitContent {
		unitChanged = true
		needsRestart = true
	}
	if err := os.WriteFile(unitPath, []byte(unitContent), 0o644); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write unit: %v", err)}
	}

	// 5) reload (only on unit change) + enable (idempotent) + restart (only when
	// something changed). When nothing changed we leave the running process
	// alone — systemd's Restart=on-failure owns crash recovery, and the Restart
	// button drives a separate app.python.control call.
	unit := pythonAppUnitName(p.AppID)
	if unitChanged {
		_ = execCommandContext(ctx, "systemctl", "daemon-reload").Run()
	}
	_ = execCommandContext(ctx, "systemctl", "enable", "--quiet", unit).Run()
	if needsRestart {
		if out, err := execCommandContext(ctx, "systemctl", "restart", unit).CombinedOutput(); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("restart %s: %v: %s%s", unit, err, strings.TrimSpace(string(out)), tasksLimitSuffix(ctx, unit))}
		}
	}

	active := execCommandContext(ctx, "systemctl", "is-active", "--quiet", unit).Run() == nil
	res := pythonAppApplyResult{Active: active, Unit: unit}
	if !active {
		// Capture WHY it isn't active so the panel shows the real startup error
		// instead of only "not active — check journalctl" (GH #357).
		// --output=cat drops syslog metadata; last 15 lines keep it bounded.
		if out, jerr := execCommandContext(ctx, "journalctl", "-u", unit, "-n", "20", "--no-pager", "--output=cat").CombinedOutput(); jerr == nil {
			res.Detail = lastLines(strings.TrimSpace(string(out)), 15)
		}
		res.Hint = tasksLimitHint(ctx, unit)
	}
	return res, nil
}

// derivePythonStart builds the gunicorn (WSGI) / uvicorn (ASGI) command bound
// to the loopback port. base_uri != "/" is threaded as SCRIPT_NAME (gunicorn)
// or --root-path (uvicorn) so the app generates correct sub-path URLs.
func derivePythonStart(venv string, p pythonAppApplyParams, server string) string {
	bin := filepath.Join(venv, "bin", server)
	bind := fmt.Sprintf("127.0.0.1:%d", p.Port)
	prefix := strings.TrimRight(p.BaseURI, "/")
	if p.AppType == "asgi" {
		cmd := fmt.Sprintf("%s --host 127.0.0.1 --port %d %s", bin, p.Port, p.Entrypoint)
		if prefix != "" {
			cmd += " --root-path " + prefix
		}
		return cmd
	}
	// gunicorn (WSGI). SCRIPT_NAME goes in the EnvironmentFile, not here.
	return fmt.Sprintf("%s --workers 3 --bind %s %s", bin, bind, p.Entrypoint)
}

func renderEnvFile(p pythonAppApplyParams) string {
	var b strings.Builder
	if p.AppType == "wsgi" {
		if prefix := strings.TrimRight(p.BaseURI, "/"); prefix != "" {
			fmt.Fprintf(&b, "SCRIPT_NAME=%s\n", prefix)
		}
	}
	for k, v := range p.Env {
		if !validEnvKey(k) {
			continue
		}
		// systemd EnvironmentFile: KEY=value, no interpolation, one line.
		fmt.Fprintf(&b, "%s=%s\n", k, strings.ReplaceAll(v, "\n", " "))
	}
	return b.String()
}

func renderPythonUnit(p pythonAppApplyParams, appRoot, envPath, start string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[Unit]\nDescription=Jabali Python app %s\nAfter=network.target\n\n", p.AppID)
	b.WriteString("[Service]\n")
	fmt.Fprintf(&b, "User=%s\nGroup=%s\n", p.Username, p.Username)
	// Place the app inside the owner's M18 user slice (ADR-0131, Gitea #490) so
	// the package's cgroup CPU/memory/PID limits apply to it via the slice,
	// independent of the optional per-app caps below. systemd nests it under
	// jabali.slice/jabali-user.slice automatically via the slice unit.
	fmt.Fprintf(&b, "Slice=jabali-user-%s.slice\n", p.Username)
	fmt.Fprintf(&b, "WorkingDirectory=%s\n", appRoot)
	fmt.Fprintf(&b, "EnvironmentFile=%s\n", envPath)
	fmt.Fprintf(&b, "ExecStart=%s\n", start)
	b.WriteString("Restart=on-failure\nRestartSec=3\nNoNewPrivileges=true\nPrivateTmp=true\n")
	if p.MemoryLimit != "" {
		fmt.Fprintf(&b, "MemoryMax=%s\n", p.MemoryLimit)
	}
	if p.CPULimit != "" {
		fmt.Fprintf(&b, "CPUQuota=%s\n", p.CPULimit)
	}
	if p.PIDsLimit > 0 {
		fmt.Fprintf(&b, "TasksMax=%d\n", p.PIDsLimit)
	}
	b.WriteString("\n[Install]\nWantedBy=multi-user.target\n")
	return b.String()
}

// runAsUser runs a command as the hosting user via sudo, capturing output.
func runAsUser(ctx context.Context, username string, args ...string) (string, error) {
	return runAsUserInDir(ctx, username, "", args...)
}

// runAsUserInDir is runAsUser with an explicit working directory. Needed by
// scaffolds whose commands resolve relative paths (e.g. `django-admin
// startproject config .` writes manage.py into the cwd) — without it the child
// inherits the agent's cwd and writes to the wrong place. dir="" keeps the
// inherited cwd. dir must be a tenant-owned path the user can chdir into.
func runAsUserInDir(ctx context.Context, username, dir string, args ...string) (string, error) {
	full := append([]string{"-u", username, "-H"}, args...)
	cmd := execCommandContext(ctx, "sudo", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PIP_DISABLE_PIP_VERSION_CHECK=1")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	// pip installs can be slow on first run.
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return "", err
	}
	go func() { done <- cmd.Wait() }()
	select {
	case <-time.After(8 * time.Minute):
		_ = cmd.Process.Kill()
		return out.String(), fmt.Errorf("timed out")
	case err := <-done:
		return out.String(), err
	}
}

func validEnvKey(k string) bool {
	if k == "" {
		return false
	}
	for _, c := range k {
		if !(c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

// fileSHA256 returns the hex sha256 of a file's contents. Used to skip
// re-running `pip install -r requirements.txt` when the file is unchanged
// since the last successful install (GH #357 per-tick pip storm).
// maxRequirementsBytes caps how much of a tenant's requirements.txt the agent
// (running as root) will read to hash it. Real files are a few KB.
const maxRequirementsBytes = 1 << 20

// fileSHA256 hashes a tenant-controlled file. The tenant decides what the path
// is, so only a regular file of at most maxRequirementsBytes is read: a FIFO
// would block the read forever and a symlink to /dev/zero would never end. The
// open is non-blocking so a FIFO swapped in after a stat can't block it either.
func fileSHA256(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, maxRequirementsBytes+1))
	if err != nil {
		return "", err
	}
	if n > maxRequirementsBytes {
		return "", fmt.Errorf("%s is larger than %d bytes", filepath.Base(path), maxRequirementsBytes)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// pipRetryBackoff caps how often a DETERMINISTICALLY-failing `pip install` is
// retried for the SAME requirements. Without it the reconciler re-ran pip every
// converge tick (~60s) forever on an app whose deps can't build (GH #357: ~95
// failures/hour observed). A requirements.txt edit clears the failure and
// retries immediately regardless of this backoff.
const pipRetryBackoff = 15 * time.Minute

// reqFailState is the cached record of the last failed `pip install -r
// requirements.txt`, so a re-converge for the same requirements can surface the
// error without re-running pip (GH #357 pip-storm).
type reqFailState struct {
	SHA   string `json:"sha"`   // sha256 of requirements.txt at the failed attempt
	Error string `json:"error"` // the message to surface while backed off
	At    int64  `json:"at"`    // unix seconds of the last attempt
}

func readReqFail(path string) reqFailState {
	var s reqFailState
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s)
	}
	return s
}

func writeReqFail(path, sha, errText string, now time.Time) {
	b, err := json.Marshal(reqFailState{SHA: sha, Error: errText, At: now.Unix()})
	if err != nil {
		return
	}
	_ = os.WriteFile(path, b, 0o640)
}

// pipFailureContext pulls the package-identifying lines out of pip's output
// (the "Collecting <pkg>" / "Building wheel for <pkg>" lines pip prints while
// processing the dep it then failed on) and appends the tail, so the stored
// error names WHICH dependency broke — the plain last-N-lines tail dropped it
// (GH #357). Requires pip to run without -q (quiet hides those lines).
func pipFailureContext(out string) string {
	var ids []string
	for _, ln := range strings.Split(out, "\n") {
		l := strings.TrimSpace(ln)
		if strings.HasPrefix(l, "Collecting ") ||
			strings.HasPrefix(l, "Building wheel for ") ||
			strings.HasPrefix(l, "Failed building wheel for ") ||
			strings.HasPrefix(l, "ERROR: Could not build wheels for") ||
			strings.Contains(l, "wheel for ") {
			ids = append(ids, l)
		}
	}
	// Keep the last few — pip processes deps in order, so the tail of this list
	// is the package it was on when it failed.
	if len(ids) > 4 {
		ids = ids[len(ids)-4:]
	}
	tail := lastLines(out, 12)
	if len(ids) == 0 {
		return tail
	}
	return strings.Join(ids, "\n") + "\n...\n" + tail
}

func init() {
	Default.Register("app.python.apply", pythonAppApplyHandler)
}

// ensurePythonVenvPackage makes `python<ver> -m venv` usable. On Debian/Ubuntu
// that requires the version-specific python<ver>-venv package, and the
// interpreter binary can be installed without it — venv then fails with
// "ensurepip is not available" (GH #357). Probe ensurepip first so this is a
// no-op on a healthy box; only apt-install when it is actually missing. The apt
// call runs root-side via systemd-run to escape the agent's PrivateTmp mount ns
// (same pattern as install_python_apps_runtime). Idempotent.
func ensurePythonVenvPackage(ctx context.Context, ver string) error {
	pyBin := "python" + ver
	if err := execCommandContext(ctx, pyBin, "-c", "import ensurepip").Run(); err == nil {
		return nil // venv already works for this interpreter.
	}
	pkg := "python" + ver + "-venv"
	cmd := execCommandContext(ctx, "systemd-run",
		"--pipe", "--wait", "--quiet", "--collect",
		"--unit=jabali-python-venv-install",
		"--service-type=oneshot", "--",
		"bash", "-c", "DEBIAN_FRONTEND=noninteractive apt-get install -y -q "+pkg)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("install %s: %v: %s", pkg, err, strings.TrimSpace(string(out)))
	}
	if err := execCommandContext(ctx, pyBin, "-c", "import ensurepip").Run(); err != nil {
		return fmt.Errorf("%s still cannot create virtualenvs after installing %s (ensurepip unavailable)", pyBin, pkg)
	}
	return nil
}
