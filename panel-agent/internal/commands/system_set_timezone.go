package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// systemSetTimezoneParams is the input shape for system.set_timezone.
type systemSetTimezoneParams struct {
	Timezone string `json:"timezone"`
}

// systemSetTimezoneResponse is the output shape. Returns the timezone that
// was actually applied so the panel can confirm the round-trip succeeded.
type systemSetTimezoneResponse struct {
	Timezone string `json:"timezone"`
}

// systemSetTimezoneHandler applies a new timezone to the OS via timedatectl.
// The timezone is validated against /usr/share/zoneinfo before applying.
func systemSetTimezoneHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p systemSetTimezoneParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	p.Timezone = strings.TrimSpace(p.Timezone)
	if p.Timezone == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "timezone cannot be empty",
		}
	}

	// Basic format validation: reject .. and leading /
	if strings.Contains(p.Timezone, "..") || strings.HasPrefix(p.Timezone, "/") {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid timezone format",
		}
	}

	// Verify the timezone exists in /usr/share/zoneinfo.
	// Use os.Lstat to avoid following symlinks that might escape the tree.
	zonetabPath := "/usr/share/zoneinfo/" + p.Timezone
	info, err := os.Lstat(zonetabPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: fmt.Sprintf("timezone not found: %s", p.Timezone),
			}
		}
		return nil, fmt.Errorf("stat zoneinfo: %w", err)
	}

	// Reject directories (must be a regular file)
	if info.IsDir() {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "timezone path must be a file, not a directory",
		}
	}

	// Apply via timedatectl.
	cmd := execCommandContext(ctx, "timedatectl", "set-timezone", p.Timezone)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("timedatectl failed: %s: %w", strings.TrimSpace(string(out)), err)
	}

	return systemSetTimezoneResponse{Timezone: p.Timezone}, nil
}

func init() {
	Default.Register("system.set_timezone", systemSetTimezoneHandler)
}
