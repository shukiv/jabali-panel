package uploadintake

import (
	"context"
	"encoding/json"
)

// IngestMethod is the agent verb that moves a staged file into a tenant tree.
const IngestMethod = "files.ingest"

// IngestParams are the files.ingest params. The JSON tags are the agent wire
// contract (panel-agent/internal/commands/files_ingest.go filesIngestParams).
type IngestParams struct {
	Overwrite bool   `json:"overwrite,omitempty"`
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root,omitempty"`
	TmpPath   string `json:"tmp_path"`
	DestPath  string `json:"dest_path"`
}

// CallFunc is an agent RPC call: agent.AgentInterface.Call on panel-api, the
// CLI's agent client on `jabali`.
type CallFunc func(ctx context.Context, method string, params any) (json.RawMessage, error)

// Ingest hands a staged file to the agent. On success the agent has moved it
// into the tenant tree; on failure Ingest discards it, so a failed ingest never
// leaves staging behind to count against the owner's budget.
func Ingest(ctx context.Context, call CallFunc, p IngestParams) error {
	if _, err := call(ctx, IngestMethod, p); err != nil {
		Discard(p.TmpPath)
		return err
	}
	return nil
}
