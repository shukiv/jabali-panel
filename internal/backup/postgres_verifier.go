package backup

import "regexp"

// postgresSCRAMVerifierRe is a PostgreSQL SCRAM-SHA-256 password verifier as
// pg_authid stores it: SCRAM-SHA-256$<iterations>:<salt>$<StoredKey>:<ServerKey>,
// each part base64.
var postgresSCRAMVerifierRe = regexp.MustCompile(`^SCRAM-SHA-256\$[0-9]{1,7}:[A-Za-z0-9+/]+={0,2}\$[A-Za-z0-9+/]+={0,2}:[A-Za-z0-9+/]+={0,2}$`)

// IsPostgresSCRAMVerifier reports whether s is a SCRAM-SHA-256 verifier a
// restore may recreate a role with. An md5 verifier is salted with the role's
// name, so it wouldn't work for a role a restore renames; it doesn't qualify.
// The character set also keeps s safe inside a quoted psql variable.
func IsPostgresSCRAMVerifier(s string) bool {
	return len(s) <= 512 && postgresSCRAMVerifierRe.MatchString(s)
}
