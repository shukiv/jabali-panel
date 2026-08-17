package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// systemUpdateRunResponse is the wire shape for system.update_run.
// The caller starts the update detached as a transient systemd unit and
// gets the unit name back. Status + log tail come from system.update_status,
// not this call. "started_at" is the timestamp the agent fired systemd-run;
// pass it back to system.update_status as `since` to scope journalctl.
type systemUpdateRunResponse struct {
	Unit      string `json:"unit"`
	StartedAt string `json:"started_at"`
}

const updateUnitName = "jabali-update-oneshot.service"

// systemUpdateRunHandler shells out to `systemd-run --unit=… --no-block`
// so the update lives in its OWN cgroup. The `jabali update` script ends
// with `→ restart services` which restarts jabali-agent itself; if the
// update were a child of jabali-agent, systemd would SIGTERM it during
// the restart and the panel would be left half-built. Transient unit =
// independent cgroup = survives.
func systemUpdateRunHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	// Reset the unit name first so a previous run's logs don't bleed into
	// `journalctl --since=<now>` output. systemd keeps failed-unit state
	// indefinitely; without `reset-failed`, a second run that hits a
	// transient name collision is rejected with ALREADY_EXISTS.
	_ = execCommandContext(ctx, "systemctl", "reset-failed", updateUnitName).Run()

	startedAt := time.Now().UTC()
	cmd := execCommandContext(ctx, "systemd-run",
		"--unit="+updateUnitName,
		// Tell the CLI this run came from the panel so it does NOT also
		// write an update_history row — the API already logged it (GH #300).
		"--setenv=JABALI_UPDATE_FROM_PANEL=1",
		"--no-block",
		// No --collect: a *failed* oneshot lingers in "failed" state so the
		// M50 run reconciler can read it as failed; a *successful* one is
		// GC'd to inactive (= success). reset-failed at the next run clears it.
		"/usr/local/bin/jabali", "update", "-f",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("systemd-run: %v: %s", err, string(out)),
		}
	}

	return systemUpdateRunResponse{
		Unit:      updateUnitName,
		StartedAt: startedAt.Format(time.RFC3339Nano),
	}, nil
}

func init() {
	Default.Register("system.update_run", systemUpdateRunHandler)
}
