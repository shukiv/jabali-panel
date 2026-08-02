package releasesig

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/crypto/blake2b"
)

// testKeypair mints a minisign-shaped keypair and a signer. Signing lives only
// in the test: the production code must never be able to produce a valid
// signature, or the offline-key property is fiction.
type testKeypair struct {
	pubLine string
	priv    ed25519.PrivateKey
	keyID   [8]byte
}

func newTestKeypair(t *testing.T) testKeypair {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		t.Fatalf("key id: %v", err)
	}
	raw := append(append([]byte(algPure), id[:]...), pub...)
	return testKeypair{pubLine: base64.StdEncoding.EncodeToString(raw), priv: priv, keyID: id}
}

// sign produces a .minisig exactly as the minisign CLI would.
func (k testKeypair) sign(artifact []byte, alg, trustedComment string) string {
	signed := artifact
	if alg == algPrehashed {
		h := blake2b.Sum512(artifact)
		signed = h[:]
	}
	sig := ed25519.Sign(k.priv, signed)
	sigLine := base64.StdEncoding.EncodeToString(append(append([]byte(alg), k.keyID[:]...), sig...))
	global := ed25519.Sign(k.priv, append(append([]byte{}, sig...), []byte(trustedComment)...))
	return fmt.Sprintf("untrusted comment: signature from jabali release key\n%s\ntrusted comment: %s\n%s\n",
		sigLine, trustedComment, base64.StdEncoding.EncodeToString(global))
}

func TestVerifyAcceptsGenuineSignature(t *testing.T) {
	t.Parallel()
	k := newTestKeypair(t)
	pk, err := ParsePublicKey(k.pubLine)
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}
	artifact := []byte("a release tarball's bytes")

	// minisign defaults to prehashed these days but still emits legacy pure
	// signatures with -S on older versions; both must verify.
	for _, alg := range []string{algPure, algPrehashed} {
		if err := pk.Verify(artifact, k.sign(artifact, alg, "timestamp:1 file:jabali-release.tar.gz")); err != nil {
			t.Errorf("alg %s: genuine signature rejected: %v", alg, err)
		}
	}
}

// The whole point: a tampered artifact must fail even though the signature file
// is perfectly well-formed.
func TestVerifyRejectsTamperedArtifact(t *testing.T) {
	t.Parallel()
	k := newTestKeypair(t)
	pk, _ := ParsePublicKey(k.pubLine)
	sig := k.sign([]byte("genuine tarball"), algPrehashed, "timestamp:1")

	if err := pk.Verify([]byte("trojaned tarball"), sig); !errors.Is(err, ErrBadSignature) {
		t.Errorf("tampered artifact must fail with ErrBadSignature, got %v", err)
	}
}

// An attacker who can publish releases can also publish a signature — made with
// THEIR key. Only the embedded key may satisfy the check.
func TestVerifyRejectsSignatureFromAnotherKey(t *testing.T) {
	t.Parallel()
	trusted := newTestKeypair(t)
	attacker := newTestKeypair(t)
	pk, _ := ParsePublicKey(trusted.pubLine)
	artifact := []byte("trojaned tarball")

	err := pk.Verify(artifact, attacker.sign(artifact, algPrehashed, "timestamp:1"))
	if !errors.Is(err, ErrKeyMismatch) {
		t.Errorf("a signature from an untrusted key must be refused, got %v", err)
	}
}

// Same key ID as ours but a different private key — the key-ID check alone must
// not be what carries the security.
func TestVerifyRejectsForgedKeyID(t *testing.T) {
	t.Parallel()
	trusted := newTestKeypair(t)
	forger := newTestKeypair(t)
	forger.keyID = trusted.keyID // pretend to be us
	pk, _ := ParsePublicKey(trusted.pubLine)
	artifact := []byte("trojaned tarball")

	if err := pk.Verify(artifact, forger.sign(artifact, algPrehashed, "timestamp:1")); !errors.Is(err, ErrBadSignature) {
		t.Errorf("matching key ID with a different key must still fail, got %v", err)
	}
}

// The trusted comment is what an operator reads when inspecting a release, so
// it has to be covered by the global signature rather than free-form text.
func TestVerifyRejectsRewrittenTrustedComment(t *testing.T) {
	t.Parallel()
	k := newTestKeypair(t)
	pk, _ := ParsePublicKey(k.pubLine)
	artifact := []byte("tarball")
	sig := k.sign(artifact, algPrehashed, "timestamp:1 file:jabali-release.tar.gz")

	tampered := strings.Replace(sig, "file:jabali-release.tar.gz", "file:something-else.tar.gz", 1)
	if tampered == sig {
		t.Fatal("test did not modify the comment")
	}
	if err := pk.Verify(artifact, tampered); !errors.Is(err, ErrBadSignature) {
		t.Errorf("a rewritten trusted comment must fail, got %v", err)
	}
}

func TestVerifyRejectsMalformedInput(t *testing.T) {
	t.Parallel()
	k := newTestKeypair(t)
	pk, _ := ParsePublicKey(k.pubLine)

	for name, sig := range map[string]string{
		"empty":            "",
		"too few lines":    "untrusted comment: x\nAAAA\n",
		"not base64":       "untrusted comment: x\n!!!!\ntrusted comment: t\nAAAA\n",
		"missing trusted":  "untrusted comment: x\nAAAA\nnope\nAAAA\n",
		"truncated sig":    "untrusted comment: x\n" + base64.StdEncoding.EncodeToString([]byte("Ed")) + "\ntrusted comment: t\nAAAA\n",
		"empty sig line":   "untrusted comment: x\n\ntrusted comment: t\nAAAA\n",
		"whitespace only ": "   \n \n \n \n",
	} {
		if err := pk.Verify([]byte("tarball"), sig); err == nil {
			t.Errorf("%s: malformed signature accepted", name)
		}
	}
}

func TestParsePublicKeyRejectsGarbage(t *testing.T) {
	t.Parallel()
	for name, in := range map[string]string{
		"empty":            "",
		"comment only":     "untrusted comment: jabali\n",
		"not base64":       "!!!!not-base64!!!!",
		"wrong length":     base64.StdEncoding.EncodeToString([]byte("Edshort")),
		"wrong algorithm":  base64.StdEncoding.EncodeToString(append(append([]byte("XX"), make([]byte, 8)...), make([]byte, 32)...)),
		"key from nowhere": "AAAA",
	} {
		if _, err := ParsePublicKey(in); err == nil {
			t.Errorf("%s: garbage accepted as a public key", name)
		}
	}
}

// The embedded form is the bare base64 line; the on-disk form has a comment
// header. Both must parse to the same key.
func TestParsePublicKeyAcceptsBothForms(t *testing.T) {
	t.Parallel()
	k := newTestKeypair(t)
	bare, err := ParsePublicKey(k.pubLine)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}
	full, err := ParsePublicKey("untrusted comment: minisign public key ABC\n" + k.pubLine + "\n")
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	if !bare.key.Equal(full.key) || bare.keyID != full.keyID {
		t.Error("the .pub file and the embedded line must parse to the same key")
	}
}
