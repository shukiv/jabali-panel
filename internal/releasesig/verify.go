// Package releasesig verifies minisign signatures on release artifacts.
//
// WHY THIS EXISTS (JAB-190). Both install paths verified the release tarball
// against a SHA-256 sidecar fetched from the SAME origin as the tarball. That
// detects corruption and truncation, not a malicious publisher: anyone able to
// publish a GitHub release can upload a matching .sha256 next to a trojaned
// tarball and every box accepts it. The tarball carries bin/jabali-agent, so a
// trojaned release is root on every host that installs or updates, with no
// further exploitation needed.
//
// A signature moves the trust root off the artifact host. The private key lives
// offline — never in CI — so compromising the Actions pipeline or the release
// storage is no longer enough to ship code that boxes will run.
//
// WHY MINISIGN RATHER THAN A BESPOKE FORMAT: the operator signs with the
// standard, audited minisign CLI, which keeps the private key password-
// protected and gives a well-specified format. Only the VERIFIER lives here,
// and it is deliberately small: parse, check the key ID, check two Ed25519
// signatures. Signing stays out of this codebase entirely, which is the point —
// nothing in the repo or in CI can produce a valid signature.
package releasesig

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// Errors callers may want to distinguish. A missing signature is deliberately
// NOT a distinct "skip me" case — see Verify.
var (
	ErrMalformedKey = errors.New("releasesig: malformed public key")
	ErrMalformedSig = errors.New("releasesig: malformed signature")
	ErrKeyMismatch  = errors.New("releasesig: signature was made by a different key")
	ErrBadSignature = errors.New("releasesig: signature does not match the file")
)

const (
	algPure      = "Ed" // signature over the file bytes
	algPrehashed = "ED" // signature over BLAKE2b-512 of the file bytes
)

// PublicKey is a parsed minisign public key.
type PublicKey struct {
	keyID [8]byte
	key   ed25519.PublicKey
}

// ParsePublicKey accepts either a full minisign .pub file (comment line +
// base64 line) or the bare base64 line on its own, which is the form embedded
// in the binary.
func ParsePublicKey(s string) (*PublicKey, error) {
	line := ""
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "untrusted comment:") {
			continue
		}
		line = l
		break
	}
	if line == "" {
		return nil, fmt.Errorf("%w: no base64 payload", ErrMalformedKey)
	}
	raw, err := base64.StdEncoding.DecodeString(line)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformedKey, err)
	}
	// 2-byte algorithm + 8-byte key ID + 32-byte Ed25519 key.
	if len(raw) != 2+8+ed25519.PublicKeySize {
		return nil, fmt.Errorf("%w: got %d bytes, want %d", ErrMalformedKey, len(raw), 2+8+ed25519.PublicKeySize)
	}
	if string(raw[:2]) != algPure {
		return nil, fmt.Errorf("%w: unsupported algorithm %q", ErrMalformedKey, raw[:2])
	}
	pk := &PublicKey{key: ed25519.PublicKey(raw[10:])}
	copy(pk.keyID[:], raw[2:10])
	return pk, nil
}

// Verify checks sigFile (the contents of a minisign .minisig) against the
// artifact bytes.
//
// There is no "signature absent, carry on" path here by design: the caller
// decides whether signatures are required, and when they are, a missing file is
// a hard failure at the call site. A verifier that silently passes when the
// signature is missing provides no defence at all, since the attacker
// substituting the artifact also controls whether a signature is present.
func (pk *PublicKey) Verify(artifact []byte, sigFile string) error {
	if pk == nil || len(pk.key) != ed25519.PublicKeySize {
		return ErrMalformedKey
	}
	sigLine, trustedComment, globalLine, err := parseSigFile(sigFile)
	if err != nil {
		return err
	}
	raw, err := base64.StdEncoding.DecodeString(sigLine)
	if err != nil {
		return fmt.Errorf("%w: signature line: %v", ErrMalformedSig, err)
	}
	if len(raw) != 2+8+ed25519.SignatureSize {
		return fmt.Errorf("%w: signature is %d bytes, want %d", ErrMalformedSig, len(raw), 2+8+ed25519.SignatureSize)
	}
	alg := string(raw[:2])
	if !bytes.Equal(raw[2:10], pk.keyID[:]) {
		// Names the situation rather than "bad signature": a key rotation that
		// forgot to re-sign looks identical to an attack otherwise.
		return fmt.Errorf("%w: signature key ID %x, trusted key ID %x", ErrKeyMismatch, raw[2:10], pk.keyID)
	}
	sig := raw[10:]

	var signed []byte
	switch alg {
	case algPure:
		signed = artifact
	case algPrehashed:
		h := blake2b.Sum512(artifact)
		signed = h[:]
	default:
		return fmt.Errorf("%w: unsupported algorithm %q", ErrMalformedSig, alg)
	}
	if !ed25519.Verify(pk.key, signed, sig) {
		return ErrBadSignature
	}

	// The trusted comment is covered by a second signature over
	// (signature || comment). Without checking it, the comment could be
	// rewritten at will — and it is the field operators actually read.
	global, err := base64.StdEncoding.DecodeString(globalLine)
	if err != nil {
		return fmt.Errorf("%w: global signature: %v", ErrMalformedSig, err)
	}
	if len(global) != ed25519.SignatureSize {
		return fmt.Errorf("%w: global signature is %d bytes, want %d", ErrMalformedSig, len(global), ed25519.SignatureSize)
	}
	if !ed25519.Verify(pk.key, append(append([]byte{}, sig...), []byte(trustedComment)...), global) {
		return fmt.Errorf("%w: trusted comment is not covered by the global signature", ErrBadSignature)
	}
	return nil
}

// parseSigFile pulls the four fields out of a .minisig:
//
//	untrusted comment: <text>
//	<base64 signature>
//	trusted comment: <text>
//	<base64 global signature>
func parseSigFile(s string) (sigLine, trustedComment, globalLine string, err error) {
	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(s), "\r\n", "\n"), "\n")
	if len(lines) < 4 {
		return "", "", "", fmt.Errorf("%w: want 4 lines, got %d", ErrMalformedSig, len(lines))
	}
	sigLine = strings.TrimSpace(lines[1])
	const marker = "trusted comment:"
	third := strings.TrimSpace(lines[2])
	if !strings.HasPrefix(third, marker) {
		return "", "", "", fmt.Errorf("%w: third line is not a trusted comment", ErrMalformedSig)
	}
	trustedComment = strings.TrimPrefix(third, marker)
	trustedComment = strings.TrimPrefix(trustedComment, " ")
	globalLine = strings.TrimSpace(lines[3])
	if sigLine == "" || globalLine == "" {
		return "", "", "", fmt.Errorf("%w: empty signature line", ErrMalformedSig)
	}
	return sigLine, trustedComment, globalLine, nil
}
