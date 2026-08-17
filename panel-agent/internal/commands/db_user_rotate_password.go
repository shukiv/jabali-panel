package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// dbUserRotatePasswordParams is the input shape for db_user.rotate_password.
type dbUserRotatePasswordParams struct {
	DBUserName  string `json:"db_user_name"`
	NewPassword string `json:"new_password"`
}

// dbUserRotatePasswordResponse is the output shape for db_user.rotate_password.
type dbUserRotatePasswordResponse struct {
	OK bool `json:"ok"`
}

var dbUserRotatePasswordNameRegex = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,63}$`)

func dbUserRotatePasswordHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbUserRotatePasswordParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// Validate db_user_name format.
	if !dbUserRotatePasswordNameRegex.MatchString(p.DBUserName) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid database user name",
		}
	}

	if p.NewPassword == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "new_password cannot be empty",
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

	// Escape the new password literal.
	escapedPassword, err := EscapeMariaDBLiteral(p.NewPassword)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid password",
		}
	}

	// Build the ALTER USER command.
	// Form: ALTER USER '<name>'@'localhost' IDENTIFIED BY '<newpw>';
	sql := fmt.Sprintf(
		"ALTER USER %s@'localhost' IDENTIFIED BY %s",
		escapedUsername,
		escapedPassword,
	)

	cmd := execCommandContext(ctx, "mysql", "-e", sql)
	if err := cmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "failed to rotate password",
		}
	}

	return dbUserRotatePasswordResponse{OK: true}, nil
}

func init() {
	Default.Register("db_user.rotate_password", dbUserRotatePasswordHandler)
}
