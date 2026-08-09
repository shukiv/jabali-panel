package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// dbSizeParams is the input shape for db.size.
type dbSizeParams struct {
	DBName string `json:"db_name"`
	// Engine selects the size query. "postgres" uses pg_database_size();
	// "mariadb" / "mysql" / "" (a pre-#1005 panel that omits the field) use the
	// MariaDB information_schema sum. GH #1005: without this a Postgres database
	// was queried against MariaDB's information_schema and always summed to 0 B.
	Engine string `json:"engine"`
}

// dbSizeResponse is the output shape for db.size.
type dbSizeResponse struct {
	SizeBytes int64 `json:"size_bytes"`
}

// dbSizeNameRegex validates a database name for both engines: start with a
// letter, then letters/digits/underscores/hyphens, max 64 chars. Because a
// valid name contains no quote or SQL metacharacter, it is safe to interpolate
// as a literal into either the MariaDB or the Postgres query below.
var dbSizeNameRegex = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)

// dbSizeExec runs a size-query command and returns its stdout. Package var so
// tests can fake DB access (GH #994); production shells out for real.
var dbSizeExec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}

func dbSizeHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbSizeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// Validate db_name format (shared by both engines).
	if !dbSizeNameRegex.MatchString(p.DBName) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid database name",
		}
	}

	if p.Engine == "postgres" {
		return dbSizeResponse{SizeBytes: dbSizePostgres(ctx, p.DBName)}, nil
	}
	return dbSizeResponse{SizeBytes: dbSizeMariaDB(ctx, p.DBName)}, nil
}

// dbSizeMariaDB sums data_length+index_length across the database's tables.
// Degrades to 0 (never errors) so the list endpoint can't 500 on one bad row.
func dbSizeMariaDB(ctx context.Context, dbName string) int64 {
	escapedDBName, err := EscapeMariaDBLiteral(dbName)
	if err != nil {
		return 0
	}
	sql := fmt.Sprintf(
		"SELECT COALESCE(SUM(data_length+index_length),0) FROM information_schema.tables WHERE table_schema = %s",
		escapedDBName,
	)
	out, err := dbSizeExec(ctx, "mysql", "-Nse", sql)
	if err != nil {
		return 0
	}
	return parseSizeOutput(out)
}

// dbSizePostgres reads pg_database_size over the local peer socket (same
// `sudo -u postgres psql` path the other Postgres handlers use). dbName is
// regex-validated above, so the single-quoted literal cannot inject. Degrades
// to 0 on any error (missing DB, psql unavailable) exactly like the MariaDB path.
func dbSizePostgres(ctx context.Context, dbName string) int64 {
	sql := fmt.Sprintf("SELECT pg_database_size('%s')", dbName)
	out, err := dbSizeExec(ctx, "sudo", "-u", "postgres", "psql", "-tAq", "-c", sql)
	if err != nil {
		return 0
	}
	return parseSizeOutput(out)
}

// parseSizeOutput turns a single numeric line into an int64, or 0 if malformed.
func parseSizeOutput(out []byte) int64 {
	s := strings.TrimSpace(string(out))
	if s == "" {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func init() {
	Default.Register("db.size", dbSizeHandler)
}
