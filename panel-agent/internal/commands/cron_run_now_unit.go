package commands

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// GH #2122: "Run now" used to run the job as a child of this call, so the
// call's deadline (the panel's agent timeout, 30 s by default) killed any job
// that ran longer, and the panel answered 502. A job now runs through its own
// unit, the one the timer fires: the run belongs to the systemd manager (the
// user's for a tenant job, the system's for a root job), not to this call, so
// it keeps running after the call answers (and across an agent restart). Its
// output lands in the job's log and the reconciler's cron.status harvest
// records Last run when it ends.

// cronRunNowDefaultWait is how long Run now waits for a run to finish before
// answering that it is still running, when the caller names no wait.
const cronRunNowDefaultWait = 20 * time.Second

// cronRunNowDeadlineMargin keeps the wait clear of the call's deadline: after
// the wait the handler still reads the unit's result and output, and the
// answer has to reach the panel before the panel gives up on the call.
const cronRunNowDeadlineMargin = 3 * time.Second

// cronRunNowOutputLines caps how much of a finished run's output is returned.
const cronRunNowOutputLines = 1000

// cronRunNowWait is the requested wait (the default when none), shortened to
// fit inside the call's deadline.
func cronRunNowWait(ctx context.Context, requestedSeconds int) time.Duration {
	wait := cronRunNowDefaultWait
	if requestedSeconds > 0 {
		wait = time.Duration(requestedSeconds) * time.Second
	}
	if deadline, ok := ctx.Deadline(); ok {
		if left := time.Until(deadline) - cronRunNowDeadlineMargin; left < wait {
			wait = left
		}
	}
	if wait < time.Second {
		wait = time.Second
	}
	return wait
}

// cronSyslogIdentifier is the identifier cron.apply gives a tenant job's unit
// (SyslogIdentifier=), so every line the job writes to the journal carries it.
func cronSyslogIdentifier(jobID string) string {
	return "jabali-cron-" + jobID
}

// cronUnitName is the name of a job's service unit.
func cronUnitName(jobID string) string {
	return fmt.Sprintf("jabali-cron-%s.service", jobID)
}

// cronUnitScope says where a job's unit lives and how to reach it: a tenant
// job's in the user's own systemd manager, a root job's (JAB-293) in the
// system manager, as cron.apply installs them.
type cronUnitScope struct {
	// systemctl returns the command that runs `systemctl <args>` against
	// the manager that owns the unit.
	systemctl func(args ...string) (string, []string)
	// outputMatch returns the journalctl arguments that select what the
	// job's own processes wrote: not the manager's "Starting…" /
	// "Finished…" lines.
	outputMatch func(jobID string) []string
	// logMatch returns the journalctl arguments that select the job's log:
	// its output and the manager's lines about its unit.
	logMatch func(jobID string) []string
}

// userCronScope reaches a tenant's unit through the user's own manager, the
// same transport cron.apply and cron.status use (systemctlUserExec explains
// why the bus env is spelled out).
//
// The job's journal is read as root, limited to the tenant's entries by the
// journal's trusted _UID field (GH #2120): `journalctl --user` as the tenant
// sees only the tenant's own journal file, and journald splits one out per
// user only when the journal is stored persistently, so with a volatile
// journal the output was lost.
func userCronScope(username, uid string) cronUnitScope {
	runtimeDir := "/run/user/" + uid
	owner := "_UID=" + uid
	// The job's own lines: those journald tied to the unit, OR those tagged
	// with the job's identifier. journald ties a line to a unit by looking
	// up the writer's cgroup when it logs the line. The last line a job
	// writes without a trailing newline is logged only when the output
	// stream closes, after the job has exited, and journald takes the unit
	// name a stream announces only from root (journald-stream.c), so for a
	// tenant job that line has no unit field (GH #2120): a script whose
	// output has no final newline (`echo "done<br>";`) showed nothing. The
	// identifier travels with the stream itself, so it catches that line.
	// Any process can set SYSLOG_IDENTIFIER, which is why both terms are
	// held to the tenant's _UID: only the tenant can put lines into its own
	// job's output. The first term keeps runs logged before the unit had the
	// identifier.
	output := func(jobID string) []string {
		return []string{
			owner, "_SYSTEMD_USER_UNIT=" + cronUnitName(jobID), "+",
			owner, "SYSLOG_IDENTIFIER=" + cronSyslogIdentifier(jobID),
		}
	}
	return cronUnitScope{
		systemctl: func(args ...string) (string, []string) {
			return "sudo", append([]string{
				"-u", username,
				"env",
				"XDG_RUNTIME_DIR=" + runtimeDir,
				"DBUS_SESSION_BUS_ADDRESS=unix:path=" + runtimeDir + "/bus",
				"systemctl", "--user",
			}, args...)
		},
		outputMatch: output,
		// Plus the user manager's lines about the unit, which carry USER_UNIT.
		logMatch: func(jobID string) []string {
			return append(output(jobID), "+", owner, "USER_UNIT="+cronUnitName(jobID))
		},
	}
}

// systemCronScope reaches a root job's unit, which lives in the system
// manager under /etc/systemd/system. A root job's last line keeps its unit
// field: journald trusts the unit name a root stream announces.
func systemCronScope() cronUnitScope {
	return cronUnitScope{
		systemctl:   func(args ...string) (string, []string) { return "systemctl", args },
		outputMatch: func(jobID string) []string { return []string{"_SYSTEMD_UNIT=" + cronUnitName(jobID)} },
		logMatch:    func(jobID string) []string { return []string{"-u", cronUnitName(jobID)} },
	}
}

