package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"golang.org/x/sys/unix"
)

// composer_runs.go — Composer runs a tenant starts from the panel (GH #2276,
// ADR-0176).
//
//   composer.detect      where a domain's composer.json is, and what a run needs
//   composer.run_start   start one run as a detached transient unit
//   composer.run_status  the run's state, exit code and log
//   composer.run_stop    cancel a run, or release a finished one
//
// A run is jabali-composer-<run id>.service: the tenant's uid in their slice,
// Type=oneshot with RemainAfterExit so Result and ExecMainStatus outlive the
// process (never --collect, which erases them). It runs Composer in the SSH
// sandbox (`jabali-ssh-shell --exec`, as cron does, GH #1458), because a
// composer.json's scripts and plugins are the tenant's code. The panel
// stores the result, then asks run_stop to release the unit.

const (
	// composerRunLogLines and composerRunLogMax bound the log run_status
	// returns: the last lines of the unit's journal, then its last bytes.
	composerRunLogLines = 5000
	composerRunLogMax   = 256 << 10
	// composerMaxPackages bounds the packages one run names.
	composerMaxPackages = 20
)

// composerSandboxShell runs one command in the tenant's SSH sandbox.
var composerSandboxShell = cronSandboxWrapper

// composerRunProps are the run unit's limits, on top of the user's slice.
// TimeoutStartSec caps the whole run: a oneshot unit is "starting" until its
// command exits. TimeoutStopSec keeps a cancel within seconds.
var composerRunProps = []string{
	"--property=TimeoutStartSec=1800",
	"--property=TimeoutStopSec=10",
	"--property=MemoryMax=2G",
	"--property=TasksMax=256",
	"--property=CPUWeight=50",
	"--property=IOWeight=50",
}

// composerCommand is one Composer command a run may start.
type composerCommand struct {
	// packages is what the run's packages may be: "" none, "names" package
	// names, "require" names with an optional :constraint.
	packages    string
	minPackages int
	noDev       bool     // takes --no-dev
	noProgress  bool     // takes --no-progress
	extra       []string // options the command always gets
}

var composerCommands = map[string]composerCommand{
	"install":       {noDev: true, noProgress: true},
	"update":        {packages: "names", noDev: true, noProgress: true},
	"require":       {packages: "require", minPackages: 1, noProgress: true},
	"remove":        {packages: "names", minPackages: 1, noProgress: true},
	"audit":         {noDev: true},
	"outdated":      {noDev: true},
	"show":          {noDev: true},
	"validate":      {},
	"dump-autoload": {noDev: true, extra: []string{"--optimize"}},
	"diagnose":      {},
}

var (
	// composerPackageRE is a Composer package name (composer-schema.json).
	composerPackageRE = regexp.MustCompile(`^[a-z0-9]([_.-]?[a-z0-9]+)*/[a-z0-9](([_.]|-{1,2})?[a-z0-9]+)*$`)
	// composerConstraintRE is a version constraint after "name:". No $ or %:
	// systemd expands both in ExecStart.
	composerConstraintRE = regexp.MustCompile(`^[A-Za-z0-9.*^~<>=!|, @_+-]{1,64}$`)
	// composerAppDirRE is an app directory relative to the domain's directory.
	composerAppDirRE = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)
	// composerRunBlockedRE is the line PHP Defense logs when it stops a call,
	// in the Composer rules' php log media.
	composerRunBlockedRE = regexp.MustCompile(`\[snuffleupagus\]\[[^\]]*\]\[[^\]]*\]\[drop\]`)
)

func composerRunUnit(runID string) string { return "jabali-composer-" + runID + ".service" }

// composerDomainDir is the directory a domain's files live in.
func composerDomainDir(osUser, domain string) string {
	return filepath.Join(tenantHomeDir(osUser), "domains", domain)
}

