package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// dbUserDropParams is the input shape for db_user.drop.
type dbUserDropParams struct {
	DBUserName string `json:"db_user_name"`
}

// dbUserDropResponse is the output shape for db_user.drop.
type dbUserDropResponse struct {
	OK bool `json:"ok"`
}

// dbUserDropNameRegex validates MariaDB database user name format.
var dbUserDropNameRegex = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,63}$`)

func dbUserDropHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbUserDropParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// Validate db_user_name format.
	if !dbUserDropNameRegex.MatchString(p.DBUserName) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid database user name",
		}
	}

	// Escape the username literal for the 'name'@'localhost' form.
	escapedUsername, err := EscapeMariaDBLiteral(p.DBUserName)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid username",
		}
	}

	// Build the DROP USER IF EXISTS command.
	// Form: DROP USER IF EXISTS '<name>'@'localhost';
	sql := fmt.Sprintf("DROP USER IF EXISTS %s@'localhost'", escapedUsername)

	cmd := execCommandContext(ctx, "mysql", "-e", sql)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to drop user: %v; stderr=%q", err, truncateStr(stderr.String(), 300)),
		}
	}

	return dbUserDropResponse{OK: true}, nil
}

func init() {
	Default.Register("db_user.drop", dbUserDropHandler)
}