func (s cronUnitScope) command(ctx context.Context, build func(...string) (string, []string), args ...string) *exec.Cmd {
	name, argv := build(args...)
	return execCommandContext(ctx, name, argv...)
}

// cronUnitProps reads the named properties of a job's unit.
func cronUnitProps(ctx context.Context, scope cronUnitScope, unit string, props ...string) (map[string]string, error) {
	args := []string{"show", unit, "--no-pager"}
	for _, p := range props {
		args = append(args, "-p", p)
	}
	cmd := scope.command(ctx, scope.systemctl, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("systemctl show %s: %v: %s", unit, err, strings.TrimSpace(stderr.String()))
	}
	out := map[string]string{}
	for _, line := range strings.Split(stdout.String(), "\n") {
		if eq := strings.IndexByte(line, '='); eq > 0 {
			out[line[:eq]] = strings.TrimSpace(line[eq+1:])
		}
	}
	return out, nil
}

// cronUnitRunning reports whether a unit's ActiveState means a run is under way.
func cronUnitRunning(activeState string) bool {
	switch activeState {
	case "activating", "active", "deactivating", "reloading", "refreshing":
		return true
	}
	return false
}

// cronUnitRan reports whether a unit's Result means its commands ran, as
// opposed to systemd refusing to start it (start-limit-hit, a bad unit...).
func cronUnitRan(result string) bool {
	switch result {
	case "success", "exit-code", "signal", "core-dump", "timeout", "watchdog", "oom-kill":
		return true
	}
	return false
}

// cronUnitExitCode turns the unit's main-process result into an exit code: the
// command's own status, or 128+signal (as a shell reports it) when a signal
// ended it.
func cronUnitExitCode(props map[string]string) int {
	status, _ := strconv.Atoi(props["ExecMainStatus"])
	switch props["ExecMainCode"] {
	case "2", "3": // CLD_KILLED, CLD_DUMPED: status is the signal
		return 128 + status
	}
	if status == 0 && props["Result"] != "success" {
		return 1
	}
	return status
}

// cronUnitOutput returns what the run started at `since` wrote. The journal
// keeps stdout and stderr as one stream, so this is the combined output.
func cronUnitOutput(ctx context.Context, scope cronUnitScope, jobID string, since time.Time) string {
	// journald reads the job's output asynchronously; flush it so the last
	// lines written before the job ended are in the journal before reading.
	_ = execCommandContext(ctx, "journalctl", "--sync").Run()
	// The job's own output only: not the manager's "Starting…" /
	// "Finished…" lines.
	args := append(scope.outputMatch(jobID),
		"--since", fmt.Sprintf("@%d", since.Unix()),
		"-n", strconv.Itoa(cronRunNowOutputLines),
		"-o", "cat",
		"--no-pager",
	)
	cmd := execCommandContext(ctx, "journalctl", args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	_ = cmd.Run()
	return strings.TrimSpace(stdout.String())
}

// cronRunViaUnit runs a job by starting its own unit. handled is false when
// the job has no loaded unit or its manager can't be reached (a job created
// moments ago, before the reconciler installed it, or a host where the user
// manager is down — GH #296); the caller then runs the command directly as
// before.
func cronRunViaUnit(ctx context.Context, scope cronUnitScope, jobID string, wait time.Duration) (resp *cronRunNowResponse, handled bool, err error) {
	unit := cronUnitName(jobID)

	before, err := cronUnitProps(ctx, scope, unit, "LoadState", "ActiveState")
	if err != nil || before["LoadState"] != "loaded" {
		return nil, false, nil
	}
	if cronUnitRunning(before["ActiveState"]) {
		// A scheduled fire or an earlier Run now is still going; starting the
		// unit again would only attach to that run.
		return &cronRunNowResponse{AlreadyRunning: true}, true, nil
	}

	since := time.Now()
	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	// `systemctl start` on a Type=oneshot unit returns when the run ends.
	start := scope.command(waitCtx, scope.systemctl, "start", unit)
	// When the wait is over, stop only this waiting client (SIGTERM, which
	// sudo passes on to systemctl); the run carries on in its manager.
	start.Cancel = func() error { return start.Process.Signal(syscall.SIGTERM) }
	start.WaitDelay = 2 * time.Second
	var startErr bytes.Buffer
	start.Stderr = &startErr
	runErr := start.Run()
	if waitCtx.Err() != nil {
		return &cronRunNowResponse{Running: true}, true, nil
	}

	after, err := cronUnitProps(ctx, scope, unit,
		"Result", "ExecMainCode", "ExecMainStatus", "ConditionResult")
	if err != nil {
		return nil, true, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	if runErr != nil && !cronUnitRan(after["Result"]) {
		return nil, true, &agentwire.AgentError{
			Code:    agentwire.CodeFailedPrecondition,
			Message: fmt.Sprintf("start %s: %v: %s", unit, runErr, strings.TrimSpace(startErr.String())),
		}
	}
	if after["ConditionResult"] == "no" {
		// The unit's ConditionPathIsDirectory= gate (GH #403): a site folder
		// the job uses is gone, so the scheduled run skips it too.
		return &cronRunNowResponse{
			ExitCode: 1,
			Stderr:   "The job did not run: a site folder it uses does not exist.",
		}, true, nil
	}
	return &cronRunNowResponse{
		ExitCode: cronUnitExitCode(after),
		Output:   cronUnitOutput(ctx, scope, jobID, since),
	}, true, nil
}
