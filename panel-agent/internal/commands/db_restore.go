package commands

import (
	"git.jabali-panel.com/shukivaknin/jabali2/internal/hostreserve"
	"context"
	"encoding/json"
	"fmt"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/filesafe"
	"os"
	"regexp"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// dbRestoreParams is the input shape for db.restore.
type dbRestoreParams struct {
	DBName string `json:"db_name"`
	Path   string `json:"path"`
	// ResetBeforeRestore — ADR-0095 amendment 2026-05-12. When true,
	// the handler issues DROP DATABASE IF EXISTS + CREATE DATABASE
	// before streaming the dump. Makes the restore idempotent under
	// retry-resume (the M35.1 default retry path). Migration importer
	// + retry-from-scratch set this; first-time restores against a
	// freshly-provisioned DB don't need it. Default false keeps
	// historic behaviour intact.
	ResetBeforeRestore bool `json:"reset_before_restore,omitempty"`
}

// dbRestoreResponse is the output shape for db.restore.
type dbRestoreResponse struct {
	OK bool `json:"ok"`
}

// dbRestoreNameRegex validates MariaDB database name format.
var dbRestoreNameRegex = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)

func dbRestoreHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbRestoreParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// JAB-243 (partial): tenant DB storage lives under /var/lib/mysql,
	// outside every POSIX quota. A hard per-tenant cap needs tablespace/
	// project-quota work (tracked on the ticket); until then, at least
	// refuse to START loading tenant SQL when the DB filesystem is
	// already below the host reserve floor.
	if err := hostreserve.CheckReserve("/var/lib/mysql", 0); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeUnavailable,
			Message: "database storage is under the host disk reserve: " + err.Error(),
		}
	}

	// Validate db_name format.
	if !dbRestoreNameRegex.MatchString(p.DBName) {
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

	// Validate + open the restore file under an allowed directory using a
	// symlink-safe scope (Gitea #501). Raw string-prefix checks let a planted
	// symlink / .. component escape; filesafe resolves symlinks, verifies the
	// real path stays beneath an allowed root, and opens with O_NOFOLLOW.
	//   /var/lib/jabali/restore/    — interactive restores
	//   /var/lib/jabali-migrations/ — migration importer
	restoreScope, scErr := filesafe.NewScope("system", "system", []string{
		"/var/lib/jabali/restore", "/var/lib/jabali-migrations", "/var/lib/jabali/migrations",
	})
	if scErr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("scope: %v", scErr)}
	}
	// GH #530: rewrite ONLY the /var/lib/jabali-migrations compat-symlink root
	// prefix (GH #327) so filesafe's openat2 RESOLVE_BENEATH — which won't
	// traverse a symlink — doesn't ENOTDIR on the .sql open (which would
	// silently restore nothing). The tenant-extractable remainder is left
	// verbatim, so RESOLVE_BENEATH still rejects any symlink planted in it.
	p.Path = canonicalizeStagingRootPrefix(p.Path)
	f, err := restoreScope.Open(p.Path, os.O_RDONLY, 0)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "restore path invalid, outside an allowed directory, or not readable",
		}
	}
	defer f.Close()

	// If asked to reset, drop + recreate the DB FIRST so the dump
	// can stream into an empty schema. Idempotent: DROP IF EXISTS is
	// safe on a never-existed DB; CREATE always succeeds.
	if p.ResetBeforeRestore {
		resetSQL := fmt.Sprintf(
			"DROP DATABASE IF EXISTS `%s`; CREATE DATABASE `%s` CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;",
			p.DBName, p.DBName,
		)
		reset := execCommandContext(ctx, "mysql", "-e", resetSQL)
		if out, err := reset.CombinedOutput(); err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("reset failed: %v: %s", err, string(out)),
			}
		}
	}

	// Run the dump load through the db-scoped shadow account as an
	// unprivileged OS user (JAB-239) — never as root. See
	// db_load_scoped.go for the trust model.
	if err := loadMariaDBDumpScoped(ctx, p.DBName, f); err != nil {
		// Always delete the file, whether restore succeeds or fails — but
		// through the SCOPE, not os.Remove. The scope's RESOLVE_BENEATH
		// applies to the open above; a raw os.Remove(p.Path) re-walks the
		// unvalidated path string from /, so a symlink or a swapped parent
		// directory in between would send this ROOT-privileged unlink
		// somewhere the scope never approved (the Gitea #501 class, which the
		// open was already hardened against).
		_ = restoreScope.RemoveInScope(p.Path, false)
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "failed to restore database",
		}
	}

	// Delete the file on success — again through the scope (see above).
	// Cleanup failure does not fail the restore; it already succeeded.
	_ = restoreScope.RemoveInScope(p.Path, false)

	return dbRestoreResponse{OK: true}, nil
}

func init() {
	Default.Register("db.restore", dbRestoreHandler)
}
