package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// ssoTokenHashPrefix returns the audit-log prefix for an SSO handoff token.
//
// One definition, used by BOTH the mint and validate sides, because they
// disagreed: mint hashed the base64url STRING and logged 8 bytes, validate
// hashed the DECODED bytes and logged 4. Two different digests at two
// different lengths, so an "issued" line and its matching "validated" or
// "unauthorized" line could never be correlated — the SSO audit chain was
// silently broken precisely when you need it, during an incident.
//
// The token is base64url; a value that fails to decode is hashed as-is rather
// than dropped, so a malformed token still produces a stable, greppable
// prefix instead of an empty field.
func ssoTokenHashPrefix(token string) string {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		raw = []byte(token)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:4])
}
