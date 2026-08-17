package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// userSliceStatusParams is the input shape for user.slice.status.
type userSliceStatusParams struct {
	Username string `json:"username"`
}

// userSliceStatusResponse is the output shape for user.slice.status.
// Booleans are the unambiguous "is it running" signals; the counters are
// raw systemd values so the UI can format them however it likes. A
// username with no slice or masked/inactive units returns active=false
// and zero counters rather than an error — that's a valid state to
// render ("not provisioned yet").
type userSliceStatusResponse struct {
	Username           string `json:"username"`
	SliceActive        bool   `json:"slice_active"`
	FPMActive          bool   `json:"fpm_active"`
	MemoryCurrentBytes uint64 `json:"memory_current_bytes"`
	TasksCurrent       uint64 `json:"tasks_current"`
	CPUUsageNSec       uint64 `json:"cpu_usage_nsec"`
}

func userSliceStatusHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p userSliceStatusParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	// Reuse the strict username regex from user.slice.ensure. Avoids a
	// malicious caller passing "$(rm -rf /)" as a username and having it
	// interpolated into unit names. This is defence-in-depth; the regex
	// prevents path characters that systemd wouldn't accept anyway.
	if !userSliceUsernameRegex.MatchString(p.Username) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid username %q", p.Username),
		}
	}

	sliceUnit := fmt.Sprintf("jabali-user-%s.slice", p.Username)
	fpmUnit := fmt.Sprintf("jabali-fpm@%s.service", p.Username)

	resp := userSliceStatusResponse{Username: p.Username}
	resp.SliceActive = systemctlUnitActive(ctx, sliceUnit)
	resp.FPMActive = systemctlUnitActive(ctx, fpmUnit)

	// One `systemctl show` call fetches all three properties at once —
	// cheaper than three separate calls and atomic with respect to a
	// slice that stops between calls.
	props, err := systemctlShow(ctx, sliceUnit, "MemoryCurrent", "TasksCurrent", "CPUUsageNSec")
	if err == nil {
		resp.MemoryCurrentBytes = parseUintProperty(props["MemoryCurrent"])
		resp.TasksCurrent = parseUintProperty(props["TasksCurrent"])
		resp.CPUUsageNSec = parseUintProperty(props["CPUUsageNSec"])
	}
	// If the slice doesn't exist, `systemctl show` returns values like
	// "[not set]" or the sentinel 2^64-1. parseUintProperty maps both
	// to zero so the response stays useful.

	return resp, nil
}

// systemctlUnitActive returns true when `systemctl is-active <unit>` exits 0.
func systemctlUnitActive(ctx context.Context, unit string) bool {
	cmd := execCommandContext(ctx, "systemctl", "is-active", "--quiet", unit)
	return cmd.Run() == nil
}

// systemctlShow runs `systemctl show <unit> -p <prop1> -p <prop2> ...`
// and parses the `Key=Value` lines into a map.
func systemctlShow(ctx context.Context, unit string, props ...string) (map[string]string, error) {
	args := []string{"show", unit}
	for _, p := range props {
		args = append(args, "-p", p)
	}
	cmd := execCommandContext(ctx, "systemctl", args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(props))
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		result[k] = v
	}
	return result, nil
}

// parseUintProperty parses a systemd property like "12345678" to uint64.
// systemd uses 2^64-1 as "unset/infinity" for numeric counters; map that
// to 0 so callers don't have to special-case it.
func parseUintProperty(v string) uint64 {
	if v == "" || v == "[not set]" {
		return 0
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0
	}
	// ^uint64(0) is the "infinity" sentinel systemd emits for unset
	// counters. Treat as zero.
	if n == ^uint64(0) {
		return 0
	}
	return n
}

func init() {
	Default.Register("user.slice.status", userSliceStatusHandler)
}
