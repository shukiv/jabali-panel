package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"os/user"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/cronvalidate"
)

// cronRunNowParams is the input for cron.run_now command.
type cronRunNowParams struct {
	UserID        string   `json:"user_id"`
	Username      string   `json:"username"`
	JobID         string   `json:"job_id"`
	Command       string   `json:"command"`
	OwnedDocroots []string `json:"owned_docroots"`
	OwnedDomains  []string `json:"owned_domains"`
	// WaitSeconds is how long to wait for the run to end before answering
	// that it is still running (GH #2122). 0 means cronRunNowDefaultWait.
	WaitSeconds int `json:"wait_seconds,omitempty"`
	// RunAsRoot marks an admin's root job (JAB-293): its unit is the system
	// one cron.apply installs under /etc/systemd/system, and Username must be
	// "root".
	RunAsRoot bool `json:"run_as_root,omitempty"`
	// AllowShellScripts: the account's package includes SSH (GH #2290), as on
	// cron.apply.
	AllowShellScripts bool `json:"allow_shell_scripts,omitempty"`
}

// cronRunNowResponse is the output from cron.run_now.
type cronRunNowResponse struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	// Output is the run's combined stdout and stderr, set instead of Stdout
	// when the run went through the job's unit (the journal keeps one stream).
	Output string `json:"output,omitempty"`
	// Running: the run outlived the wait and carries on in the job's unit. Its
	// output goes to the job's log; Last run is recorded when it ends.
	Running bool `json:"running,omitempty"`
	// AlreadyRunning: the job's unit was already running, so nothing started.
	AlreadyRunning bool `json:"already_running,omitempty"`
}

// cronRunNowHandler runs a cron job's command ONCE, immediately, in the
// owning user's context.
//
// A tenant job with an installed unit runs through that unit (cronRunViaUnit,
// GH #2122): the handler waits up to wait_seconds and returns the exit code
// and output, or reports that the run is still going and carries on in the
// unit. A root job (run_as_root) runs through its system unit the same way.
// A job without a loaded unit, or on a host whose user manager can't be
// reached, runs the command directly via runuser and returns its exit code +
// output, bounded by the call's deadline.
func cronRunNowHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p cronRunNowParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	if p.Username == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "username required"}
	}
	if p.JobID == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "job_id required"}
	}
	// The job id names the unit this handler starts; hold it to the ULID shape
	// cron.apply enforces rather than trusting the caller.
	if !ulidRE.MatchString(p.JobID) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "job_id must be a 26-char ULID"}
	}
	if p.Command == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "command required"}
	}
	if p.RunAsRoot && p.Username != "root" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "a root job runs as root: username must be root"}
	}

	// Defense-in-depth: re-validate the command against the user's owned
	// docroots (same gate as cron.apply) before executing it. Never trust
	// the stored command blindly. GH #1435: a job may hold several commands.
	validatedCmds, vErr := cronvalidate.ValidateAnyMultiPolicy(p.Command, p.OwnedDocroots, p.OwnedDomains, cronHomeForUser(p.Username), cronShellPolicy(p.AllowShellScripts, p.RunAsRoot))
	if vErr != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("command failed validation: %v", vErr),
		}
	}

	u, err := user.Lookup(p.Username)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeNotFound,
			Message: fmt.Sprintf("user %s not found: %v", p.Username, err),
		}
	}
	// Run the command directly as the owning user via `runuser` -- a plain
	// setuid/setgid drop with NO dependency on the system D-Bus bus, the
	// user's systemd manager, or linger. The former path
	// (`systemd-run --user --machine=<user>@.host`) needed all three up and
	// reachable, and failed on hosts where the user manager was not --
	// "Failed to connect to system scope bus via local transport" /
	// "Transport endpoint is not connected" -- even with dbus installed
	// (GH #296). runuser propagates the command's real exit code and
	// stdout/stderr exactly the same, and works on every host shape.
	// (The scheduled timer run still executes inside the user manager; this
	// is only the manual "Run now" one-shot, which does not need the slice.)
	// A root job's unit is the system one; a tenant job's lives in the user's
	// manager. Root without run_as_root has no unit to start.
	var scope *cronUnitScope
	switch {
	case p.RunAsRoot:
		sc := systemCronScope()
		scope = &sc
	case p.Username != "root":
		sc := userCronScope(p.Username, u.Uid)
		scope = &sc
	}
	if scope != nil {
		resp, handled, rErr := cronRunViaUnit(ctx, *scope, p.JobID, cronRunNowWait(ctx, p.WaitSeconds))
		if handled {
			if rErr != nil {
				return nil, rErr
			}
			return resp, nil
		}
	}

	home := u.HomeDir
	if home == "" {
		home = "/home/" + p.Username
	}
	env := []string{
		"HOME=" + home,
		"USER=" + p.Username,
		"LOGNAME=" + p.Username,
		"PATH=" + home + "/.jabali/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin",
	}

	// Run each command in order, exactly like the Type=oneshot timer: stop at
	// the first non-zero exit and report it, so "Run now" mirrors a scheduled run.
	var stdout, stderr bytes.Buffer
	exitCode := 0
	multi := len(validatedCmds) > 1
	for i, vc := range validatedCmds {
		if multi {
			// Label each step so the operator can see which command ran.
			fmt.Fprintf(&stdout, "$ %s\n", strings.Join(vc.Argv, " "))
		}
		// GH #1458: run the command inside the per-user SSH sandbox (matching the
		// scheduled timer), so "Run now" can't shell out of the jail either. Root
		// (admin) crons stay unsandboxed, same as the timer path.
		args := []string{"-u", p.Username, "--"}
		if p.Username != "root" && vc.Kind != cronvalidate.KindHTTPTrigger {
			args = append(args, cronSandboxWrapper, "--exec")
		}
		args = append(args, vc.Argv...)
		cmd := execCommandContext(ctx, "runuser", args...)
		cmd.Dir = home
		cmd.Env = env
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		runErr := cmd.Run()
		if runErr != nil {
			if exitErr, ok := runErr.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = 1
			}
			fmt.Fprintf(&stderr, "(stopped: command %d exited %d)\n", i+1, exitCode)
			break
		}
	}

	return &cronRunNowResponse{
		ExitCode: exitCode,
		Stdout:   strings.TrimSpace(stdout.String()),
		Stderr:   strings.TrimSpace(stderr.String()),
	}, nil
}

func init() {
	Default.Register("cron.run_now", cronRunNowHandler)
}
