package dbconsoleops

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// TokenAuditPrefix returns the audit-log prefix for a DB-console SSO handoff
// token. It is the single "log the token safely" primitive shared by every
// adapter's audit line, so an "issued" record and its matching
// "validated"/"unauthorized" record share a value to grep for — and so no
// adapter ever logs raw token material (JAB-348 AC5).
//
// The token is base64url (base64.RawURLEncoding, as sso.Service.MintToken
// emits). The prefix is the first 4 bytes of SHA-256 over the DECODED token
// bytes, hex-encoded — never the token itself. Both the mint side and the
// validate side must derive it the same way or the audit chain cannot be
// followed; a value that fails to decode is hashed as-is rather than dropped,
// so a malformed token still yields a stable, greppable 8-hex-char prefix
// instead of an empty audit field.
func TokenAuditPrefix(token string) string {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		raw = []byte(token)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:4])
}
