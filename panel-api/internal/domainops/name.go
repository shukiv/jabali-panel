package domainops

import "strings"

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
