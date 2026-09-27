package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// migration.secrets_wipe removes a migration job's source credentials
// (/etc/jabali-panel/migration-secrets/<job-id>.env) when the panel cancels or
// destroys the job (JAB-357). The panel runs as the jabali user and cannot
// unlink in the root:jabali 0750 secrets directory, so its own
// migrate.WipeJobSecret only ever worked from root (the import runner).
//
// Only <job-id>.env is removed, and only a regular file: the host-key pin and
// every other name stay, as in the reaper. A missing file is success.

type migrationSecretsWipeParams struct {
	JobID string `json:"job_id"`
}

func init() {
	Default.Register("migration.secrets_wipe", migrationSecretsWipeHandler)
}

func migrationSecretsWipeHandler(_ context.Context, raw json.RawMessage) (any, error) {
	var p migrationSecretsWipeParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "malformed JSON: " + err.Error()}
	}
	if !migrationJobIDRe.MatchString(p.JobID) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "job_id must be 26-char alnum (ULID)"}
	}
	removed, err := removeMigrationSecret(migrationSecretsBaseDir, p.JobID)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	return map[string]bool{"removed": removed}, nil
}

// removeMigrationSecret removes dir/<jobID>.env and reports whether it did.
// jobID must already be validated.
func removeMigrationSecret(dir, jobID string) (bool, error) {
	path := filepath.Join(dir, jobID+".env")
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	if !fi.Mode().IsRegular() {
		return false, fmt.Errorf("%s is not a regular file; left in place", path)
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("remove %s: %w", path, err)
	}
	return true, nil
}
