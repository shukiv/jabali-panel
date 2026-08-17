package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// dbUserRevokeParams is the input shape for db_user.revoke.
type dbUserRevokeParams struct {
	DBName     string   `json:"db_name"`
	DBUserName string   `json:"db_user_name"`
	GrantLevel string   `json:"grant_level"` // "rw" or "ro" (legacy, fallback)
	Privileges []string `json:"privileges"` // ["SELECT", "INSERT", ...] or ["ALL"]
}

// dbUserRevokeResponse is the output shape for db_user.revoke.
type dbUserRevokeResponse struct {
	OK bool `json:"ok"`
}

var dbUserRevokeNameRegex = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,63}$`)

func dbUserRevokeHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbUserRevokeParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// Validate db_name format.
	if !dbUserRevokeNameRegex.MatchString(p.DBName) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid database name",
		}
	}

	// Validate db_user_name format.
	if !dbUserRevokeNameRegex.MatchString(p.DBUserName) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid database user name",
		}
	}

	// Determine which privilege list to use: privileges (new) or fallback to grant_level (legacy).
	var privStr string
	if len(p.Privileges) > 0 {
		// Use privileges array.
		normalized, err := validateAndNormalizePrivileges(p.Privileges)
		if err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: fmt.Sprintf("invalid privileges: %v", err),
			}
		}
		privStr = normalized
	} else {
		// Fallback to grant_level for backward compatibility.
		if p.GrantLevel == "rw" {
			privStr = "ALL"
		} else if p.GrantLevel == "ro" {
			privStr = "SELECT"
		} else {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: "either privileges or valid grant_level must be provided",
			}
		}
	}

	// Escape database name using backticks.
	escapedDBName, err := EscapeMariaDBIdentifier(p.DBName)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid database name",
		}
	}

	// Escape username literal for the 'name'@'localhost' form.
	escapedUsername, err := EscapeMariaDBLiteral(p.DBUserName)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid username",
		}
	}

	// Build the REVOKE command.
	var revokeSql string
	if privStr == "ALL" {
		revokeSql = fmt.Sprintf(
			"REVOKE ALL PRIVILEGES ON %s.* FROM %s@'localhost'",
			escapedDBName,
			escapedUsername,
		)
	} else {
		revokeSql = fmt.Sprintf(
			"REVOKE %s ON %s.* FROM %s@'localhost'",
			privStr,
			escapedDBName,
			escapedUsername,
		)
	}

	// Issue the REVOKE and FLUSH PRIVILEGES in one command.
	sql := revokeSql + "; FLUSH PRIVILEGES"

	cmd := execCommandContext(ctx, "mysql", "-e", sql)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Treat "no such grant" as idempotent success — the caller wants
		// privileges gone, and they already are.
		se := strings.ToLower(stderr.String())
		if strings.Contains(se, "there is no such grant") || strings.Contains(se, "nonexistent grant") {
			return dbUserRevokeResponse{OK: true}, nil
		}
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to revoke privileges: %v; stderr=%q", err, truncateStr(stderr.String(), 300)),
		}
	}

	return dbUserRevokeResponse{OK: true}, nil
}

func init() {
	Default.Register("db_user.revoke", dbUserRevokeHandler)
}
