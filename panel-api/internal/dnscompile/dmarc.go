package dnscompile

// GH #648 (DMARCbis): jabali generates a canonical per-domain _dmarc record.
// The base policy is fixed (p=quarantine; sp=quarantine; adkim=r; aspf=r); the
// operator-settable DMARCbis tags are `np` (policy for non-existent subdomains
// — RFC 9091/DMARCbis) and `t=y` (testing mode, which replaces the retired
// `pct`). Keeping generation in one place lets the reconciler recognise a
// jabali-authored record (any canonical variant) and re-render it when the
// domain's setting changes, WITHOUT clobbering a fully operator-customised
// _dmarc (mail_provider_reconcile preserves anything not in this set).

// DMARCNPValues is the allowlist for the np tag. Empty = omit np (receivers
// fall back to sp, then p — the pre-DMARCbis behaviour).
var DMARCNPValues = []string{"none", "quarantine", "reject"}

// ValidDMARCNP reports whether np is empty (omit) or one of the allowed values.
func ValidDMARCNP(np string) bool {
	if np == "" {
		return true
	}
	for _, v := range DMARCNPValues {
		if np == v {
			return true
		}
	}
	return false
}

// BuildDMARCString renders jabali's canonical _dmarc TXT content (quoted).
// BuildDMARCString("", false) is byte-identical to the legacy hardcoded record,
// so pre-#648 records are recognised as canonical (see CanonicalDMARCStrings).
func BuildDMARCString(np string, testing bool) string {
	s := `"v=DMARC1; p=quarantine; sp=quarantine; adkim=r; aspf=r`
	if np == "none" || np == "quarantine" || np == "reject" {
		s += "; np=" + np
	}
	if testing {
		s += "; t=y"
	}
	return s + `"`
}

// CanonicalDMARCStrings returns every record BuildDMARCString can emit. The
// reconciler treats a _dmarc whose content is in this set as jabali-authored
// (safe to re-render); anything else is an operator edit and left untouched.
func CanonicalDMARCStrings() []string {
	out := make([]string, 0, (len(DMARCNPValues)+1)*2)
	for _, np := range append([]string{""}, DMARCNPValues...) {
		out = append(out, BuildDMARCString(np, false), BuildDMARCString(np, true))
	}
	return out
}

// IsCanonicalDMARC reports whether content is one jabali could have generated.
func IsCanonicalDMARC(content string) bool {
	for _, s := range CanonicalDMARCStrings() {
		if s == content {
			return true
		}
	}
	return false
}
