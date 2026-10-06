package commands

import (
	"context"
	"encoding/json"
	"runtime"
	"time"
)

// Build-time metadata. main.go sets Version via -ldflags before the init
// chain runs, so this file reads whatever's current at registration time.
// StartTime is captured when the process boots, in main.go, for the same
// reason. Keeping these as package-level vars avoids plumbing them through
// every handler when we only need them here.
var (
	Version   = "dev"
	StartTime = time.Now()
)

// agentVersionResponse is the canonical shape for agent.version. Callers
// use it as a liveness probe and an upgrade sanity check ("is the agent we
// thought we had the one actually running?").
type agentVersionResponse struct {
	Version       string `json:"version"`
	GoVersion     string `json:"go_version"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	StartedAt     string `json:"started_at"`
	// Capabilities names behaviours the panel must confirm before it relies
	// on them; an older agent reports none, so the panel refuses rather than
	// run unprotected.
	Capabilities []string `json:"capabilities"`
}

// Capability names (agentVersionResponse.Capabilities).
const (
	// capRestoreUploadConfinement: backup.restore_from_tar honours mode=upload
	// (GH #1993).
	capRestoreUploadConfinement = "restore_upload_confinement"
	// capDBUserCreateOnly: db_user.create honours create_only (GH #1993).
	capDBUserCreateOnly = "db_user_create_only"
	// capRestoreKeepExisting: backup.restore_from_tar honours keep_existing
	// (GH #1993).
	capRestoreKeepExisting = "restore_keep_existing"
)

func agentVersionHandler(_ context.Context, _ json.RawMessage) (any, error) {
	now := time.Now()
	return agentVersionResponse{
		Version:       Version,
		GoVersion:     runtime.Version(),
		UptimeSeconds: int64(now.Sub(StartTime).Seconds()),
		StartedAt:     StartTime.UTC().Format(time.RFC3339),
		Capabilities:  []string{capRestoreUploadConfinement, capDBUserCreateOnly, capRestoreKeepExisting},
	}, nil
}

func init() {
	Default.Register("agent.version", agentVersionHandler)
}
