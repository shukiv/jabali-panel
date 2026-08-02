// backup.forget — manually delete one backup run's snapshots from a restic
// repo (GH #294). The caller (panel-api) resolves the run's destination →
// repo_url + creds and passes the run's job_id; we list the exact snapshots
// tagged job-id=<id>, forget those precise IDs, and prune. Deterministic
// (never a blind `forget --tag`, which can over-match or need a keep policy)
// and idempotent (no snapshots → success, so the caller can drop the DB row
// even for a backup that never wrote anything).
package commands

import (
	"context"
	"encoding/json"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
)

type backupForgetParams struct {
	JobID          string            `json:"job_id"`
	UserID         string            `json:"user_id"`
	Kind           string            `json:"kind"` // job-slot lock key (account_backup|system_backup)
	RepoURL        string            `json:"repo_url"`
	CredentialsRef string            `json:"credentials_ref,omitempty"`
	PasswordFile   string            `json:"password_file,omitempty"`
	SFTP           *backupSFTPInputs `json:"sftp,omitempty"`
}

type backupForgetResponse struct {
	JobID   string `json:"job_id"`
	Removed int    `json:"removed"`
}

func backupForgetHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p backupForgetParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, bkInvalidArg(fmt.Sprintf("parse: %v", err))
	}
	if p.JobID == "" {
		return nil, bkInvalidArg("job_id required")
	}
	// Empty repo_url -> the agent's default local repo (matches backup.create,
	// which me-backups call with no destination). The lock kind is the literal
	// "backup" string create uses, so forget and create are mutually exclusive
	// on the same user+repo (avoids a restic "repository is already locked").
	if existing, ok := defaultJobSlots.acquire("backup", p.UserID, p.RepoURL, p.JobID); !ok {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeFailedPrecondition,
			Message: fmt.Sprintf("another backup operation (%s) is running on this repository — retry once it finishes", existing),
		}
	}
	defer defaultJobSlots.release("backup", p.UserID, p.RepoURL)

	cfg, cerr := bkResticConfigWithPassword(p.RepoURL, p.CredentialsRef, p.PasswordFile, p.SFTP)
	if cerr != nil {
		return nil, bkInternal("restic config", cerr)
	}
	c := backup.New(cfg)

	snaps, err := c.Snapshots(ctx, []backup.Tag{backup.Tag(backup.TagKeyJobID + "=" + p.JobID)})
	if err != nil {
		return nil, bkInternal("list snapshots for forget", err)
	}
	if len(snaps) == 0 {
		// Nothing to remove — idempotent success.
		return &backupForgetResponse{JobID: p.JobID, Removed: 0}, nil
	}
	ids := make([]string, 0, len(snaps))
	for _, s := range snaps {
		ids = append(ids, s.ID)
	}
	if _, err := c.ForgetIDs(ctx, ids, true); err != nil {
		return nil, bkInternal("restic forget", err)
	}
	return &backupForgetResponse{JobID: p.JobID, Removed: len(ids)}, nil
}

func init() {
	Default.Register("backup.forget", backupForgetHandler)
}
