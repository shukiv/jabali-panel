package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/cronvalidate"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/filesafe"
)

// cronApplyParams is the input for cron.apply command.
type cronApplyParams struct {
	UserID        string   `json:"user_id"`
	Username      string   `json:"username"`
	JobID         string   `json:"job_id"`
	Name          string   `json:"name"`
	Command       string   `json:"command"`
	Schedule      string   `json:"schedule"`
	OwnedDocroots []string `json:"owned_docroots"`
	OwnedDomains  []string `json:"owned_domains"`
	RunAsRoot     bool     `json:"run_as_root,omitempty"`
}

// cronApplyResponse is the output from cron.apply.
type cronApplyResponse struct {
	ServicePath string `json:"service_path"`
	TimerPath   string `json:"timer_path"`
	NoChange    bool   `json:"no_change,omitempty"`
}

func cronApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p cronApplyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// Validate inputs
	if p.Username == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "username required",
		}
	}
	// job_id is interpolated into root-written unit paths under
	// /etc/systemd/system (and the per-user units dir), so it must be
	// format-checked, not merely non-empty: a value like "../ssh" would make
	// this handler clobber /etc/systemd/system/ssh.service, and the remove
	// path unlink arbitrary *.service / *.timer files as root. panel-api only
	// ever passes DB-minted ULIDs today — this is the agent doing its own
	// last-line validation rather than trusting that to stay true.
	if !ulidRE.MatchString(p.JobID) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "job_id must be a 26-char ULID",
		}
	}

	// SECURITY: Validate cron name to prevent control character injection (defense-in-depth)
	if err := cronvalidate.ValidateCronName(p.Name); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("name validation failed: %v", err),
		}
	}

	if p.Command == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "command required",
		}
	}
	if p.Schedule == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "schedule required",
		}
	}

	// Re-validate command and schedule (defense-in-depth per spec §3 invariant).
	// ValidateAny routes curl/wget self-domain crons (GH #400 Part B) to the
	// http-trigger validator (gated on OwnedDomains); everything else stays
	// the wp/php closed set (gated on OwnedDocroots).
	cmd, err := cronvalidate.ValidateAny(p.Command, p.OwnedDocroots, p.OwnedDomains)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("command validation failed: %v", err),
		}
	}

	if err := cronvalidate.ValidateSchedule(p.Schedule); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("schedule validation failed: %v", err),
		}
	}

	// Root cron branch (admin-only at API gate). Writes a SYSTEM-scoped
	// systemd unit at /etc/systemd/system/jabali-cron-<id>.{service,timer}
	// that runs the command as uid 0. Returns early; the per-user code
	// path below stays the canonical path for tenant crons.
	if p.RunAsRoot {
		return applyRootCron(ctx, p)
	}

	// Resolve user's UID
	u, err := user.Lookup(p.Username)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeNotFound,
			Message: fmt.Sprintf("user %s not found: %v", p.Username, err),
		}
	}

	uid, _ := strconv.Atoi(u.Uid)
	runtimeDir := fmt.Sprintf("/run/user/%d", uid)

	// Ensure linger is enabled + the per-user systemd manager is up so the
	// user bus (/run/user/<uid>/bus) exists before we touch `systemctl
	// --user`. A freshly-created (e.g. migrated) user may not be lingering
	// yet; erroring here and waiting for a separate user_slice_ensure tick
	// left timers unscheduled in practice, so self-heal it idempotently.
	if err := ensureUserManager(ctx, p.Username, uid, runtimeDir); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("prepare user systemd manager for %s: %v", p.Username, err),
		}
	}

	// Units MUST live in a systemd-user search path or `systemctl --user
	// enable <name>` can't find them ("Unit … does not exist"). The
	// previous /etc/jabali-panel/cron-units/<user> path is NOT a search
	// path, so timers were written to disk but never loaded (list-timers
	// empty, last_run NULL). Write into the user's
	// ~/.config/systemd/user/ — the standard per-user search path — and
	// chown the tree so the user manager can read it.
	uidN, _ := strconv.Atoi(u.Uid)
	gidN, _ := strconv.Atoi(u.Gid)
	unitsDir := filepath.Join(u.HomeDir, ".config", "systemd", "user")
	// All ~/.config/systemd/user writes go through a filesafe scope rooted at
	// the user's home (Gitea #475). The agent is root, so if .config or any
	// component is a tenant-planted symlink, plain MkdirAll/Chown/write/Rename
	// would follow it and redirect root-side writes/chowns outside the home.
	// openat2 RESOLVE_BENEATH refuses a symlinked component.
	cronScope, scErr := filesafe.NewScope(p.Username, p.Username, []string{u.HomeDir})
	if scErr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "scope init: " + scErr.Error()}
	}
	if _, err := cronScope.MkdirInScope(unitsDir, true, 0o755); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to create units directory: %v", err),
		}
	}
	// chown the dirs we may have just created, via directory fds (fchown,
	// symlink-safe) so systemd --user + the user can manage them.
	for _, d := range []string{
		filepath.Join(u.HomeDir, ".config"),
		filepath.Join(u.HomeDir, ".config", "systemd"),
		unitsDir,
	} {
		if df, derr := cronScope.OpenDirInScope(d); derr == nil {
			_ = df.Chown(uidN, gidN)
			_ = df.Close()
		}
	}
	// Best-effort: clean up units stranded in the old non-search-path
	// location from before this fix so they don't linger on disk.
	_ = os.RemoveAll(fmt.Sprintf("/etc/jabali-panel/cron-units/%s/jabali-cron-%s.service", p.Username, p.JobID))
	_ = os.RemoveAll(fmt.Sprintf("/etc/jabali-panel/cron-units/%s/jabali-cron-%s.timer", p.Username, p.JobID))

	// Generate unit file paths
	servicePath := filepath.Join(unitsDir, fmt.Sprintf("jabali-cron-%s.service", p.JobID))
	timerPath := filepath.Join(unitsDir, fmt.Sprintf("jabali-cron-%s.timer", p.JobID))

	// Generate unit file content
	serviceContent := buildCronServiceContent(p.JobID, p.Name, cmd, p.Username, p.OwnedDocroots)
	timerContent, tcErr := buildCronTimerContent(p.JobID, p.Schedule)
	if tcErr != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("translate schedule %q to OnCalendar: %v", p.Schedule, tcErr),
		}
	}

	// Check for no-change (idempotency per spec §3 invariant)
	serviceMatch := filesMatch(servicePath, []byte(serviceContent))
	timerMatch := filesMatch(timerPath, []byte(timerContent))
	if serviceMatch && timerMatch {
		return &cronApplyResponse{
			ServicePath: servicePath,
			TimerPath:   timerPath,
			NoChange:    true,
		}, nil
	}

	// Write unit files atomically, escape-proof (openat2), owned by the user.
	if err := writeUnitInScope(cronScope, servicePath, []byte(serviceContent), uidN, gidN); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to write service file: %v", err),
		}
	}
	if err := writeUnitInScope(cronScope, timerPath, []byte(timerContent), uidN, gidN); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to write timer file: %v", err),
		}
	}

	// Reload systemd as user and enable timer
	if err := systemctlUserExec(ctx, p.Username, runtimeDir, "daemon-reload"); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to reload systemd: %v", err),
		}
	}

	if err := systemctlUserExec(ctx, p.Username, runtimeDir, "enable", "--now", fmt.Sprintf("jabali-cron-%s.timer", p.JobID)); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to enable timer: %v", err),
		}
	}

	return &cronApplyResponse{
		ServicePath: servicePath,
		TimerPath:   timerPath,
	}, nil
}

