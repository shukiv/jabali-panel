package commands

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"regexp"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// dbMysqladminEnsureParams is the input shape for db.mysqladmin.ensure.
type dbMysqladminEnsureParams struct {
	PanelUsername string `json:"panel_username"`
}

// dbMysqladminEnsureResponse is the output shape for db.mysqladmin.ensure.
// The password is plaintext and MUST be encrypted by panel-api immediately.
type dbMysqladminEnsureResponse struct {
	Username string `json:"mysqladmin_username"`
	Password string `json:"mysqladmin_password"`
}

// panelUsernameRegex validates panel username format. Must start with a
// lowercase letter; letters, digits, and underscores only; 1–32 chars so
// the "_mysqladmin" suffix stays under MariaDB's 80-char user-name limit.
var panelUsernameRegex = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// passwordCharset is the character set used for mysqladmin passwords.
// 64 chars keeps `byte % 64` bias-free (256 is an exact multiple of 64).
// No shell-special, no quote, no backslash — safe inside a single-quoted
// SQL literal after EscapeMariaDBLiteral (which is still applied for
// defense in depth).
const mysqladminPasswordCharset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-"

// generateMysqladminPassword returns a 32-char password drawn uniformly
// from mysqladminPasswordCharset.
func generateMysqladminPassword() (string, error) {
	const n = 32
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	for i := range buf {
		buf[i] = mysqladminPasswordCharset[buf[i]%byte(len(mysqladminPasswordCharset))]
	}
	return string(buf), nil
}

func dbMysqladminEnsureHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbMysqladminEnsureParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// Primary validation: regex. Anything that passes is a safe identifier.
	if !panelUsernameRegex.MatchString(p.PanelUsername) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid panel username",
		}
	}

	// Belt-and-suspenders: the regex already excludes these, but a buggy
	// regex change elsewhere shouldn't silently open this up.
	forbidden := []string{"..", `\`, `'`, `"`, ` `, "/", ".", ";", "\n", "\r"}
	for _, f := range forbidden {
		for i := 0; i+len(f) <= len(p.PanelUsername); i++ {
			if p.PanelUsername[i:i+len(f)] == f {
				return nil, &agentwire.AgentError{
					Code:    agentwire.CodeInvalidArgument,
					Message: "invalid panel username",
				}
			}
		}
	}

	password, err := generateMysqladminPassword()
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "failed to generate password",
		}
	}

	shadowUser := p.PanelUsername + "_mysqladmin"

	// Escape the user name and password for use as single-quoted SQL
	// literals. The DB pattern does NOT get EscapeMariaDBLiteral: MariaDB
	// parses GRANT's schema pattern with LIKE-wildcard semantics, and we
	// need an unquoted identifier with a literal underscore (\_) so that
	// `alice_wp` matches but `aliceNwp` does not.
	escapedUser, err := EscapeMariaDBLiteral(shadowUser)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid shadow username",
		}
	}
	escapedPassword, err := EscapeMariaDBLiteral(password)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "invalid generated password",
		}
	}

	// Database pattern: backtick-wrapped so MariaDB parses it as a pattern
	// identifier; `\_` escapes the underscore wildcard so only DBs whose
	// name starts with "<panel_username>_" match.
	dbPattern := fmt.Sprintf("`%s\\_%%`", p.PanelUsername)

	// Four idempotent statements. CREATE USER IF NOT EXISTS is a no-op
	// on pre-existing rows; ALTER USER always rotates the password.
	// Together they handle both the "first time" and "rotate on exist"
	// cases in the reconciler crash-recovery path (step 7 of the plan).
	sql := fmt.Sprintf(
		"CREATE USER IF NOT EXISTS %[1]s@'localhost' IDENTIFIED BY %[2]s; "+
			"ALTER USER %[1]s@'localhost' IDENTIFIED BY %[2]s; "+
			"GRANT ALL PRIVILEGES ON %[3]s.* TO %[1]s@'localhost'; "+
			"FLUSH PRIVILEGES;",
		escapedUser, escapedPassword, dbPattern,
	)

	cmd := execCommandContext(ctx, "mysql", "-e", sql)
	if err := cmd.Run(); err != nil {
		// Do not echo mysql's stderr — it may contain the password.
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: "failed to ensure mysqladmin shadow user",
		}
	}

	return dbMysqladminEnsureResponse{
		Username: shadowUser,
		Password: password,
	}, nil
}

func init() {
	Default.Register("db.mysqladmin.ensure", dbMysqladminEnsureHandler)
}
