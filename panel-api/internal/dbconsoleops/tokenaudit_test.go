package dbconsoleops

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

// The whole point of the prefix is that an audit record can carry it WITHOUT
// carrying token material. Pin that property: the prefix is a short hex digest,
// never the token and never a substring of it.
func TestTokenAuditPrefix_NoTokenMaterial(t *testing.T) {
	// A representative base64url token (the alphabet MintToken emits).
	const token = "Zm9vYmFyLWJhei0xMjM0NTY3ODkw_AB-cd"

	got := TokenAuditPrefix(token)

	if len(got) != 8 { // 4 bytes, hex-encoded
		t.Fatalf("prefix %q: want 8 hex chars", got)
	}
	if _, err := hex.DecodeString(got); err != nil {
		t.Fatalf("prefix %q is not hex: %v", got, err)
	}
	if got == token {
		t.Fatal("prefix equals the token — token material leaked into the audit field")
	}
	if strings.Contains(token, got) {
		t.Fatalf("prefix %q appears inside the token — not a safe audit value", got)
	}
}

// A malformed (undecodable) token must still yield a stable, greppable prefix
// rather than an empty audit field or a panic.
func TestTokenAuditPrefix_UndecodableTokenIsStable(t *testing.T) {
	const bad = "!!!not-base64!!!"
	got := TokenAuditPrefix(bad)
	if len(got) != 8 {
		t.Fatalf("prefix %q: want 8 hex chars even for an undecodable token", got)
	}
	if got != TokenAuditPrefix(bad) {
		t.Error("prefix must be deterministic")
	}
}

// Distinct tokens get distinct prefixes (collision would break correlation),
// and re-encoding the same bytes yields the same prefix.
func TestTokenAuditPrefix_DeterministicPerToken(t *testing.T) {
	a := base64.RawURLEncoding.EncodeToString([]byte("token-alpha-000000000000000000000"))
	b := base64.RawURLEncoding.EncodeToString([]byte("token-beta-0000000000000000000000"))
	if TokenAuditPrefix(a) == TokenAuditPrefix(b) {
		t.Fatal("distinct tokens produced the same audit prefix")
	}
	if TokenAuditPrefix(a) != TokenAuditPrefix(a) {
		t.Fatal("same token produced different prefixes")
	}
}
