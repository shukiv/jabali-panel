package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// dbCreateParams is the input shape for db.create.
type dbCreateParams struct {
	DBName    string `json:"db_name"`
	Charset   string `json:"charset"`
	Collation string `json:"collation"`
}

// dbCreateResponse is the output shape for db.create.
type dbCreateResponse struct {
	OK bool `json:"ok"`
}

// dbNameRegex validates MariaDB database name format.
// Must start with letter, contain only letters, digits, underscores, hyphens.
// Max 64 chars (MariaDB limit for unquoted identifiers).
var dbNameRegex = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`)

func dbCreateHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbCreateParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// Validate db_name format.
	if !dbNameRegex.MatchString(p.DBName) {
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

	// Escape the database name using backticks.
	escapedDBName, err := EscapeMariaDBIdentifier(p.DBName)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid database name",
		}
	}

	// Default charset/collation if not provided.
	if p.Charset == "" {
		p.Charset = "utf8mb4"
	}
	if p.Collation == "" {
		p.Collation = "utf8mb4_unicode_ci"
	}

	// Escape charset and collation (string literals).
	escapedCharset, err := EscapeMariaDBLiteral(p.Charset)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid charset",
		}
	}

	escapedCollation, err := EscapeMariaDBLiteral(p.Collation)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid collation",
		}
	}

	// Build the CREATE DATABASE command.
	// The agent runs as root, and on Debian MariaDB root@localhost uses
	// unix_socket auth by default — so `mysql -e ...` works with no
	// password file. M9 hardener may later inject /root/.my.cnf for
	// remote-admin scenarios; current path is intentionally socket-only.
	sql := fmt.Sprintf(
		"CREATE DATABASE %s CHARACTER SET %s COLLATE %s",
		escapedDBName,
		escapedCharset,
		escapedCollation,
	)

	cmd := execCommandContext(ctx, "mysql", "-e", sql)
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to create database: %v: %s", runErr, strings.TrimSpace(string(out))),
		}
	}

	return dbCreateResponse{OK: true}, nil
}

func init() {
	Default.Register("db.create", dbCreateHandler)
}
