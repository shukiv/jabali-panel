package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// systemUnitStatusParams is shared by system.update_status + system.apt_status.
// The same handler reads any allowlisted transient unit; locking the unit
// name to a server-side allowlist defends against a compromised panel-api
// asking for arbitrary unit logs.
type systemUnitStatusParams struct {
	Since string `json:"since,omitempty"`
}

type systemUnitStatusResponse struct {
	Unit      string `json:"unit"`
	Status    string `json:"status"` // "active" | "inactive" | "failed" | "activating"
	ExitCode  *int   `json:"exit_code,omitempty"`
	LogTail   string `json:"log_tail"`
	FetchedAt string `json:"fetched_at"`
}

// allowedStatusUnits is the explicit allowlist of transient units a caller
// can ask about. Hard-code so a malformed param can't tail an unrelated
// service like jabali-panel.
var allowedStatusUnits = map[string]string{
	"system.update_status":       "jabali-update-oneshot.service",
	"system.apt_status":          "jabali-apt-oneshot.service",
	"system.repair_status":       "jabali-repair-oneshot.service",
	"system.nspawn_build_status": "jabali-nspawn-build.service",
}

func systemUnitStatusFor(unit string) func(context.Context, json.RawMessage) (any, error) {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p systemUnitStatusParams
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
			}
		}
		// Default since=15m if not provided so we always get useful tail.
		since := p.Since
		if since == "" {
			since = time.Now().Add(-15 * time.Minute).UTC().Format(time.RFC3339)
		}
		statusOut, _ := execCommandContext(ctx, "systemctl", "is-active", unit).Output()
		status := strings.TrimSpace(string(statusOut))

		journalArgs := []string{"-u", unit, "--since=" + since, "--no-pager", "-o", "cat"}
		journalOut, _ := execCommandContext(ctx, "journalctl", journalArgs...).Output()

		resp := systemUnitStatusResponse{
			Unit:      unit,
			Status:    status,
			LogTail:   stripTransientNoise(string(journalOut), unit),
			FetchedAt: time.Now().UTC().Format(time.RFC3339Nano),
		}

		// Read exit code from `systemctl show` once the unit terminates.
		if status == "inactive" || status == "failed" {
			showOut, err := execCommandContext(ctx, "systemctl", "show", unit,
				"--property=ExecMainStatus", "--value").Output()
			if err == nil {
				if v, perr := strconv.Atoi(strings.TrimSpace(string(showOut))); perr == nil {
					resp.ExitCode = &v
				}
			}
		}
		return resp, nil
	}
}

// stripTransientNoise drops systemd's own benign "Failed to open
// /run/systemd/transient/<unit>: No such file or directory" lines from a unit's
// journal tail (GH #739). We run apt/update in a `systemd-run --no-block`
// transient unit; a package postinst's `systemctl daemon-reload` races the
// cleanup of that transient unit, so systemd logs the open-failure while
// re-reading the just-removed transient file. The update itself succeeds — the
// lines are pure noise that made operators think a package update half-failed.
// Scoped to this unit's transient path so unrelated log lines are untouched.
func stripTransientNoise(logTail, unit string) string {
	needle := "Failed to open /run/systemd/transient/" + unit
	if !strings.Contains(logTail, needle) {
		return logTail
	}
	lines := strings.Split(logTail, "\n")
	kept := lines[:0]
	for _, ln := range lines {
		if strings.Contains(ln, needle) {
			continue
		}
		kept = append(kept, ln)
	}
	return strings.Join(kept, "\n")
}

func init() {
	for cmdName, unit := range allowedStatusUnits {
		Default.Register(cmdName, systemUnitStatusFor(unit))
	}
}