// buildCronServiceContent generates the systemd service unit content.
func buildCronServiceContent(jobID, name string, cmd *cronvalidate.Command, username string, ownedDocroots []string) string {
	// Build ExecStart with single-quoted tokens (systemd parses whitespace to argv)
	execStart := "ExecStart="
	for i, token := range cmd.Argv {
		if i > 0 {
			execStart += " "
		}
		execStart += singleQuote(token)
	}

	// For ExecStartPre, determine the docroot to validate
	// We use the first argument that starts with / and is in an owned docroot
	docroot := ""
	for _, arg := range cmd.Argv[1:] {
		if strings.HasPrefix(arg, "/") {
			// Check if it's in an owned docroot
			for _, od := range ownedDocroots {
				if arg == od || strings.HasPrefix(arg, od+"/") {
					docroot = od
					break
				}
			}
			if docroot != "" {
				break
			}
		}
	}

	// If we have a docroot, gate the whole unit on it existing via a
	// systemd [Unit] condition (GH #403). This REPLACES the former
	// `ExecStartPre=/usr/local/libexec/jabali/cron-precheck <docroot>` bash
	// helper: that helper spawned a `bash` execve on EVERY cron fire, run
	// inside the tenant's lingering user@<uid>.service (auid=tenant), which
	// the M33 `jabali_susp_exec` auditd rule tagged -> a tenant with several
	// crons tripped the "Suspicious exec burst" detector with jabali's own
	// machinery. ConditionPathIsDirectory does the same docroot check in the
	// systemd manager (no exec): a missing docroot skips the unit cleanly,
	// same outcome as the precheck's exit 1, without an audited shell.
	condDocroot := ""
	if docroot != "" {
		condDocroot = "ConditionPathIsDirectory=" + docroot + "\n"
	}

	// ExecSearchPath puts the per-user wrapper dir FIRST in systemd's binary
	// search for ExecStart= (GH #299). systemd resolves the ExecStart binary
	// against the MANAGER's path, NOT Environment=PATH — so a bare `php`
	// resolved to /usr/bin/php (system default), ignoring the user's pinned
	// version, and `php8.5` only worked because /usr/bin/php8.5 happened to
	// exist. With .jabali/bin first, bare `php` follows the user's pinned
	// wrapper (.jabali/bin/php -> their version) and `php8.5` follows
	// .jabali/bin/php8.5. A version installed NOWHERE yields a clean 203/EXEC
	// failure — never a silent wrong-version run.
	//
	// ExecSearchPath REPLACES the default search path, it does not prepend to
	// it (JAB-182). Listing only binDir therefore made every bare command that
	// is NOT a jabali wrapper unresolvable — `wp`, `git`, `rsync`, `node`,
	// `composer` — each failing 203/EXEC before the job ran a single line.
	// Measured on a host: 5,650 silent failures in 30 days. Only `php*` worked,
	// because those are exactly the names .jabali/bin provides, which is what
	// made it look like the path was falling through to /usr/bin when it was
	// not. Verified with systemd 255:
	//
	//   systemd-run --property=ExecSearchPath=/tmp/empty --wait wp --version
	//     -> 203     (and bare `git` likewise, though it is in /usr/bin)
	//   systemd-run --wait wp --version
	//     -> runs
	//
	// So the wrapper dir goes first and the standard system dirs follow — the
	// same list as Environment=PATH, built from one variable so the two cannot
	// drift apart again. Environment=PATH is still needed separately because
	// wp's `#!/usr/bin/env php` shebang reads the running process's PATH, not
	// ExecSearchPath. (GH #184, #256, #299, JAB-182.)
	binDir := "/home/" + username + "/.jabali/bin"
	searchPath := binDir + ":/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin"
	return fmt.Sprintf(`[Unit]
Description=Jabali cron job %s (%s)
After=default.target
StartLimitIntervalSec=1
StartLimitBurst=1
%s
[Service]
Type=oneshot
RemainAfterExit=no
ExecSearchPath=%s
Environment=PATH=%s
WorkingDirectory=%%h
%s
`, jobID, name, condDocroot,
		searchPath,
		searchPath,
		execStart)
}

