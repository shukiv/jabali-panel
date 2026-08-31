package imap

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"strings"
	"syscall"
)

// Classify maps a probe/connect error to a stable machine code and a SAFE,
// operator-facing hint (GH #1429). The hint is one of OUR fixed strings keyed on
// the failure kind — it never contains the remote server's banner or the raw
// error text — so it is safe to return to a client, while the caller logs the
// real error server-side for diagnosis.
//
// The previous behaviour returned a single "could not enumerate the remote
// account" for every non-auth failure and logged nothing, so an operator whose
// probe failed (wrong port, STARTTLS mismatch, DNS typo, private host blocked by
// the SSRF guard, timeout, bad cert) had no way to tell which.
func Classify(err error) (code, hint string) {
	if err == nil {
		return "", ""
	}
	if errors.Is(err, ErrAuth) {
		return "auth_failed", "The remote server rejected the credentials. Check the username (usually the full email address) and the password — many providers require an app-specific password rather than the account password."
	}

	// DNS resolution failure. This MUST be checked before the "ssrf:" string
	// match below: the SSRF guard resolves the hostname itself and wraps a
	// lookup failure as `ssrf: resolve <host>: ...`, but the %w chain preserves
	// the *net.DNSError — so a hostname typo is a DNS problem, not a blocked
	// private host, and telling the operator to "enable Allow private hosts"
	// would be actively wrong.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns", "Could not resolve the mail host name. Check the hostname for typos."
	}

	// SSRF guard: migrate.DialTCP wraps private/loopback/rebinding rejections
	// with an "ssrf:" prefix. This is a very common cause when migrating from a
	// server on the local network with "Allow private hosts" left off.
	if strings.Contains(err.Error(), "ssrf:") {
		return "blocked_host", "The mail host resolved to a private or disallowed network address. If the server is on your internal network, enable “Allow private/LAN host” in the advanced options."
	}

	// Timeout — context deadline or a net timeout.
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return "timeout", "The connection to the mail host timed out. Check the host and port, and that the server is reachable and not blocked by a firewall."
	}

	// Connection refused / reset — usually the wrong port or TLS mode.
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) {
		return "refused", "The mail host refused the connection on that port. Check the port and whether the server uses implicit TLS (993) or STARTTLS (143)."
	}

	// TLS / certificate — often implicit-TLS vs STARTTLS confusion.
	var certErr x509.CertificateInvalidError
	var caErr x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	if errors.As(err, &certErr) || errors.As(err, &caErr) || errors.As(err, &hostErr) ||
		strings.Contains(err.Error(), "tls handshake") ||
		strings.Contains(err.Error(), "starttls") ||
		strings.Contains(err.Error(), "certificate") {
		return "tls", "The secure connection could not be established. Check whether the server uses implicit TLS (port 993) or STARTTLS (port 143), and that its certificate is valid."
	}

	return "connect_failed", "Could not connect to the mail host. Check the host, port, and whether the server uses implicit TLS (993) or STARTTLS (143)."
}
