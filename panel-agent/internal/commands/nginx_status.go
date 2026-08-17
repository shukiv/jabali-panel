package commands

// nginx.status — cheap nginx health check for the Server Status
// dashboard poller. Returns whether the systemd service is active and
// its main PID. Unlike nginx.test, this does NOT shell out to
// `nginx -t` and does NOT compile any config; it just asks systemd
// for the unit state, which is sub-50ms on every host.
//
// Why a separate verb (not just `nginx.test`):
//
//   Server Status is polled every ~30s from any /jabali-admin/* tab
//   the operator has open. nginx.test runs `nginx -t` which, on hosts
//   with the CrowdSec AppSec acquis loaded across many vhosts, takes
//   ~19s at >60% CPU because Coraza compiles the full CRS rule set
//   per-vhost during parse. The dashboard does not need a syntactic
//   verdict on the live config — it needs âis nginx running and
//   serving trafficâ. systemctl is-active answers that for free.
//
// nginx.test still exists and is still the gate for nginx.reload —
// it is correct + necessary there because a broken config silently
// keeps the old worker running on SIGHUP, and we want a hard reject
// at the reload boundary instead of a stale-config surprise.

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// nginxStatusResponse is the output shape for nginx.status.
type nginxStatusResponse struct {
	// Active mirrors `systemctl is-active nginx` returning "active".
	Active bool `json:"active"`
	// State is the raw systemd unit state ("active", "activating",
	// "failed", "inactive"). Useful for distinguishing a transient
	// reload-in-progress from a hard failure on the UI side.
	State string `json:"state"`
	// MainPID is nginx\'s master PID from systemd, 0 when inactive.
	// Surfaced for the dashboard tile so operators can spot a flapping
	// service (PID changing between polls).
	MainPID int `json:"main_pid"`
}

func nginxStatusHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	state := readNginxSystemdState(ctx)
	pid := readNginxMainPID(ctx)
	resp := nginxStatusResponse{
		Active:  state == "active" || state == "reloading",
		State:   state,
		MainPID: pid,
	}
	if !resp.Active {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "nginx not running: state=" + state,
		}
	}
	return resp, nil
}

// readNginxSystemdState shells out to `systemctl is-active nginx`. The
// command prints the state on stdout AND exits non-zero for any
// state other than "active", so we read stdout regardless of exit code.
func readNginxSystemdState(ctx context.Context) string {
	cmd := execCommandContext(ctx, "systemctl", "is-active", "nginx")
	var out bytes.Buffer
	cmd.Stdout = &out
	_ = cmd.Run()
	return strings.TrimSpace(out.String())
}

// readNginxMainPID asks systemd for nginx.service MainPID. Returns 0
// when the service is inactive or the lookup fails — callers should
// treat 0 as "no live worker", not as an error.
func readNginxMainPID(ctx context.Context) int {
	cmd := execCommandContext(ctx, "systemctl", "show", "-p", "MainPID", "--value", "nginx")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return 0
	}
	v, err := strconv.Atoi(strings.TrimSpace(out.String()))
	if err != nil {
		return 0
	}
	return v
}

func init() {
	Default.Register("nginx.status", nginxStatusHandler)
}
