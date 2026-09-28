// Package mailaddr canonicalises and validates email addresses used by the
// M6 mail subsystem.
//
// Shared by panel-api (handler-layer input validation) and panel-agent
// (cross-boundary guard before invoking Stalwart JMAP). The canonical form
// is the invariant Stalwart's SQL directory filter queries against
// (mailboxes.email_cached = CONCAT(local_part, '@', domains.name) in
// migration 000054, ADR-0042).
//
// v1 rules:
//   - lowercase both parts
//   - strip `+tag` subaddress from local part (RFC 5233 plus-addressing)
//   - reject non-ASCII local parts (UTF-8 SMTPUTF8 deferred)
//   - punycode-encode domains (IDNA 2008 via golang.org/x/net/idna)
//   - reject shell metacharacters in either part (defence in depth for the
//     agent's exec path even though Stalwart reads SQL, not argv)
//   - local <= 64 octets, domain <= 253 octets (RFC 5321 §4.5.3.1)
package mailaddr

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/net/idna"
)

// Sentinel errors. Callers use errors.Is to distinguish categories for
// HTTP status mapping (all of these are 400 Bad Request, but logs should
// tell the operator which rule fired).
var (
	ErrEmpty           = errors.New("mailaddr: empty address")
	ErrNoAtSign        = errors.New("mailaddr: missing '@'")
	ErrMultipleAtSigns = errors.New("mailaddr: multiple '@' signs")
	ErrLocalEmpty      = errors.New("mailaddr: empty local part")
	ErrLocalTooLong    = errors.New("mailaddr: local part exceeds 64 octets")
	ErrLocalNonASCII   = errors.New("mailaddr: non-ASCII local part (SMTPUTF8 not supported in v1)")
	ErrLocalShellMeta  = errors.New("mailaddr: shell metacharacter in local part")
	ErrDomainEmpty     = errors.New("mailaddr: empty domain part")
	ErrDomainTooLong   = errors.New("mailaddr: domain exceeds 253 octets")
	ErrDomainShellMeta = errors.New("mailaddr: shell metacharacter in domain")
	ErrIDNA            = errors.New("mailaddr: invalid IDN domain")
)

// Max octet lengths per RFC 5321 §4.5.3.1.
const (
	maxLocalOctets  = 64
	maxDomainOctets = 253
)

// Canonicalise returns the lowercased, subaddress-stripped local part and
// the lowercased, punycode-encoded domain. The caller composes
// CONCAT(local, '@', domain) for the email_cached column.
//
// Returns one of the sentinel errors above on any validation failure.
func Canonicalise(raw string) (local, domain string, err error) {
	if raw == "" {
		return "", "", ErrEmpty
	}

	// Split on '@'. strings.Cut is exact-once; reject multi-@ explicitly
	// so we don't silently accept alice@x@example.com.
	at := strings.IndexByte(raw, '@')
	if at < 0 {
		return "", "", ErrNoAtSign
	}
	if strings.IndexByte(raw[at+1:], '@') >= 0 {
		return "", "", ErrMultipleAtSigns
	}

	localRaw := raw[:at]
	domainRaw := raw[at+1:]

	local, err = canonLocal(localRaw)
	if err != nil {
		return "", "", err
	}
	domain, err = canonDomain(domainRaw)
	if err != nil {
		return "", "", err
	}

	return local, domain, nil
}

// canonLocal handles the local half: strip +tag, lowercase, validate.
func canonLocal(raw string) (string, error) {
	// Strip +tag subaddress (RFC 5233). Everything from the first '+' to
	// end-of-string is the tag. Strip before length + charset checks so a
	// user with 40-char local + 30-char tag isn't rejected for length.
	if plus := strings.IndexByte(raw, '+'); plus >= 0 {
		raw = raw[:plus]
	}

	if raw == "" {
		return "", ErrLocalEmpty
	}
	if len(raw) > maxLocalOctets {
		return "", ErrLocalTooLong
	}

	// v1 restriction: ASCII only. SMTPUTF8 is a separate RFC 6531 problem
	// (Stalwart supports it; the panel doesn't expose it yet).
	for i := 0; i < len(raw); i++ {
		if raw[i] > 0x7f {
			return "", ErrLocalNonASCII
		}
	}

	// Charset allowlist. RFC 5321 permits many special chars in quoted
	// local parts; shared-hosting mailboxes don't need them and each one
	// is a shell-escape footgun in the agent's exec path. Stick to
	// letters/digits/dot/underscore/hyphen. `+` already stripped above.
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-':
		default:
			return "", ErrLocalShellMeta
		}
	}

	// Defence in depth: even if the charset allowlist above already
	// excludes shell metachars, re-check with an explicit deny list so a
	// future widening of the allowlist doesn't silently re-admit them.
	if strings.ContainsAny(raw, shellMetaChars) {
		return "", ErrLocalShellMeta
	}

	return strings.ToLower(raw), nil
}

// canonDomain handles the domain half: IDN via punycode, lowercase, validate.
func canonDomain(raw string) (string, error) {
	if raw == "" {
		return "", ErrDomainEmpty
	}
	if len(raw) > maxDomainOctets {
		return "", ErrDomainTooLong
	}

	// Reject shell metachars BEFORE IDN so idna.ToASCII doesn't have a
	// chance to mistake one for a label separator in some edge case.
	if strings.ContainsAny(raw, shellMetaChars) {
		return "", ErrDomainShellMeta
	}

	// idna.Lookup is strict (IDNA 2008, no deviation mappings, no
	// transitional processing). Rejects trailing dots, empty labels,
	// over-long labels (63 octets), disallowed code points.
	ascii, err := idna.Lookup.ToASCII(raw)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrIDNA, err)
	}

	// After IDN encode, re-check length (punycode can grow the string).
	if len(ascii) > maxDomainOctets {
		return "", ErrDomainTooLong
	}

	// Sanity: IDN output must be pure ASCII lowercase.
	lower := strings.ToLower(ascii)

	// Last-mile metachar re-check on the IDN-encoded form (punycode uses
	// `-` which is allowed; belt and braces).
	if strings.ContainsAny(lower, shellMetaChars) {
		return "", ErrDomainShellMeta
	}

	return lower, nil
}

// DirectoryLocalPart is the local part of the Stalwart principal that hosts a
// domain's directory address book (GH #1637, ADR-0171). The panel creates
// that principal itself, so no mailbox, mail group or shared resource may
// take the address: its principal would collide with the host.
const DirectoryLocalPart = "jabali-directory"

// ErrLocalReserved is returned for a local part the panel keeps for itself.
var ErrLocalReserved = errors.New("mailaddr: local part is reserved")

// CheckNotReserved returns ErrLocalReserved when canonLocal, a local part
// already canonicalised by Canonicalise, is one the panel keeps for itself.
// The doors that create an address in a domain call it.
func CheckNotReserved(canonLocal string) error {
	if canonLocal == DirectoryLocalPart {
		return ErrLocalReserved
	}
	return nil
}

// shellMetaChars is the deny list referenced by both canonLocal (belt +
// braces beside the charset allowlist) and canonDomain.
//
// Includes whitespace + every character with special meaning in bourne
// sh, POSIX sh, bash, and systemd ExecStart argv splitting. '@' and '+'
// are not on the list because they are legal email syntax handled before
// the metachar check.
const shellMetaChars = " \t\n\r;&|<>`$\\(){}'\"!*?[]"
