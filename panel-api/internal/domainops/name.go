package domainops

import (
	"errors"
	"regexp"
	"strings"
)

// Domain-name validation sentinels (JAB-279 AC2/AC6). ValidateDomainName is the
// single FQDN gate the REST create/rename/alias handlers, the automation
// account-create handler, and the operator CLI all call, so the accepted set of
// domain names cannot drift between adapters. Each adapter maps these typed
// reasons to its own transport-shaped message (the Module carries no HTTP or CLI
// wording), the same adapter pattern as ValidateDocumentRoot's sentinels.
var (
	ErrDomainNameEmpty      = errors.New("domainops: domain name is empty")
	ErrDomainNameWhitespace = errors.New("domainops: domain name contains whitespace")
	ErrDomainNameTooLong    = errors.New("domainops: domain name exceeds 253 characters")
	ErrDomainNameHTML       = errors.New("domainops: domain name contains HTML characters")
	ErrDomainNameTraversal  = errors.New("domainops: domain name contains path characters")
	ErrDomainNameNotFQDN    = errors.New("domainops: domain name is not a valid FQDN")
)

var (
	// nameFQDNRe is the RFC 1035 shape: at least two labels and a 2+ letter TLD,
	// letters/digits/hyphens only. It alone rejects HTML tags, path separators,
	// consecutive dots, and whitespace; the explicit checks below run first only
	// to give each reason its own sentinel (and message) instead of a blanket
	// "not a valid FQDN".
	nameFQDNRe    = regexp.MustCompile(`^(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,63}$`)
	nameHTMLTagRe = regexp.MustCompile(`<[^>]*>`)
)

// Domain-name canonicalization (JAB-279, GH #884). The REST create/rename/alias
// handlers, the automation account-create handler, and the operator CLI all
// route a domain name through this leaf before anything consumes it, so the
// stored identity cannot drift between adapters — the same one-owner-per-policy
// shape as CheckOwnerEligible and ValidateDocumentRoot.
//
// Domain names are case-insensitive per DNS, but jabali uses the stored string
// verbatim for the docroot path, cert lineage, DNS zone, and nginx server_name.
// A mixed-case entry (a mobile keyboard's autocorrected capital) is accepted by
// the RFC check yet yields a site that never resolves. Trimming edge whitespace
// and lowercasing produces the one canonical form every adapter stores, so the
// same input persists the same domain regardless of the door it came through.
//
// This is a pure transform, not a gate: callers still run their FQDN validator
// on the result to enforce RFC shape.
func NormalizeDomainName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// AncestorDomains returns the strict parent suffixes of a canonicalized domain
// name that are themselves registrable (>= 2 labels), most-specific first. For
// "a.b.example.com" it yields ["b.example.com", "example.com"] — the names
// another tenant could already own as a parent zone of this one. The single-
// label TLD ("com") is excluded: validateDomainName rejects a bare TLD, so no
// domain row can hold it, and treating it as an "ancestor domain" is nonsense.
//
// Used by the cross-tenant subdomain-hijack guard (GH #1789), which walks these
// ancestors to find a differently-owned parent zone. The input is assumed
// already normalized (NormalizeDomainName); this is a pure transform.
func AncestorDomains(name string) []string {
	labels := strings.Split(name, ".")
	// Drop one or more leftmost labels while keeping at least two labels, so
	// the shortest ancestor returned is a registrable second-level domain.
	var out []string
	for i := 1; i+1 < len(labels); i++ {
		out = append(out, strings.Join(labels[i:], "."))
	}
	return out
}

// ValidateDomainName enforces the RFC 1035 FQDN shape and returns a typed reason
// on failure (nil when valid). It is a pure gate and does NOT normalize — callers
// run NormalizeDomainName first, exactly as before, so what a door validates is
// what it stores.
//
// The checks run in a fixed order so the first failing reason is deterministic
// across adapters: empty, whitespace, over-length, HTML characters, path
// characters, then the FQDN regex. The whitespace / HTML / path checks are
// redundant with the regex (which rejects every one of those inputs) but run
// first so the caller can surface a specific reason instead of a blanket
// "not a valid FQDN".
func ValidateDomainName(s string) error {
	if strings.TrimSpace(s) == "" {
		return ErrDomainNameEmpty
	}
	if strings.ContainsAny(s, " \t\n\r") {
		return ErrDomainNameWhitespace
	}
	if len(s) > 253 {
		return ErrDomainNameTooLong
	}
	if nameHTMLTagRe.MatchString(s) {
		return ErrDomainNameHTML
	}
	if strings.Contains(s, "..") || strings.Contains(s, "/") || strings.Contains(s, "\\") {
		return ErrDomainNameTraversal
	}
	if !nameFQDNRe.MatchString(s) {
		return ErrDomainNameNotFQDN
	}
	return nil
}
