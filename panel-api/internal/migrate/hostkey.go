package migrate

import (
	"fmt"
	"net"
	"strings"

	"golang.org/x/crypto/ssh"
)

// SSH host-key verification for migration connectors (Gitea #461, ADR-0151).
//
// The connectors previously used ssh.InsecureIgnoreHostKey(), so a network
// MITM able to intercept routing to the source host could impersonate it and
// harvest the supplied SSH password / key-auth attempt, or feed a malicious
// restore.
//
// This callback runs inside panel-api (the non-root `jabali` user, which CANNOT
// write the root:jabali 0750 migration-secrets dir), so it does NOT persist a
// known_hosts file — verification is in-memory:
//
//   - With an admin-supplied SHA256 fingerprint (pasted out-of-band), the
//     presented key MUST match it; a mismatch aborts the handshake before any
//     credential is sent. This is the strong path and protects even the first
//     connect.
//   - Without a fingerprint, the connector accepts the key (trust-on-first-use
//     for the short discover connection). The high-value rsync transfer stage
//     runs in the root agent, which pins the key with StrictHostKeyChecking=
//     accept-new (and enforces the same fingerprint when supplied), so a key
//     flip during the long transfer is rejected there.

// PinningHostKeyCallback returns an ssh.HostKeyCallback. expectedFP, when set,
// is an admin-supplied SHA256 fingerprint ("SHA256:…" or the bare base64 body)
// the source key must match.
func PinningHostKeyCallback(expectedFP string) ssh.HostKeyCallback {
	expected := normalizeFingerprint(expectedFP)
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if expected == "" {
			return nil // TOFU: accept the discover connection.
		}
		got := normalizeFingerprint(ssh.FingerprintSHA256(key))
		if got != expected {
			return fmt.Errorf("host key fingerprint mismatch for %s: the source presented its %s key, %s; expected %s (the pin must be the fingerprint of the source's %s host key — the panel asks for ed25519 first)",
				hostname, key.Type(), ssh.FingerprintSHA256(key), expectedFP, key.Type())
		}
		return nil
	}
}

// HostKeyAlgorithms is the host-key algorithm preference for every migration
// SSH dial: ed25519, then ECDSA, then RSA with SHA-2, then the legacy ssh-rsa
// and ssh-dss that x/crypto's default also offers, so an old source that
// connects today still does. No certificate algorithms: the pin is a plain
// key's fingerprint, and a host certificate's never matches it.
//
// x/crypto's default lists ECDSA and RSA before ED25519, so a source with
// both keys presented its ECDSA key while `ssh-keyscan -t ed25519` (what the
// wizard asks the operator to pin) gave the ed25519 one, and the pin never
// matched. A fresh slice each call: ssh.ClientConfig keeps the reference.
func HostKeyAlgorithms() []string {
	return []string{
		ssh.KeyAlgoED25519,
		ssh.KeyAlgoECDSA256,
		ssh.KeyAlgoECDSA384,
		ssh.KeyAlgoECDSA521,
		ssh.KeyAlgoRSASHA512,
		ssh.KeyAlgoRSASHA256,
		ssh.KeyAlgoRSA,
		ssh.InsecureKeyAlgoDSA,
	}
}

// normalizeFingerprint canonicalizes a SHA256 fingerprint for comparison:
// trims, strips an optional "SHA256:" prefix, and drops trailing base64 "=".
func normalizeFingerprint(fp string) string {
	s := strings.TrimSpace(fp)
	s = strings.TrimPrefix(s, "SHA256:")
	s = strings.TrimRight(s, "=")
	return s
}
