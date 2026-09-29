package dnscompile

import (
	"regexp"
	"strings"
)

// GH #648 (DMARCbis): jabali generates a canonical per-domain _dmarc record.
// The base policy is fixed (p=quarantine; sp=quarantine; adkim=r; aspf=r); the
// operator-settable DMARCbis tags are `np` (policy for non-existent subdomains
// — RFC 9091/DMARCbis) and `t=y` (testing mode, which replaces the retired
// `pct`). Keeping generation in one place lets the reconciler recognise a
// jabali-authored record (any canonical variant) and re-render it when the
// domain's setting changes, WITHOUT clobbering a fully operator-customised
// _dmarc (mail_provider_reconcile preserves anything not in this set).
//
// The record asks receivers for aggregate reports at postmaster@<zone>
// (`rua`). Stalwart reads the reports sent to postmaster@* and the panel
// imports them for the deliverability score (ADR-0110). The address is in the
// zone itself, so no RFC 7489 §7.1 authorisation record is needed. Records
// rendered before `rua` was added stay canonical, so the reconciler upgrades
// them.

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

// dmarcZoneRe is a plain DNS name: dot-separated labels of letters, digits
// and inner hyphens.
var dmarcZoneRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

// dmarcReportDomain returns zone as the domain of the rua address, or "" when
// it is not a plain DNS name. Zone names come from the database; the check
// keeps a malformed one from adding tags to the record or breaking its quoting.
func dmarcReportDomain(zone string) string {
	zone = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(zone)), ".")
	if len(zone) > 253 || !dmarcZoneRe.MatchString(zone) {
		return ""
	}
	return zone
}

// BuildDMARCString renders jabali's canonical _dmarc TXT content (quoted) for
// a zone. An unusable zone name renders the record without rua.
func BuildDMARCString(zone, np string, testing bool) string {
	return buildDMARC(dmarcReportDomain(zone), np, testing)
}

// buildDMARC renders the record; an empty reportDomain omits rua, which is
// the record jabali rendered before rua was added.
func buildDMARC(reportDomain, np string, testing bool) string {
	s := `"v=DMARC1; p=quarantine; sp=quarantine; adkim=r; aspf=r`
	if reportDomain != "" {
		s += "; rua=mailto:postmaster@" + reportDomain
	}
	if np == "none" || np == "quarantine" || np == "reject" {
		s += "; np=" + np
	}
	if testing {
		s += "; t=y"
	}
	return s + `"`
}

// CanonicalDMARCStrings returns every record jabali renders or has rendered
// for zone: each np/testing variant with and without rua. The reconciler
// treats a _dmarc whose content is in this set as jabali-authored (safe to
// re-render); anything else is an operator edit and left untouched.
func CanonicalDMARCStrings(zone string) []string {
	reportDomain := dmarcReportDomain(zone)
	out := make([]string, 0, (len(DMARCNPValues)+1)*4)
	for _, np := range append([]string{""}, DMARCNPValues...) {
		for _, testing := range []bool{false, true} {
			out = append(out, buildDMARC("", np, testing))
			if reportDomain != "" {
				out = append(out, buildDMARC(reportDomain, np, testing))
			}
		}
	}
	return out
}

// IsCanonicalDMARC reports whether content is one jabali could have generated
// for zone.
func IsCanonicalDMARC(zone, content string) bool {
	for _, s := range CanonicalDMARCStrings(zone) {
		if s == content {
			return true
		}
	}
	return false
}
