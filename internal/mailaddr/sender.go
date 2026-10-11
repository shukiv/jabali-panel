package mailaddr

import (
	"errors"
	"fmt"
	"strings"
)

// Errors of CanonicaliseSender beside the shared sentinels above.
var (
	ErrSenderLocal = errors.New("mailaddr: local part has a character or dot a sender address can't have")
	ErrDomainNoDot = errors.New("mailaddr: domain has no dot")
)

// CanonicaliseSender returns the canonical form of an address a mailbox
// trusts as a sender (GH #2017): both parts lowercased, the domain
// punycoded. Unlike Canonicalise it keeps a +tag: Stalwart matches a sender
// to a contact card exactly, ignoring only case, so news+weekly@x.com and
// news@x.com are different senders.
//
// The local part is a dot-atom of letters, digits and . _ - + =, which
// covers the addresses people send from; quoted local parts and the rarer
// RFC 5322 specials are refused. The domain must have a dot.
func CanonicaliseSender(raw string) (string, error) {
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
	local, err := senderLocal(raw[:at])
	if err != nil {
		return "", err
	}
	domain, err := canonDomain(raw[at+1:])
	if err != nil {
		return "", err
	}
	if !strings.Contains(domain, ".") {
		return "", ErrDomainNoDot
	}
	// idna.Lookup lets an empty label through (x..com, x.com.).
	for _, label := range strings.Split(domain, ".") {
		if label == "" {
			return "", fmt.Errorf("%w: empty label in %q", ErrIDNA, domain)
		}
	}
	return local + "@" + domain, nil
}

func senderLocal(raw string) (string, error) {
	if raw == "" {
		return "", ErrLocalEmpty
	}
	if len(raw) > maxLocalOctets {
		return "", ErrLocalTooLong
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] > 0x7f {
			return "", ErrLocalNonASCII
		}
	}
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == '-' || c == '+' || c == '=':
		default:
			return "", ErrSenderLocal
		}
	}
	if raw[0] == '.' || raw[len(raw)-1] == '.' || strings.Contains(raw, "..") {
		return "", ErrSenderLocal
	}
	return strings.ToLower(raw), nil
}
