package commands

import (
	"fmt"
	"strings"
)

// EscapeMariaDBIdentifier wraps a database or user name in backticks and
// doubles any backtick inside, matching MariaDB's quoted-identifier
// rules. NUL bytes and unprintable characters are rejected by the
// caller (we validate upstream); this function exists to neutralise
// backticks and nothing else.
func EscapeMariaDBIdentifier(s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("empty identifier")
	}
	for _, r := range s {
		if r == 0 {
			return "", fmt.Errorf("NUL byte in identifier")
		}
	}
	return "`" + strings.ReplaceAll(s, "`", "``") + "`", nil
}

// EscapeMariaDBLiteral wraps a string literal in single quotes, doubles
// any single-quote, and rejects NULs. Use for IDENTIFIED BY passwords,
// charset names, etc. Never use for identifiers — use
// EscapeMariaDBIdentifier instead.
func EscapeMariaDBLiteral(s string) (string, error) {
	for _, r := range s {
		if r == 0 {
			return "", fmt.Errorf("NUL byte in literal")
		}
	}
	// MariaDB also honours backslash-escapes by default (NO_BACKSLASH_ESCAPES
	// sql_mode disables this). We escape backslashes too so the function
	// is safe regardless of the session's sql_mode.
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `''`)
	return "'" + s + "'", nil
}

// EscapeMariaDBGrantDB quotes a database name for a database-level GRANT or
// REVOKE (`ON <here>.*`). In that position MariaDB reads `_` and `%` as
// wildcards: an unescaped alice_shop also matches aliceXshop, which can be
// another tenant's database. The escaped form matches the one name only.
func EscapeMariaDBGrantDB(db string) (string, error) {
	return EscapeMariaDBIdentifier(mariaDBGrantPattern(db))
}

// isNoSuchGrant reports whether a mysql client error is MariaDB 1141 ("There
// is no such grant defined"): the revoked grant is not held.
func isNoSuchGrant(out string) bool {
	se := strings.ToLower(out)
	return strings.Contains(se, "there is no such grant") || strings.Contains(se, "nonexistent grant")
}
