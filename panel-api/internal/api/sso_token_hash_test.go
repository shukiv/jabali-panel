package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"
)

// The mint side and the validate side must derive the SAME audit prefix for
// the same token, or the SSO audit chain cannot be followed: an "issued" line
// and its matching "validated"/"unauthorized" line would never share a value
// to grep for.
//
// They disagreed in two ways at once — mint hashed the base64url STRING and
// logged 8 bytes, validate hashed the DECODED bytes and logged 4 — so this
// pins both the input and the length.
func TestSSOTokenHashPrefixMatchesValidateSide(t *testing.T) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	// Exactly what the validate handlers compute.
	sum := sha256.Sum256(raw)
	wantValidateSide := hex.EncodeToString(sum[:4])

	if got := ssoTokenHashPrefix(token); got != wantValidateSide {
		t.Fatalf("mint prefix %q != validate prefix %q — the two halves of the "+
			"SSO audit trail cannot be correlated", got, wantValidateSide)
	}

	// The old mint behaviour (hash the base64 string, take 8 bytes) must NOT
	// be what we produce.
	strSum := sha256.Sum256([]byte(token))
	if got := ssoTokenHashPrefix(token); got == hex.EncodeToString(strSum[:8]) {
		t.Error("prefix still derived from the base64 string at 8 bytes (the old bug)")
	}
}

// A malformed token still yields a stable, greppable prefix rather than an
// empty audit field.
func TestSSOTokenHashPrefixHandlesUndecodableToken(t *testing.T) {
	const bad = "!!!not-base64!!!"
	got := ssoTokenHashPrefix(bad)
	if len(got) != 8 { // 4 bytes hex-encoded
		t.Fatalf("prefix %q: want 8 hex chars even for an undecodable token", got)
	}
	if got != ssoTokenHashPrefix(bad) {
		t.Error("prefix must be deterministic")
	}
}