// buildCronTimerContent generates the systemd timer unit content. The
// stored schedule is 5-field POSIX cron; systemd OnCalendar= needs
// systemd-calendar format (cron syntax yields "Loaded: bad-setting" and
// the timer never fires), so translate it via cronvalidate.
func buildCronTimerContent(jobID, schedule string) (string, error) {
	onCalendar, err := cronvalidate.CronToSystemdCalendar(schedule)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`[Unit]
Description=Jabali cron timer for %s

[Timer]
OnCalendar=%s
Persistent=true
Unit=jabali-cron-%s.service

[Install]
WantedBy=timers.target
`, jobID, onCalendar, jobID), nil
}

// singleQuote wraps a token in single quotes, escaping any embedded single quotes.
// This matches systemd's ExecStart argv parsing where single quotes prevent shell interpretation.
func singleQuote(s string) string {
	// Single-quote escape: 'foo'\''bar' produces foo'bar when shell-parsed
	// But systemd doesn't shell-parse; it uses the raw token.
	// For systemd ExecStart, we just wrap in single quotes.
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// checkUserLinger verifies the user has loginctl enable-linger set.
func checkUserLinger(ctx context.Context, username string) error {
	// Check if user's linger session exists at /var/lib/systemd/linger/<username>
	lingerPath := filepath.Join("/var/lib/systemd/linger", username)
	_, err := os.Stat(lingerPath)
	return err
}

// ensureUserManager makes `systemctl --user` usable for username:
//  1. enable-linger (idempotent) so the user manager persists + the
//     runtime dir is created.
//  2. start user@<uid>.service synchronously so /run/user/<uid>/bus
//     exists right now (enable-linger alone can be async).
//
// Both are no-ops when already in place.
// ensureSystemBus makes sure the D-Bus system bus socket exists, starting
// dbus.socket/dbus.service if not (the agent runs as root). On minimal
// LXC/OpenVZ images dbus.socket is static and isn't pulled in at boot, so the
// socket can be missing after a reboot even though install.sh activated it once
// — every cron op then fails "Failed to connect to system scope bus" (GH #296).
// Returns a clear, actionable error when the bus genuinely can't be brought up.
func ensureSystemBus(ctx context.Context) error {
	const sock = "/run/dbus/system_bus_socket"
	if _, err := os.Stat(sock); err == nil {
		return nil
	}
	_ = execCommandContext(ctx, "systemctl", "start", "dbus.socket", "dbus.service").Run()
	for i := 0; i < 5; i++ {
		if _, err := os.Stat(sock); err == nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("system D-Bus is unavailable (%s missing) and could not be started — cron needs a running system bus; on a minimal LXC/OpenVZ image enable it with `systemctl enable --now dbus.socket`", sock)
}

func ensureUserManager(ctx context.Context, username string, uid int, runtimeDir string) error {
	// The user manager and `systemd-run --machine` both reach the user's
	// systemd over the SYSTEM bus — make sure it's up first (GH #296).
	if err := ensureSystemBus(ctx); err != nil {
		return err
	}
	if err := checkUserLinger(ctx, username); err != nil {
		if out, lErr := execCommandContext(ctx, "loginctl", "enable-linger", username).CombinedOutput(); lErr != nil {
			return fmt.Errorf("enable-linger: %v: %s", lErr, strings.TrimSpace(string(out)))
		}
	}
	// Start (idempotent) the user manager so the bus socket is present.
	if _, err := os.Stat(filepath.Join(runtimeDir, "bus")); err != nil {
		if out, sErr := execCommandContext(ctx, "systemctl", "start",
			fmt.Sprintf("user@%d.service", uid)).CombinedOutput(); sErr != nil {
			return fmt.Errorf("start user@%d.service: %v: %s", uid, sErr, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// systemctlUserExec runs `systemctl --user <args>` as the target user.
//
// `sudo -u <user>` RESETS the environment (env_reset is the sudoers
// default), so setting cmd.Env is useless — the vars are stripped before
// systemctl runs and it reports "Failed to connect to user scope bus …
// $DBUS_SESSION_BUS_ADDRESS and $XDG_RUNTIME_DIR not defined". Pass the
// two vars the user-bus connection needs THROUGH sudo via the `env`
// command so they land in the child's environment:
//
//	XDG_RUNTIME_DIR=/run/user/<uid>
//	DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/<uid>/bus
//
// This is the bug that left every migrated user's cron timer unscheduled
// (panel row enabled, but `systemctl --user list-timers` empty).
func systemctlUserExec(ctx context.Context, username string, runtimeDir string, args ...string) error {
	full := []string{
		"-u", username,
		"env",
		"XDG_RUNTIME_DIR=" + runtimeDir,
		"DBUS_SESSION_BUS_ADDRESS=unix:path=" + runtimeDir + "/bus",
		"systemctl", "--user",
	}
	full = append(full, args...)
	cmd := execCommandContext(ctx, "sudo", full...)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// applyRootCron renders + installs a SYSTEM-scoped systemd timer for
// an admin-created root cron. The unit lives under /etc/systemd/system/
// and runs as root (no User= directive). Idempotent: returns no_change
// when the on-disk files already match.
func applyRootCron(ctx context.Context, p cronApplyParams) (any, error) {
	// Re-run validation that the user-path also enforces. We do NOT
	// call cronvalidate.ValidateCommand with OwnedDocroots because
	// root crons don't have a tenant docroot; the validator's
	// purpose there is to block escaping into other tenants' dirs.
	// Root has no such constraint by definition.
	if err := cronvalidate.ValidateSchedule(p.Schedule); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("schedule validation failed: %v", err),
		}
	}

	servicePath := filepath.Join("/etc/systemd/system", fmt.Sprintf("jabali-cron-%s.service", p.JobID))
	timerPath := filepath.Join("/etc/systemd/system", fmt.Sprintf("jabali-cron-%s.timer", p.JobID))

	serviceContent := buildRootCronServiceContent(p.JobID, p.Name, p.Command)
	timerContent, tcErr := buildCronTimerContent(p.JobID, p.Schedule)
	if tcErr != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("translate schedule %q to OnCalendar: %v", p.Schedule, tcErr),
		}
	}

	if filesMatch(servicePath, []byte(serviceContent)) && filesMatch(timerPath, []byte(timerContent)) {
		return &cronApplyResponse{ServicePath: servicePath, TimerPath: timerPath, NoChange: true}, nil
	}

	if err := writeFileAtomically(servicePath, []byte(serviceContent), 0644); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write service: %v", err)}
	}
	if err := writeFileAtomically(timerPath, []byte(timerContent), 0644); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write timer: %v", err)}
	}

	// system-level daemon-reload + enable.
	if out, err := execCommandContext(ctx, "systemctl", "daemon-reload").CombinedOutput(); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("daemon-reload: %v: %s", err, out)}
	}
	if out, err := execCommandContext(ctx, "systemctl", "enable", "--now", fmt.Sprintf("jabali-cron-%s.timer", p.JobID)).CombinedOutput(); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("enable timer: %v: %s", err, out)}
	}
	return &cronApplyResponse{ServicePath: servicePath, TimerPath: timerPath}, nil
}

