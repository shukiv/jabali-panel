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
}

// cronRunNowResponse is the output from cron.run_now.
type cronRunNowResponse struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
}

// cronRunNowHandler runs a cron job's command ONCE, immediately, in the
// owning user's context, and returns its exit code + output.
//
// It executes the command directly via
//
//	systemd-run --user --machine=<user>@.host --pipe --wait --collect -- <argv>
//
// rather than `systemctl --user start <timer's .service>`:
//   - --machine=<user>@.host connects to the user's systemd manager over
//     the system bus, so no fragile XDG_RUNTIME_DIR / DBUS env plumbing
//     through `sudo -u` (whose env_reset stripped it — the old bug).
//   - --pipe captures stdout/stderr, --wait blocks until the unit
//     finishes and propagates its exit code, --collect garbage-collects
//     the transient unit. So the operator gets the REAL command exit code
//   - output, independent of whether the persistent timer/service unit
//     happens to be loaded.
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
	if p.Command == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "command required"}
	}

	// Defense-in-depth: re-validate the command against the user's owned
	// docroots (same gate as cron.apply) before executing it. Never trust
	// the stored command blindly. GH #1435: a job may hold several commands.
	validatedCmds, vErr := cronvalidate.ValidateAnyMulti(p.Command, p.OwnedDocroots, p.OwnedDomains)
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
		args := append([]string{"-u", p.Username, "--"}, vc.Argv...)
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
