package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
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
	pyBin := "python" + p.PythonVersion

	// 1) venv (as the user), idempotent.
	if _, err := os.Stat(filepath.Join(venv, "bin", "python")); err != nil {
		// GH #357: `python<ver> -m venv` needs the version-specific
		// python<ver>-venv package on Debian/Ubuntu — the interpreter binary can
		// be present without it, and venv then dies with "ensurepip is not
		// available", leaving the app stuck pending->failed. Ensure it first.
		if verr := ensurePythonVenvPackage(ctx, p.PythonVersion); verr != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("python venv prerequisite: %v", verr)}
		}
		if out, err := runAsUser(ctx, p.Username, pyBin, "-m", "venv", venv); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("create venv: %v: %s", err, out)}
		}
	}

	// 2) deps + app server (as the user). requirements.txt is optional.
	server := "gunicorn"
	if p.AppType == "asgi" {
		server = "uvicorn"
	}
	pip := filepath.Join(venv, "bin", "pip")
	if _, err := os.Stat(filepath.Join(appRoot, "requirements.txt")); err == nil {
		if out, err := runAsUser(ctx, p.Username, pip, "install", "-q", "-r", filepath.Join(appRoot, "requirements.txt")); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("pip install requirements: %v: %s", err, lastLines(out, 12))}
		}
	}
	if out, err := runAsUser(ctx, p.Username, pip, "install", "-q", server); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("pip install %s: %v: %s", server, err, lastLines(out, 8))}
	}

	// 3) EnvironmentFile (root-owned 0640; values never logged).
	if err := os.MkdirAll(pythonAppEnvDir, 0o750); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("mkdir env dir: %v", err)}
	}
	envPath := filepath.Join(pythonAppEnvDir, p.AppID+".env")
	if err := os.WriteFile(envPath, []byte(renderEnvFile(p)), 0o640); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write env file: %v", err)}
	}

	// 4) systemd unit.
	start := p.StartCommand
	if strings.TrimSpace(start) == "" {
		start = derivePythonStart(venv, p, server)
	}
	unitPath := filepath.Join(pythonAppUnitDir, pythonAppUnitName(p.AppID))
	if err := os.WriteFile(unitPath, []byte(renderPythonUnit(p, appRoot, envPath, start)), 0o644); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write unit: %v", err)}
	}

	// 5) reload + enable + restart.
	_ = exec.CommandContext(ctx, "systemctl", "daemon-reload").Run()
	unit := pythonAppUnitName(p.AppID)
	_ = exec.CommandContext(ctx, "systemctl", "enable", "--quiet", unit).Run()
	if out, err := exec.CommandContext(ctx, "systemctl", "restart", unit).CombinedOutput(); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("restart %s: %v: %s", unit, err, strings.TrimSpace(string(out)))}
	}

	active := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", unit).Run() == nil
	res := pythonAppApplyResult{Active: active, Unit: unit}
	if !active {
		// Capture WHY it isn't active so the panel shows the real startup error
		// instead of only "not active — check journalctl" (GH #357).
		// --output=cat drops syslog metadata; last 15 lines keep it bounded.
		if out, jerr := exec.CommandContext(ctx, "journalctl", "-u", unit, "-n", "20", "--no-pager", "--output=cat").CombinedOutput(); jerr == nil {
			res.Detail = lastLines(strings.TrimSpace(string(out)), 15)
		}
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
	cmd := exec.CommandContext(ctx, "sudo", full...)
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

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
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
	if err := exec.CommandContext(ctx, pyBin, "-c", "import ensurepip").Run(); err == nil {
		return nil // venv already works for this interpreter.
	}
	pkg := "python" + ver + "-venv"
	cmd := exec.CommandContext(ctx, "systemd-run",
		"--pipe", "--wait", "--quiet", "--collect",
		"--unit=jabali-python-venv-install",
		"--service-type=oneshot", "--",
		"bash", "-c", "DEBIAN_FRONTEND=noninteractive apt-get install -y -q "+pkg)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("install %s: %v: %s", pkg, err, strings.TrimSpace(string(out)))
	}
	if err := exec.CommandContext(ctx, pyBin, "-c", "import ensurepip").Run(); err != nil {
		return fmt.Errorf("%s still cannot create virtualenvs after installing %s (ensurepip unavailable)", pyBin, pkg)
	}
	return nil
}
