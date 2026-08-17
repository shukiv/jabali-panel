package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// dbUserCreateParams is the input shape for db_user.create.
type dbUserCreateParams struct {
	DBUserName string `json:"db_user_name"`
	// Password is the plaintext for normal panel-driven creation.
	Password   string `json:"password,omitempty"`
	// PasswordHash is the mysql_native_password old-style hash
	// (`*` + 40 hex). Set ONLY by the M35 migration importer when
	// recreating the source-side MySQL user from a cpmove `mysql.sql`
	// grant so the migrated app's hardcoded creds keep working with
	// zero config rewrite. Mutually exclusive with Password.
	PasswordHash string `json:"password_hash,omitempty"`
}

// nativePwdHashRe pins the supported hash format (mysql_native_password).
var nativePwdHashRe = regexp.MustCompile(`^\*[0-9A-Fa-f]{40}$`)


// dbUserCreateResponse is the output shape for db_user.create.
type dbUserCreateResponse struct {
	OK bool `json:"ok"`
}

// dbUserNameRegex validates MariaDB database user name format.
// Must start with letter, contain only letters, digits, underscores.
// Max 63 chars to leave room for @localhost suffix.
var dbUserNameRegex = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]{0,63}$`)

func dbUserCreateHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbUserCreateParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// Validate db_user_name format.
	if !dbUserNameRegex.MatchString(p.DBUserName) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid database user name",
		}
	}

	if (p.Password == "" && p.PasswordHash == "") || (p.Password != "" && p.PasswordHash != "") {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "exactly one of password or password_hash must be provided",
		}
	}
	if p.PasswordHash != "" && !nativePwdHashRe.MatchString(p.PasswordHash) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid password_hash format (expected mysql_native_password '*' + 40 hex)",
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

	// Build the CREATE USER command. IF NOT EXISTS makes the M35
	// importer's repeat invocations idempotent (a re-run / resume
	// must not 1396 ER_CANNOT_USER on an already-created compat user).
	var sql string
	if p.PasswordHash != "" {
		// Migration-compat path. Hash format already validated above;
		// no escaping needed (hex + '*' is shell+SQL-safe and the
		// MariaDB grammar requires literal single-quotes around it).
		//
		// CREATE ... IF NOT EXISTS keeps resume idempotent, but on its own it
		// LEAVES an existing user's password untouched — and jabali's own dump
		// loop may have already created a panel-managed user of the SAME name
		// (whenever the target jabali username matches the source account
		// prefix, e.g. johnnyq_wp -> johnnyq_wp). Without the ALTER the source
		// hash then never lands and the migrated app authenticates with a fresh
		// random password instead of its original one (GH #633). ALTER forces
		// the hash on whether the user was just created or pre-existed; it is a
		// harmless re-set in the freshly-created case.
		sql = fmt.Sprintf(
			"CREATE USER IF NOT EXISTS %[1]s@'localhost' IDENTIFIED BY PASSWORD '%[2]s'; "+
				"ALTER USER %[1]s@'localhost' IDENTIFIED BY PASSWORD '%[2]s'",
			escapedUsername,
			p.PasswordHash,
		)
	} else {
		escapedPassword, perr := EscapeMariaDBLiteral(p.Password)
		if perr != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: "invalid password",
			}
		}
		sql = fmt.Sprintf(
			"CREATE USER IF NOT EXISTS %s@'localhost' IDENTIFIED BY %s",
			escapedUsername,
			escapedPassword,
		)
	}

	cmd := execCommandContext(ctx, "mysql", "-e", sql)
	if err := cmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "failed to create user",
		}
	}

	return dbUserCreateResponse{OK: true}, nil
}

func init() {
	Default.Register("db_user.create", dbUserCreateHandler)
}
