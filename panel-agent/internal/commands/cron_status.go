package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os/user"
	"strconv"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// cronStatusParams is the input for cron.status.
type cronStatusParams struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	JobID    string `json:"job_id"`
	// RunAsRoot: an admin's root job (JAB-293), whose unit is a system one.
	RunAsRoot bool `json:"run_as_root,omitempty"`
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
	// The job id names the unit read below, a system one for a root job.
	if !ulidRE.MatchString(p.JobID) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "job_id must be a 26-char ULID"}
	}

	scope := systemCronScope()
	if !p.RunAsRoot {
		u, err := user.Lookup(p.Username)
		if err != nil {
			return cronStatusResult{HasRun: false}, nil
		}
		scope = userCronScope(p.Username, u.Uid)
	}
	unit := fmt.Sprintf("jabali-cron-%s.service", p.JobID)

	props, err := cronUnitProps(ctx, scope, unit, "ExecMainStartTimestamp", "ExecMainStatus", "ActiveState")
	if err != nil {
		// Unit not loaded / user manager down → best-effort no-op.
		return cronStatusResult{HasRun: false}, nil
	}

	// GH #2122: while a run is under way the unit already shows its start
	// time and a placeholder ExecMainStatus=0. Reporting that would record
	// "exit 0" for a run that hasn't ended, and the harvest's newer-than
	// check would then never replace it with the real result.
	if cronUnitRunning(props["ActiveState"]) {
		return cronStatusResult{HasRun: false}, nil
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
