package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"

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
	if err := reservedDatabaseRefusal(p.DBName); err != nil {
		return nil, err
	}

	// Validate db_user_name format.
	if !dbUserRevokeNameRegex.MatchString(p.DBUserName) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid database user name",
		}
	}
	if err := reservedDBUserRefusal(p.DBUserName); err != nil {
		return nil, err
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

	// A grant is held under the escaped name (db_user.grant escapes the
	// GRANT wildcards) or, when it predates that, under the plain name.
	// Revoke both forms; either may be absent.
	escapedDBName, err := EscapeMariaDBGrantDB(p.DBName)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid database name",
		}
	}
	legacyDBName, err := EscapeMariaDBIdentifier(p.DBName)
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

	privClause := privStr
	if privStr == "ALL" {
		privClause = "ALL PRIVILEGES"
	}
	for _, dbIdent := range []string{escapedDBName, legacyDBName} {
		// One statement per call: the client stops at the first error, and
		// "no such grant" on one form must not skip the other.
		revokeSql := fmt.Sprintf("REVOKE %s ON %s.* FROM %s@'localhost'", privClause, dbIdent, escapedUsername)
		cmd := execCommandContext(ctx, "mysql", "-e", revokeSql)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			// "No such grant" is idempotent success: the caller wants the
			// privileges gone, and under this form they already are.
			if isNoSuchGrant(stderr.String()) {
				continue
			}
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("failed to revoke privileges: %v; stderr=%q", err, truncateStr(stderr.String(), 300)),
			}
		}
	}
	if err := execCommandContext(ctx, "mysql", "-e", "FLUSH PRIVILEGES").Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to flush privileges: %v", err),
		}
	}

	return dbUserRevokeResponse{OK: true}, nil
}

func init() {
	Default.Register("db_user.revoke", dbUserRevokeHandler)
}
