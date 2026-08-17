package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// systemAptRunResponse mirrors systemUpdateRunResponse — the apt path
// detaches into its own transient cgroup so a libc/openssh upgrade that
// triggers a daemon restart doesn't kill the apt process running it.
type systemAptRunResponse struct {
	Unit      string `json:"unit"`
	StartedAt string `json:"started_at"`
}

const aptUnitName = "jabali-apt-oneshot.service"

func systemAptRunHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	_ = execCommandContext(ctx, "systemctl", "reset-failed", aptUnitName).Run()

	startedAt := time.Now().UTC()
	cmd := execCommandContext(ctx, "systemd-run",
		"--unit="+aptUnitName,
		"--no-block",
		// No --collect: failed unit lingers (reconciler reads it as failed),
		// success is GC'd to inactive. reset-failed at next run clears it.
		"--setenv=DEBIAN_FRONTEND=noninteractive",
		"--setenv=LC_ALL=C",
		"apt-get",
		"-y",
		`-o=Dpkg::Options::=--force-confdef`,
		`-o=Dpkg::Options::=--force-confold`,
		"-o=DPkg::Lock::Timeout=120",
		"dist-upgrade",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("systemd-run: %v: %s", err, string(out)),
		}
	}
	return systemAptRunResponse{
		Unit:      aptUnitName,
		StartedAt: startedAt.Format(time.RFC3339Nano),
	}, nil
}

func init() {
	Default.Register("system.apt_run", systemAptRunHandler)
}