// validateComposerAppDir checks an app directory relative to the domain's
// directory; "" is the domain's directory itself.
func validateComposerAppDir(dir string) error {
	if dir == "" {
		return nil
	}
	if len(dir) > 255 || !composerAppDirRE.MatchString(dir) {
		return fmt.Errorf("app_dir must be a relative path of letters, digits, '.', '_', '-' and '/'")
	}
	for _, seg := range strings.Split(dir, "/") {
		if seg == "." || seg == ".." {
			return fmt.Errorf("app_dir may not contain . or .. segments")
		}
	}
	return nil
}

// validateComposerPackages checks a run's packages against its command.
func validateComposerPackages(cmd composerCommand, pkgs []string) error {
	if len(pkgs) > composerMaxPackages {
		return fmt.Errorf("at most %d packages per run", composerMaxPackages)
	}
	if len(pkgs) < cmd.minPackages {
		return fmt.Errorf("name at least %d package", cmd.minPackages)
	}
	if cmd.packages == "" && len(pkgs) > 0 {
		return fmt.Errorf("this command takes no packages")
	}
	for _, p := range pkgs {
		name, constraint, hasConstraint := strings.Cut(p, ":")
		if !composerPackageRE.MatchString(name) {
			return fmt.Errorf("%q is not a vendor/name package name", p)
		}
		if hasConstraint && (cmd.packages != "require" || !composerConstraintRE.MatchString(constraint)) {
			return fmt.Errorf("%q: a version constraint is only allowed with require, and only of letters, digits and . * ^ ~ < > = ! | , @ _ + - and spaces", p)
		}
	}
	return nil
}

// composerStatInHome stats rel under osUser's home without following any
// symlink (RESOLVE_NO_SYMLINKS) or leaving the home (RESOLVE_BENEATH): the
// agent is root and the tenant owns these directories.
func composerStatInHome(osUser, rel string) (unix.Stat_t, error) {
	var st unix.Stat_t
	home, err := os.Open(tenantHomeDir(osUser))
	if err != nil {
		return st, err
	}
	defer home.Close()
	fd, err := unix.Openat2(int(home.Fd()), rel, &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS,
	})
	if err != nil {
		return st, err
	}
	defer unix.Close(fd)
	err = unix.Fstat(fd, &st)
	return st, err
}

func composerIsDir(osUser, rel string) bool {
	st, err := composerStatInHome(osUser, rel)
	return err == nil && st.Mode&unix.S_IFMT == unix.S_IFDIR
}

func composerIsFile(osUser, rel string) bool {
	st, err := composerStatInHome(osUser, rel)
	return err == nil && st.Mode&unix.S_IFMT == unix.S_IFREG
}

