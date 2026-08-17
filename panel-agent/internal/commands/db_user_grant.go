package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// dbUserGrantParams is the input shape for db_user.grant.
type dbUserGrantParams struct {
	DBName     string   `json:"db_name"`
	DBUserName string   `json:"db_user_name"`
	GrantLevel string   `json:"grant_level"` // "rw" or "ro" (legacy, fallback)
	Privileges []string `json:"privileges"` // ["SELECT", "INSERT", ...] or ["ALL"]
}

// dbUserGrantResponse is the output shape for db_user.grant.
type dbUserGrantResponse struct {
	OK bool `json:"ok"`
}

var dbUserGrantNameRegex = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,63}$`)

// Whitelist of valid privilege tokens.
const privilegeWhitelist = "SELECT,INSERT,UPDATE,DELETE,CREATE,DROP,ALTER,INDEX,ALL"

// validateAndNormalizePrivileges validates and normalizes the privilege list.
func validateAndNormalizePrivileges(privs []string) (string, error) {
	if len(privs) == 0 {
		return "", fmt.Errorf("privileges list is empty")
	}

	// Check if "ALL" is present (case-insensitive).
	for _, p := range privs {
		if p == "ALL" {
			return "ALL", nil
		}
	}

	// Whitelist of valid tokens.
	validTokens := map[string]bool{
		"SELECT": true,
		"INSERT": true,
		"UPDATE": true,
		"DELETE": true,
		"CREATE": true,
		"DROP":   true,
		"ALTER":  true,
		"INDEX":  true,
	}

	// Canonical order.
	canonicalOrder := []string{"SELECT", "INSERT", "UPDATE", "DELETE", "CREATE", "DROP", "ALTER", "INDEX"}
	seen := make(map[string]bool)

	// Validate each privilege token.
	for _, p := range privs {
		if !validTokens[p] {
			return "", fmt.Errorf("invalid privilege: %s", p)
		}
		if !seen[p] {
			seen[p] = true
		}
	}

	// Build canonical string in canonical order, deduped.
	var result []string
	for _, canonical := range canonicalOrder {
		if seen[canonical] {
			result = append(result, canonical)
		}
	}

	if len(result) == 0 {
		return "", fmt.Errorf("no valid privileges")
	}

	// Return as comma-separated string.
	canonicalStr := ""
	for i, p := range result {
		if i > 0 {
			canonicalStr += ","
		}
		canonicalStr += p
	}
	return canonicalStr, nil
}

func dbUserGrantHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbUserGrantParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// Validate db_name format.
	if !dbUserGrantNameRegex.MatchString(p.DBName) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid database name",
		}
	}

	// Validate db_user_name format.
	if !dbUserGrantNameRegex.MatchString(p.DBUserName) {
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

	// Build the GRANT command.
	var grantSql string
	if privStr == "ALL" {
		grantSql = fmt.Sprintf(
			"GRANT ALL PRIVILEGES ON %s.* TO %s@'localhost'",
			escapedDBName,
			escapedUsername,
		)
	} else {
		grantSql = fmt.Sprintf(
			"GRANT %s ON %s.* TO %s@'localhost'",
			privStr,
			escapedDBName,
			escapedUsername,
		)
	}

	// Issue the GRANT and FLUSH PRIVILEGES in one command.
	sql := grantSql + "; FLUSH PRIVILEGES"

	cmd := execCommandContext(ctx, "mysql", "-e", sql)
	if err := cmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "failed to grant privileges",
		}
	}

	return dbUserGrantResponse{OK: true}, nil
}

func init() {
	Default.Register("db_user.grant", dbUserGrantHandler)
}
