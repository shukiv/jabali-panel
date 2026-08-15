package commands

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// dbBackupChown is os.Chown in production; tests override it to assert the
// agent->panel ownership handoff without needing root.
var dbBackupChown = os.Chown

// dbBackupLookupUser resolves the panel service user; tests override it so they
// run on hosts (e.g. CI) where the "jabali" account does not exist.
var dbBackupLookupUser = func() (*user.User, error) { return user.Lookup(systemServiceUser) }

// reownBackupForPanel re-owns a freshly written dump to the panel service user
// and pins its mode to 0640. The dump is created by the agent (root) but
// streamed back to the HTTP client by panel-api, which runs as the unprivileged
// service user ("jabali"). A root:root 0640 file is unreadable by that user —
// it is not in the root group — so the panel's os.Open fails and the download
// 500s (GH #1045). Chowning the OWNER (not just the group) is deliberate:
// panel-api's systemd unit sets Group=jabali-sockets, so the service user's
// primary "jabali" group is only a supplementary group and is not guaranteed
// present on every install (see feedback_systemd_group_supplementary) —
// owner-read on 0640 works regardless. The backup dir is 0700 jabali:jabali, so
// the re-owned file stays reachable only by the panel, which can also unlink it
// after streaming. Mirrors the agent->panel handoff in sendmail_cred.go.
func reownBackupForPanel(path string) error {
	svc, err := dbBackupLookupUser()
	if err != nil {
		return fmt.Errorf("resolve service user: %w", err)
	}
	uid, err := strconv.Atoi(svc.Uid)
	if err != nil {
		return fmt.Errorf("service uid %q: %w", svc.Uid, err)
	}
	gid, err := strconv.Atoi(svc.Gid)
	if err != nil {
		return fmt.Errorf("service gid %q: %w", svc.Gid, err)
	}
	if err := dbBackupChown(path, uid, gid); err != nil {
		return fmt.Errorf("chown %s: %w", path, err)
	}
	if err := os.Chmod(path, 0640); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

// dbBackupParams is the input shape for db.backup.
type dbBackupParams struct {
	DBName string `json:"db_name"`
	Path   string `json:"path"`
}

// dbBackupResponse is the output shape for db.backup.
type dbBackupResponse struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
}

// dbBackupNameRegex validates MariaDB database name format.
var dbBackupNameRegex = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)

func dbBackupHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbBackupParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// Validate db_name format.
	if !dbBackupNameRegex.MatchString(p.DBName) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid database name",
		}
	}

	// Reject dangerous patterns (second layer of defense).
	if strings.Contains(p.DBName, "/") ||
		strings.Contains(p.DBName, "\\") ||
		strings.Contains(p.DBName, ";") ||
		strings.Contains(p.DBName, "\n") ||
		strings.Contains(p.DBName, "\r") ||
		strings.Contains(p.DBName, " ") ||
		strings.Contains(p.DBName, ".") {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid database name",
		}
	}

	backupPath := p.Path
	if backupPath == "" {
		// Generate default path with random hex string
		buf := make([]byte, 12)
		if _, err := rand.Read(buf); err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: "failed to generate backup path",
			}
		}
		backupPath = fmt.Sprintf("/var/lib/jabali/backups/%s.sql", hex.EncodeToString(buf))
	}

	// Validate path is under /var/lib/jabali/backups/ and reject directory traversal
	if !strings.HasPrefix(backupPath, "/var/lib/jabali/backups/") {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "backup path must be under /var/lib/jabali/backups/",
		}
	}

	// Reject directory traversal attempts
	if strings.Contains(backupPath, "..") {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "backup path contains invalid characters",
		}
	}

	// Ensure directory exists with mode 0700
	backupDir := "/var/lib/jabali/backups"
	if err := os.MkdirAll(backupDir, 0700); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "failed to create backup directory",
		}
	}

	// Create the backup file
	f, err := os.Create(backupPath)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "failed to create backup file",
		}
	}
	defer f.Close()

	// Run mysqldump with the database name as a positional argument (no interpolation).
	cmd := exec.CommandContext(
		ctx,
		"mysqldump",
		"--single-transaction",
		"--quick",
		"--lock-tables=0",
		"--",
		p.DBName,
	)
	cmd.Stdout = f

	if err := cmd.Run(); err != nil {
		// Remove partial file on failure
		_ = os.Remove(backupPath)
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "failed to backup database",
		}
	}

	// Hand the dump off to panel-api (runs as the unprivileged service user),
	// which streams it to the client. See reownBackupForPanel for why this is
	// required (GH #1045).
	if err := reownBackupForPanel(backupPath); err != nil {
		_ = os.Remove(backupPath)
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "failed to finalize backup file",
		}
	}

	// Get file size
	info, err := os.Stat(backupPath)
	if err != nil {
		_ = os.Remove(backupPath)
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "failed to stat backup file",
		}
	}

	return dbBackupResponse{
		Path:      backupPath,
		SizeBytes: info.Size(),
	}, nil
}

func init() {
	Default.Register("db.backup", dbBackupHandler)
}
