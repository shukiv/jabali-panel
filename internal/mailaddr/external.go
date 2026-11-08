package mailaddr

import (
	"errors"
	"strings"
)

// ErrDomainSingleLabel rejects a destination domain with no dot ("localhost").
var ErrDomainSingleLabel = errors.New("mailaddr: domain needs at least two labels")

// ErrLocalDots rejects a local part that starts or ends with a dot or holds
// two dots in a row (RFC 5322 dot-atom).
var ErrLocalDots = errors.New("mailaddr: misplaced dot in local part")

// CanonicaliseExternal validates an address on another mail server that the
// panel delivers to (a distribution list's outside member, GH #1833) and
// returns it lower-cased with a punycode domain.
//
// Unlike Canonicalise it keeps a +tag: someone@gmail.com and
// someone+lists@gmail.com are different destinations to their owner. The
// local part allows letters, digits and . _ - + only; that is narrower than
// what the agent refuses, so an address the panel saves always reaches the
// mail server, and it is safe inside a quoted Sieve string.
func CanonicaliseExternal(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrEmpty
	}
	at := strings.IndexByte(raw, '@')
	if at < 0 {
		return "", ErrNoAtSign
	}
	if strings.IndexByte(raw[at+1:], '@') >= 0 {
		return "", ErrMultipleAtSigns
	}
	local, domainRaw := raw[:at], raw[at+1:]

	if local == "" {
		return "", ErrLocalEmpty
	}
	if len(local) > maxLocalOctets {
		return "", ErrLocalTooLong
	}
	for i := 0; i < len(local); i++ {
		c := local[i]
		switch {
		case c > 0x7f:
			return "", ErrLocalNonASCII
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-' || c == '+':
		default:
			return "", ErrLocalShellMeta
		}
	}
	if local[0] == '.' || local[len(local)-1] == '.' || strings.Contains(local, "..") {
		return "", ErrLocalDots
	}

	domain, err := canonDomain(domainRaw)
	if err != nil {
		return "", err
	}
	if !strings.Contains(domain, ".") {
		return "", ErrDomainSingleLabel
	}

	// 64 + 1 + 253 octets: always fits the 320-wide address columns.
	return strings.ToLower(local) + "@" + domain, nil
}
