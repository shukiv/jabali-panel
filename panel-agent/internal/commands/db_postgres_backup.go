package commands

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/hostreserve"
)

// db.postgres.backup — user-facing per-database dump for the tenant Databases
// "Download backup" action (GH #1045 PostgreSQL parity). The MariaDB sibling is
// db.backup (db_backup.go); this mirrors it for Postgres so the API can dispatch
// by engine and stream the .sql back to the client the same way.
//
// Two things it deliberately shares with db.backup rather than db.postgres.dump
// (the internal account_full helper):
//
//   - It writes to the panel-readable staging dir and re-owns the dump to the
//     panel service user (reownBackupForPanel), so panel-api — which runs
//     unprivileged — can read the file back and unlink it after streaming. The
//     internal dump targets /var/lib/jabali (root-only) and is never streamed to
//     a client, so it skips this (GH #1045 500 was exactly this handoff).
//   - pg_dump runs as `postgres` (peer auth on the socket) but its stdout is an
//     fd the AGENT (root) opened under /var/lib/jabali-uploads. The staging dir
//     is 0750 jabali:jabali-sockets — the postgres user cannot create a file
//     there — so `pg_dump -f <staging>` would EACCES. Handing pg_dump an
//     already-open fd via Stdout sidesteps that: postgres writes to the fd, not
//     the path (same reason db_backup.go captures mysqldump's stdout).
//
// Dump flags: --no-owner --no-privileges (matching db.postgres.dump). Ownership
// and grants are NOT carried in the dump on purpose — a privilege-scoped
// restore (the planned db.postgres.restore, loaded as a NON-superuser role)
// cannot execute `ALTER ... OWNER TO <role>` / `GRANT`, so ownership must be
// re-established by an agent-authored superuser post-pass at restore time, never
// from tenant-supplied dump content. Plain format (-Fp) keeps the download a
// human-readable .sql, consistent with the MariaDB path.

type dbPgBackupParams struct {
	DBName string `json:"db_name"`
	Path   string `json:"path"`
}

type dbPgBackupResponse struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
}

func dbPgBackupHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbPgBackupParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// pgValidIdent (db_postgres.go): first char a letter, [a-zA-Z0-9_-], and no
	// quote/semicolon/space/backslash. The name is a pg_dump positional argument
	// (no shell), so this is a second layer, not the only one.
	if !pgValidIdent(p.DBName) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid database name",
		}
	}

	backupPath := p.Path
	if backupPath == "" {
		buf := make([]byte, 12)
		if _, err := rand.Read(buf); err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: "failed to generate backup path",
			}
		}
		backupPath = fmt.Sprintf("%s/jabali-pgdb-backup-%s.sql", dbBackupStagingDir, hex.EncodeToString(buf))
	}

	// Confine the output to the staging dir and reject traversal — identical to
	// db.backup, since panel-api reads back exactly this path.
	if !strings.HasPrefix(backupPath, dbBackupStagingDir+"/") {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "backup path must be under " + dbBackupStagingDir + "/",
		}
	}
	if strings.Contains(backupPath, "..") {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "backup path contains invalid characters",
		}
	}

	// JAB-244 admission: shared dump slots (per-db cap 1, host-wide 2) keyed
	// "pg:<db>" — the same key db.postgres.dump uses, so the two backup paths
	// count against one another. Refusals are retryable-unavailable.
	release, ok := dbBackupSlots.TryAcquire("pg:" + p.DBName)
	if !ok {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeUnavailable,
			Message: "too many concurrent database backups — retry shortly",
		}
	}
	defer release()
	if err := hostreserve.CheckReserve(dbBackupStagingDir, 0); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeUnavailable,
			Message: "backup staging is under the host disk reserve: " + err.Error(),
		}
	}

	if err := os.MkdirAll(dbBackupStagingDir, 0750); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "failed to create backup staging directory",
		}
	}

	// Root opens the output fd; pg_dump (as postgres) writes to it via Stdout.
	f, err := os.Create(backupPath)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "failed to create backup file",
		}
	}
	defer f.Close()

	cmd := execCommandContext(ctx, "sudo", "-u", "postgres", "pg_dump",
		"--no-owner", "--no-privileges", "-Fp", p.DBName)
	// GuardedWriter stops the dump mid-stream if concurrent writers eat the disk
	// below the host reserve (256 MiB re-checks) — same backstop as db.backup.
	cmd.Stdout = hostreserve.GuardedWriter(f, dbBackupStagingDir)
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		_ = os.Remove(backupPath)
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "failed to backup database: " + strings.TrimSpace(stderr.String()),
		}
	}

	// Hand the dump to the panel service user so panel-api can stream + unlink it
	// (GH #1045). See reownBackupForPanel in db_backup.go.
	if err := reownBackupForPanel(backupPath); err != nil {
		_ = os.Remove(backupPath)
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "failed to finalize backup file",
		}
	}

	info, err := os.Stat(backupPath)
	if err != nil {
		_ = os.Remove(backupPath)
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "failed to stat backup file",
		}
	}

	return dbPgBackupResponse{
		Path:      backupPath,
		SizeBytes: info.Size(),
	}, nil
}

func init() {
	Default.Register("db.postgres.backup", dbPgBackupHandler)
}
