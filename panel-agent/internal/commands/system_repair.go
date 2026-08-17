package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// system_repair.go — agent side of the GUI Repair Center (Updates page).
// Wraps the `jabali repair` CLI (panel-api/cmd/server/repair.go) so the
// admin UI can diagnose + auto-fix the install without a shell.
//
// Two modes, two execution models:
//   - diagnose: read-only detectors, runs inline + returns the text. Safe
//     to run synchronously because it changes nothing (can't restart a
//     service mid-call).
//   - auto: applies non-destructive fixes. One safe fix (bulwark-jwt-secret)
//     restarts jabali-panel, which would kill an inline agent->panel HTTP
//     call — so --auto runs as a transient systemd unit (own cgroup) that
//     survives, exactly like system.update_run. Status + output come from
//     system.repair_status (a system_unit_status allowlist entry).

// repairBinary is the panel CLI carrying the `repair` subcommand. Same
// binary system.update_run shells out to.
const repairBinary = "/usr/local/bin/jabali"

const repairUnitName = "jabali-repair-oneshot.service"

type systemRepairDiagnoseResponse struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exit_code"`
}

func systemRepairDiagnoseHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	cmd := execCommandContext(ctx, repairBinary, "repair", "--diagnose")
	out, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// repair --diagnose exits non-zero when issues are found; that's
			// not an agent error — surface the code + output to the UI.
			exitCode = ee.ExitCode()
		} else {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("run repair --diagnose: %v: %s", err, string(out)),
			}
		}
	}
	return systemRepairDiagnoseResponse{Output: string(out), ExitCode: exitCode}, nil
}

type systemRepairRunResponse struct {
	Unit      string `json:"unit"`
	StartedAt string `json:"started_at"`
}

func systemRepairRunHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	// Clear any prior run's lingering unit state (mirrors update_run) so a
	// transient-name collision doesn't reject the new run and journalctl
	// --since scoping stays clean.
	_ = execCommandContext(ctx, "systemctl", "reset-failed", repairUnitName).Run()

	startedAt := time.Now().UTC()
	cmd := execCommandContext(ctx, "systemd-run",
		"--unit="+repairUnitName,
		"--no-block",
		repairBinary, "repair", "--auto",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("systemd-run: %v: %s", err, string(out)),
		}
	}
	return systemRepairRunResponse{
		Unit:      repairUnitName,
		StartedAt: startedAt.Format(time.RFC3339Nano),
	}, nil
}

func init() {
	Default.Register("system.repair_diagnose", systemRepairDiagnoseHandler)
	Default.Register("system.repair_run", systemRepairRunHandler)
}
