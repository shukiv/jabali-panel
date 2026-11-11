package migrate

import (
	"errors"
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

// hostKeyAlgorithmGroups splits HostKeyAlgorithms by key type, in the same
// order. A source has at most one host key per type; asking for one group per
// handshake shows each of them in turn.
var hostKeyAlgorithmGroups = [][]string{
	{ssh.KeyAlgoED25519},
	{ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521},
	{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA},
	{ssh.InsecureKeyAlgoDSA},
}

// DialError is a TCP dial failure from SSHHandshake, kept apart from
// handshake failures so callers can word them differently.
type DialError struct{ Err error }

func (e *DialError) Error() string { return e.Err.Error() }
func (e *DialError) Unwrap() error { return e.Err }

// SSHHandshake opens a connection with dial and runs the SSH client handshake
// with cfg against addr.
//
// With a pin (expectedFP), it asks for one host-key type at a time —
// ed25519, then ECDSA, then RSA, then DSA — and dials again with the next
// type when the source's key of that type does not match the pin or the
// source has none. Only a key that matches the pin is ever accepted, and
// credentials are sent only after it is. So a pin taken from any of the
// source's keys works (pins the wizard recorded before ed25519 came first are
// ECDSA). When no key matches, the error lists each key the source presented.
//
// Without a pin it makes one handshake with cfg as given (trust on first use,
// as PinningHostKeyCallback documents).
func SSHHandshake(dial func() (net.Conn, error), addr string, cfg *ssh.ClientConfig, expectedFP string) (ssh.Conn, <-chan ssh.NewChannel, <-chan *ssh.Request, error) {
	expected := normalizeFingerprint(expectedFP)
	if expected == "" {
		return sshHandshakeOnce(dial, addr, cfg)
	}
	var presented []string
	var lastErr error
	for _, group := range hostKeyAlgorithmGroups {
		c := *cfg
		c.HostKeyAlgorithms = append([]string(nil), group...)
		var seen ssh.PublicKey
		pin := PinningHostKeyCallback(expectedFP)
		c.HostKeyCallback = func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			seen = key
			return pin(hostname, remote, key)
		}
		sc, chans, reqs, err := sshHandshakeOnce(dial, addr, &c)
		if err == nil {
			return sc, chans, reqs, nil
		}
		var de *DialError
		if errors.As(err, &de) {
			return nil, nil, nil, err
		}
		switch {
		case seen == nil:
			// No key of this type on the source (or the handshake broke
			// before one was shown): try the next type.
			lastErr = err
		case normalizeFingerprint(ssh.FingerprintSHA256(seen)) != expected:
			presented = append(presented, seen.Type()+" "+ssh.FingerprintSHA256(seen))
		default:
			// The key matched; a later step (authentication) failed.
			return nil, nil, nil, err
		}
	}
	if len(presented) == 0 {
		return nil, nil, nil, lastErr
	}
	return nil, nil, nil, fmt.Errorf("host key fingerprint mismatch for %s: none of the source's host keys matches the pin %s; it presented %s",
		addr, expectedFP, strings.Join(presented, ", "))
}

func sshHandshakeOnce(dial func() (net.Conn, error), addr string, cfg *ssh.ClientConfig) (ssh.Conn, <-chan ssh.NewChannel, <-chan *ssh.Request, error) {
	conn, err := dial()
	if err != nil {
		return nil, nil, nil, &DialError{Err: err}
	}
	sc, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return nil, nil, nil, err
	}
	return sc, chans, reqs, nil
}

// normalizeFingerprint canonicalizes a SHA256 fingerprint for comparison:
// trims, strips an optional "SHA256:" prefix, and drops trailing base64 "=".
func normalizeFingerprint(fp string) string {
	s := strings.TrimSpace(fp)
	s = strings.TrimPrefix(s, "SHA256:")
	s = strings.TrimRight(s, "=")
	return s
}