// composerRunAccount refuses a run for anyone but a hosting user. The SSH
// sandbox never jails uid 0 (GH #658: jabali-ssh-shell runs a root command
// directly), so a run as root, or as a system account, would run a
// composer.json's scripts outside it.
func composerRunAccount(osUser string) error {
	u, err := user.Lookup(osUser)
	if err != nil {
		return fmt.Errorf("look up %s: %w", osUser, err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil || uid < minTenantUID || uid >= 60000 {
		return fmt.Errorf("%s is not a hosting user: Composer runs only as one", osUser)
	}
	return nil
}

// composerDomainPHP is the PHP CLI binary of the version the domain's vhost
// passes PHP to.
func composerDomainPHP(domain, osUser string) (string, string, error) {
	pool, err := domainFPMPool(domain, osUser)
	if err != nil {
		return "", "", err
	}
	php, err := exec.LookPath("php" + pool.PHPVersion)
	if err != nil {
		return "", pool.PHPVersion, fmt.Errorf("php%s CLI is not installed", pool.PHPVersion)
	}
	return php, pool.PHPVersion, nil
}

// --- composer.detect ---

type composerDetectParams struct {
	OSUser  string `json:"os_user"`
	Domain  string `json:"domain"`
	Docroot string `json:"docroot"`
}

type composerDetectDir struct {
	Dir       string `json:"dir"`
	HasLock   bool   `json:"has_lock"`
	HasVendor bool   `json:"has_vendor"`
}

type composerDetectResponse struct {
	// Dirs are the directories from the docroot up to the domain's directory
	// that hold a composer.json, nearest the docroot first. Dir is relative to
	// the domain's directory.
	Dirs          []composerDetectDir `json:"dirs"`
	PHPVersion    string              `json:"php_version,omitempty"`
	PHPError      string              `json:"php_error,omitempty"`
	ComposerError string              `json:"composer_error,omitempty"`
}

func composerDetectHandler(_ context.Context, raw json.RawMessage) (any, error) {
	var p composerDetectParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "parse params: " + err.Error()}
	}
	if !osUserSafeRE.MatchString(p.OSUser) || !domainRegex.MatchString(p.Domain) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid os_user or domain"}
	}
	domainDir := composerDomainDir(p.OSUser, p.Domain)
	rel, err := filepath.Rel(domainDir, filepath.Clean(p.Docroot))
	if err != nil || rel == ".." || strings.HasPrefix(rel, "../") || !filepath.IsAbs(p.Docroot) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "docroot must be inside the domain's directory"}
	}
	if rel == "." {
		rel = ""
	}
	if err := validateComposerAppDir(rel); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "docroot: " + err.Error()}
	}

	resp := composerDetectResponse{Dirs: []composerDetectDir{}}
	for dir := rel; ; dir = filepath.Dir(dir) {
		if dir == "." {
			dir = ""
		}
		base := filepath.Join("domains", p.Domain, dir)
		if composerIsFile(p.OSUser, filepath.Join(base, "composer.json")) {
			resp.Dirs = append(resp.Dirs, composerDetectDir{
				Dir:       dir,
				HasLock:   composerIsFile(p.OSUser, filepath.Join(base, "composer.lock")),
				HasVendor: composerIsDir(p.OSUser, filepath.Join(base, "vendor")),
			})
		}
		if dir == "" {
			break
		}
	}
	if _, ver, err := composerDomainPHP(p.Domain, p.OSUser); err != nil {
		resp.PHPError = err.Error()
	} else {
		resp.PHPVersion = ver
	}
	if _, err := composerPharFor(p.OSUser); err != nil {
		resp.ComposerError = err.Error()
	}
	return resp, nil
}

// --- composer.run_start ---

type composerRunStartParams struct {
	RunID    string   `json:"run_id"`
	OSUser   string   `json:"os_user"`
	Domain   string   `json:"domain"`
	AppDir   string   `json:"app_dir"`
	Command  string   `json:"command"`
	Packages []string `json:"packages,omitempty"`
	NoDev    bool     `json:"no_dev,omitempty"`
}

type composerRunStartResponse struct {
	Unit       string `json:"unit"`
	PHPVersion string `json:"php_version"`
}

// composerRunArgv is the systemd-run argv of a run.
func composerRunArgv(p composerRunStartParams, cmd composerCommand, php, phar, scanDir string) []string {
	args := []string{
		"--unit=" + composerRunUnit(p.RunID),
		"--no-block",
		"--uid=" + p.OSUser,
		"--gid=" + p.OSUser,
		"--slice=jabali-user-" + p.OSUser + ".slice",
		"--service-type=oneshot",
		"--remain-after-exit",
		"--description=jabali composer " + p.Command + " " + p.Domain,
	}
	args = append(args, composerRunProps...)
	// The sandbox starts from an empty environment, so Composer's comes in
	// through env.
	args = append(args, "--", composerSandboxShell, "--exec", envBin)
	args = append(args, composerEnv(p.OSUser)...)
	if scanDir != "" {
		// The leading ':' keeps PHP's own scan dir; the Composer ini is read
		// after it, so its sp.configuration_file wins.
		args = append(args, "PHP_INI_SCAN_DIR=:"+scanDir)
	}
	args = append(args, php, phar, p.Command)
	args = append(args, cmd.extra...)
	if p.NoDev {
		args = append(args, "--no-dev")
	}
	if cmd.noProgress {
		args = append(args, "--no-progress")
	}
	args = append(args,
		"--no-interaction",
		"--no-ansi",
		"--working-dir="+filepath.Join(composerDomainDir(p.OSUser, p.Domain), p.AppDir),
	)
	return append(args, p.Packages...)
}

func composerRunStartHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p composerRunStartParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "parse params: " + err.Error()}
	}
	if !ulidRE.MatchString(p.RunID) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "run_id must be a 26-char ULID"}
	}
	if !osUserSafeRE.MatchString(p.OSUser) || !domainRegex.MatchString(p.Domain) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "invalid os_user or domain"}
	}
	cmd, ok := composerCommands[p.Command]
	if !ok {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("command %q is not one a run may start", p.Command)}
	}
	if p.NoDev && !cmd.noDev {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: p.Command + " takes no no_dev"}
	}
	if err := validateComposerPackages(cmd, p.Packages); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}
	if err := validateComposerAppDir(p.AppDir); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}
	if err := composerRunAccount(p.OSUser); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}
	if !composerIsDir(p.OSUser, filepath.Join("domains", p.Domain, p.AppDir)) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: "the app directory does not exist or is a link"}
	}
	php, ver, err := composerDomainPHP(p.Domain, p.OSUser)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: err.Error()}
	}
	phar, err := composerPharFor(p.OSUser)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: err.Error()}
	}
	if err := ensureComposerHome(p.OSUser); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	scanDir, err := ensureComposerPHPDefense()
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	unit := composerRunUnit(p.RunID)
	if st := composerUnitShow(ctx, unit); st["LoadState"] == "loaded" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeAlreadyExists, Message: unit + " already exists"}
	}
	out, err := execCommandContext(ctx, "systemd-run", composerRunArgv(p, cmd, php, phar, scanDir)...).CombinedOutput()
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("systemd-run: %v: %s", err, strings.TrimSpace(string(out)))}
	}
	return composerRunStartResponse{Unit: unit, PHPVersion: ver}, nil
}

// --- composer.run_status ---

type composerRunIDParams struct {
	RunID string `json:"run_id"`
}

