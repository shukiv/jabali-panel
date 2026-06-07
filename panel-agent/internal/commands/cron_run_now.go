package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"os/user"
	"strconv"
	"strings"

	"git.linux-hosting.co.il/shukivaknin/jabali2/agentwire"
	"git.linux-hosting.co.il/shukivaknin/jabali2/internal/cronvalidate"
)

// cronRunNowParams is the input for cron.run_now command.
type cronRunNowParams struct {
	UserID        string   `json:"user_id"`
	Username      string   `json:"username"`
	JobID         string   `json:"job_id"`
	Command       string   `json:"command"`
	OwnedDocroots []string `json:"owned_docroots"`
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
//     + output, independent of whether the persistent timer/service unit
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
	// the stored command blindly.
	validated, vErr := cronvalidate.ValidateCommand(p.Command, p.OwnedDocroots)
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
	uid, _ := strconv.Atoi(u.Uid)
	runtimeDir := fmt.Sprintf("/run/user/%d", uid)

	// Ensure the user manager is up (linger + bus) so --machine can reach it.
	if err := ensureUserManager(ctx, p.Username, uid, runtimeDir); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("prepare user systemd manager for %s: %v", p.Username, err),
		}
	}

	args := []string{
		"--user",
		"--machine=" + p.Username + "@.host",
		"--pipe", "--wait", "--collect",
		"--",
	}
	args = append(args, validated.Argv...)
	cmd := exec.CommandContext(ctx, "systemd-run", args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	exitCode := 0
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
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
