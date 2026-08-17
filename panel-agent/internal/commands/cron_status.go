package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/user"
	"strconv"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// cronStatusParams is the input for cron.status.
type cronStatusParams struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	JobID    string `json:"job_id"`
}

// cronStatusResult reports the last completion of a user cron's systemd
// service. HasRun is false when the unit has never executed, so the
// panel keeps whatever it had.
type cronStatusResult struct {
	HasRun    bool   `json:"has_run"`
	LastRunAt string `json:"last_run_at"` // RFC3339 UTC, empty when !HasRun
	ExitCode  int    `json:"exit_code"`
}

// cronStatusHandler harvests the last-run result of a user cron's
// systemd service so the panel can show "Last Run" for autonomous timer
// fires (the timer runs the unit directly — nothing otherwise reports
// the result back to the DB; only Run-now did).
//
// Property choice: a Type=oneshot service (which jabali crons are) with
// RemainAfterExit=no CLEARS ExecMainExitTimestamp once it goes inactive,
// but KEEPS ExecMainStartTimestamp (the last fire's start) and
// ExecMainStatus (the last exit code) — verified on the box. So we read
// ExecMainStartTimestamp for the run time.
//
// Connection: env-through-sudo (XDG_RUNTIME_DIR + DBUS_SESSION_BUS_
// ADDRESS), the #268 path — NOT `--machine=<user>@.host`, which proxies
// a LIMITED property set that returns EMPTY runtime timestamps (verified
// on the box). bare `systemctl --user` also fails (sudo env_reset).
func cronStatusHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p cronStatusParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if p.Username == "" || p.JobID == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "username and job_id required"}
	}

	u, err := user.Lookup(p.Username)
	if err != nil {
		return cronStatusResult{HasRun: false}, nil
	}
	runtimeDir := fmt.Sprintf("/run/user/%s", u.Uid)
	unit := fmt.Sprintf("jabali-cron-%s.service", p.JobID)

	cmd := execCommandContext(ctx, "sudo", "-u", p.Username,
		"env",
		"XDG_RUNTIME_DIR="+runtimeDir,
		"DBUS_SESSION_BUS_ADDRESS=unix:path="+runtimeDir+"/bus",
		"systemctl", "--user", "show", unit,
		"-p", "ExecMainStartTimestamp",
		"-p", "ExecMainStatus",
		"--no-pager")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Unit not loaded / user manager down → best-effort no-op.
		return cronStatusResult{HasRun: false}, nil
	}

	props := map[string]string{}
	for _, line := range strings.Split(stdout.String(), "\n") {
		if eq := strings.IndexByte(line, '='); eq > 0 {
			props[line[:eq]] = strings.TrimSpace(line[eq+1:])
		}
	}

	ts := props["ExecMainStartTimestamp"]
	if ts == "" {
		return cronStatusResult{HasRun: false}, nil
	}
	// systemd wall-clock format, e.g. "Sun 2026-06-08 02:35:59 UTC".
	t, err := time.Parse("Mon 2006-01-02 15:04:05 MST", ts)
	if err != nil {
		return cronStatusResult{HasRun: false}, nil
	}

	exitCode := 0
	if ec, err := strconv.Atoi(props["ExecMainStatus"]); err == nil {
		exitCode = ec
	}

	return cronStatusResult{
		HasRun:    true,
		LastRunAt: t.UTC().Format(time.RFC3339),
		ExitCode:  exitCode,
	}, nil
}

func init() {
	Default.Register("cron.status", cronStatusHandler)
}
