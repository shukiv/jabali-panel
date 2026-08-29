package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// system.reboot — schedule a host reboot a few seconds out (GH #1330). Kernel
// and libc security updates leave /var/run/reboot-required, which the panel
// already surfaces; this is the action that finishes the job.
//
// The reboot is DETACHED from this agent call via a transient systemd timer, so
// the call returns immediately — the panel records its audit event and answers
// the UI before the box goes down ~5s later, rather than the connection being
// severed mid-response by an instant `systemctl reboot`. The panel gates the
// call behind admin + recent-auth step-up (JAB-380).
func systemRebootHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	// --on-active=5s creates a one-shot transient timer + service that runs
	// `systemctl reboot` in five seconds; --collect garbage-collects the unit.
	cmd := execCommandContext(ctx, "systemd-run", "--collect", "--on-active=5s",
		"--timer-property=AccuracySec=1s", "systemctl", "reboot")
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("schedule reboot: %v: %s", err, strings.TrimSpace(string(out))),
		}
	}
	return map[string]any{"scheduled": true, "delay_seconds": 5}, nil
}

func init() {
	Default.Register("system.reboot", systemRebootHandler)
}