type composerRunStatusResponse struct {
	// State is running, succeeded, failed, timed_out or gone (no such unit:
	// released, cancelled, or lost to a reboot).
	State string `json:"state"`
	// Result is systemd's Result for a finished run.
	Result   string `json:"result,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Log      string `json:"log"`
	// LogTruncated: Log is the run's last lines, not all of it.
	LogTruncated      bool `json:"log_truncated,omitempty"`
	PHPDefenseBlocked bool `json:"php_defense_blocked,omitempty"`
}

// composerUnitShow reads a unit's state from systemctl show.
func composerUnitShow(ctx context.Context, unit string) map[string]string {
	out, _ := execCommandContext(ctx, "systemctl", "show", unit,
		"--property=LoadState,ActiveState,SubState,Result,ExecMainCode,ExecMainStatus").Output()
	props := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			props[k] = strings.TrimSpace(v)
		}
	}
	return props
}

// composerRunState maps a run unit's properties to the run's state.
func composerRunState(props map[string]string) (state, result string, exitCode *int) {
	if props["LoadState"] != "loaded" {
		return "gone", "", nil
	}
	result = props["Result"]
	// ExecMainCode 1 is CLD_EXITED: ExecMainStatus is the exit code, not a
	// signal number.
	if props["ExecMainCode"] == "1" {
		if n, err := strconv.Atoi(props["ExecMainStatus"]); err == nil {
			exitCode = &n
		}
	}
	switch props["ActiveState"] {
	case "activating", "deactivating", "reloading":
		return "running", "", nil
	case "active":
		if props["SubState"] == "exited" && result == "success" && exitCode != nil && *exitCode == 0 {
			return "succeeded", result, exitCode
		}
		return "running", "", nil
	case "failed":
		if result == "timeout" {
			return "timed_out", result, exitCode
		}
		return "failed", result, exitCode
	}
	return "gone", "", nil
}

// tailWriter keeps the last max bytes written to it.
type tailWriter struct {
	max       int
	buf       []byte
	truncated bool
}

func (w *tailWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	if over := len(w.buf) - w.max; over > 0 {
		w.buf = append(w.buf[:0], w.buf[over:]...)
		w.truncated = true
	}
	return len(p), nil
}

// lineCounter counts the newlines written to it.
type lineCounter int

func (c *lineCounter) Write(p []byte) (int, error) {
	*c += lineCounter(bytes.Count(p, []byte("\n")))
	return len(p), nil
}

// composerRunLog is the run's output from the journal, at most
// composerRunLogLines lines and composerRunLogMax bytes, the last ones kept.
func composerRunLog(ctx context.Context, unit string) (string, bool) {
	// _SYSTEMD_UNIT matches what the run's processes wrote, not the manager's
	// "Started"/"Finished" lines about the unit.
	cmd := execCommandContext(ctx, "journalctl", "_SYSTEMD_UNIT="+unit,
		"-n", strconv.Itoa(composerRunLogLines), "-o", "cat", "--no-pager")
	tail := &tailWriter{max: composerRunLogMax}
	var lines lineCounter
	cmd.Stdout = io.MultiWriter(tail, &lines)
	_ = cmd.Run()
	return string(tail.buf), tail.truncated || int(lines) >= composerRunLogLines
}

func composerRunStatusHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p composerRunIDParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "parse params: " + err.Error()}
	}
	if !ulidRE.MatchString(p.RunID) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "run_id must be a 26-char ULID"}
	}
	unit := composerRunUnit(p.RunID)
	resp := composerRunStatusResponse{}
	resp.State, resp.Result, resp.ExitCode = composerRunState(composerUnitShow(ctx, unit))
	resp.Log, resp.LogTruncated = composerRunLog(ctx, unit)
	if composerRunBlockedRE.MatchString(resp.Log) {
		// A blocked call can end in exit 0 (the caller swallowed the failure),
		// so a run PHP Defense stopped never counts as succeeded.
		resp.PHPDefenseBlocked = true
		if resp.State == "succeeded" {
			resp.State = "failed"
		}
	}
	return resp, nil
}

// --- composer.run_stop ---

type composerRunStopResponse struct {
	Unit string `json:"unit"`
}

// composerRunStopHandler stops a run's unit, ending a running Composer, and
// clears a failed one, so the unit is unloaded either way. A unit already
// gone is not an error.
func composerRunStopHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p composerRunIDParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "parse params: " + err.Error()}
	}
	if !ulidRE.MatchString(p.RunID) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "run_id must be a 26-char ULID"}
	}
	unit := composerRunUnit(p.RunID)
	if composerUnitShow(ctx, unit)["LoadState"] != "loaded" {
		return composerRunStopResponse{Unit: unit}, nil
	}
	if out, err := execCommandContext(ctx, "systemctl", "stop", unit).CombinedOutput(); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("systemctl stop %s: %v: %s", unit, err, strings.TrimSpace(string(out)))}
	}
	// A stopped unit that had failed stays loaded until its failure is reset.
	_ = execCommandContext(ctx, "systemctl", "reset-failed", unit).Run()
	return composerRunStopResponse{Unit: unit}, nil
}

func init() {
	Default.Register("composer.detect", composerDetectHandler)
	Default.Register("composer.run_start", composerRunStartHandler)
	Default.Register("composer.run_status", composerRunStatusHandler)
	Default.Register("composer.run_stop", composerRunStopHandler)
}
