package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/releasesig"
)

// releaseSigningPublicKey is the minisign public key releases are signed with —
// the bare base64 line from the .pub file, WITHOUT the comment header.
//
// EMPTY means signature verification is not yet in force: the tarball is
// accepted on its sha256 alone, which is where this project stood before
// JAB-190. That is a compile-time state, not a runtime one — an attacker cannot
// reach it by omitting a file, which is the failure mode the issue specifically
// called out ("a signature check that silently skips when the signature is
// absent provides no defense"). Once this constant is populated, verification is
// unconditional and a MISSING signature is a hard failure.
//
// The matching private key is held offline and never enters CI. Nothing in this
// repository can produce a signature; see docs/adr/0162-release-signing.md for
// the key ceremony and the release procedure.
const releaseSigningPublicKey = ""

// releaseSignatureRequired reports whether this build enforces signatures.
func releaseSignatureRequired() bool {
	return strings.TrimSpace(releaseSigningPublicKey) != ""
}

// verifyReleaseSignature checks the release tarball against the embedded
// public key.
//
// Called AFTER the sha256 check and BEFORE the tarball is extracted or any
// binary is installed. The sha256 sidecar comes from the same origin as the
// tarball, so it proves the download was not corrupted in transit and nothing
// more; whoever can publish the tarball can publish a matching checksum. The
// signature is what makes a trojaned release fail, because the signing key is
// not on the machine that publishes it.
//
// The order matters: verify before extract. Extracting first would run tar over
// attacker-controlled bytes and write them to disk before we had decided
// whether to trust them.
func verifyReleaseSignature(ctx context.Context, tarPath, sigURL string, log func(string, ...any)) error {
	return verifyReleaseSignatureWith(ctx, releaseSigningPublicKey, tarPath, sigURL, log)
}

// verifyReleaseSignatureWith is the body of verifyReleaseSignature with the
// trusted key passed in, so tests can exercise enforcement without a build-tag
// dance. The production path always supplies the embedded constant.
func verifyReleaseSignatureWith(ctx context.Context, pubKey, tarPath, sigURL string, log func(string, ...any)) error {
	if strings.TrimSpace(pubKey) == "" {
		return nil
	}
	pk, err := releasesig.ParsePublicKey(pubKey)
	if err != nil {
		// A build that claims to enforce signatures but cannot parse its own
		// key must refuse to install anything, not fall through to unverified.
		return fmt.Errorf("embedded release signing key is unusable: %w", err)
	}

	sigPath := tarPath + ".minisig"
	dctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := downloadTo(dctx, sigURL, sigPath, 60*time.Second); err != nil {
		return fmt.Errorf("this build requires signed releases and the signature could not be fetched from %s: %w\n"+
			"  Refusing to install an unverified tarball. If the release is genuinely unsigned it must be "+
			"re-published with its .minisig, not installed as-is", sigURL, err)
	}
	sigBody, err := os.ReadFile(sigPath)
	if err != nil {
		return fmt.Errorf("read signature: %w", err)
	}
	artifact, err := os.ReadFile(tarPath)
	if err != nil {
		return fmt.Errorf("read tarball: %w", err)
	}
	if err := pk.Verify(artifact, string(sigBody)); err != nil {
		switch {
		case errors.Is(err, releasesig.ErrKeyMismatch):
			return fmt.Errorf("release signature was made by a DIFFERENT key than this build trusts: %w\n"+
				"  Either the signing key was rotated (update the panel from a source build first) or this "+
				"tarball did not come from the jabali release process", err)
		case errors.Is(err, releasesig.ErrBadSignature):
			return fmt.Errorf("release signature does not match the tarball: %w\n"+
				"  The tarball's sha256 matched its sidecar, so this is not corruption — the artifact and "+
				"its checksum were both replaced. Do not install this release", err)
		default:
			return fmt.Errorf("release signature verification failed: %w", err)
		}
	}
	log("release: signature verified against the embedded release key")
	return nil
}

// releaseSignatureURL is the .minisig companion for a tarball URL. minisign
// appends its own suffix rather than replacing the extension.
func releaseSignatureURL(tarURL string) string { return tarURL + ".minisig" }