// buildRootCronServiceContent renders a system-scoped service that
// runs the user-supplied command as root. We deliberately do NOT add
// a User= directive; the unit inherits the system root context.
//
// Hardening:
//   - PrivateTmp=true so a cron run can't poke at another service's
//     /tmp state.
//   - ProtectSystem=strict, ProtectHome=read-only: read-only views
//     of /usr, /boot, /etc by default; writes need explicit
//     ReadWritePaths. The operator can lift those by editing the
//     unit (defaults are intentionally restrictive).
//   - NoNewPrivileges=true.
//   - StandardOutput/Error journald + the per-job logs live in
//     /var/log/jabali/cron/root/<jobid>.log (kept in sync with
//     the per-user layout under /var/log/jabali/cron/<user>/).
func buildRootCronServiceContent(jobID, name, command string) string {
	return fmt.Sprintf(`[Unit]
Description=Jabali root cron: %s
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/bin/sh -c %s
PrivateTmp=true
ProtectSystem=strict
ProtectHome=read-only
NoNewPrivileges=true
ReadWritePaths=/var/log /var/lib /var/cache /tmp
StandardOutput=append:/var/log/jabali/cron/root/%s.log
StandardError=append:/var/log/jabali/cron/root/%s.log

[Install]
WantedBy=multi-user.target
`, name, singleQuote(command), jobID, jobID)
}

func init() {
	Default.Register("cron.apply", cronApplyHandler)
}

// writeUnitInScope writes content to an in-home unit file via a temp file +
// renameat, all through the escape-proof filesafe scope, owned by the user
// (Gitea #475). Mirrors the .ssh/authorized_keys atomic write.
func writeUnitInScope(scope *filesafe.Scope, path string, content []byte, uid, gid int) error {
	tmp := path + ".tmp"
	_ = scope.RemoveInScope(tmp, false)
	f, err := scope.CreateExclInScope(tmp, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		_ = scope.RemoveInScope(tmp, false)
		return err
	}
	if err := f.Chown(uid, gid); err != nil {
		_ = f.Close()
		_ = scope.RemoveInScope(tmp, false)
		return err
	}
	if err := f.Close(); err != nil {
		_ = scope.RemoveInScope(tmp, false)
		return err
	}
	return scope.RenameInScope(tmp, path)
}
