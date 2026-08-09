// Package hostedsvc implements the jabalihosted.com free-hostname service
// (JAB-213): PowerDNS-backed IP-derived hostnames + ACME DNS-01 broker for
// Jabali Panel installations. Design + invariants:
// plans/jab213-free-hostname-service.md.
package hostedsvc

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// BaseDomain is the PSL-submitted base under which every label lives.
const BaseDomain = "jabalihosted.com"

// bogonV4 are IPv4 ranges that must never become a label: they are not
// publicly routable, so a hostname pointing into them is useless at best and
// a DNS-rebinding lure at worst. Go's stdlib (IsPrivate/IsLoopback/
// IsLinkLocalUnicast/IsGlobalUnicast) catches RFC1918, loopback, link-local,
// multicast, and unspecified — but MISSES CGNAT (100.64/10), Class E (240/4),
// "this network" (0/8), TEST-NET (RFC5737), benchmarking (198.18/15), and the
// IETF protocol block (192.0.0/24). We refuse all of them explicitly.
var bogonV4 = func() []*net.IPNet {
	cidrs := []string{
		"0.0.0.0/8",       // "this network" (RFC 1122)
		"100.64.0.0/10",   // CGNAT / carrier-grade NAT (RFC 6598) — not internet-routable
		"192.0.0.0/24",    // IETF protocol assignments (RFC 6890)
		"192.0.2.0/24",    // TEST-NET-1 (RFC 5737)
		"198.18.0.0/15",   // benchmarking (RFC 2544)
		"198.51.100.0/24", // TEST-NET-2 (RFC 5737)
		"203.0.113.0/24",  // TEST-NET-3 (RFC 5737)
		"240.0.0.0/4",     // reserved / Class E (RFC 1112) — stdlib treats as global unicast
	}
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, _ := net.ParseCIDR(c)
		out = append(out, n)
	}
	return out
}()

// LabelFromIP derives the hostname label from the OBSERVED public source
// address of the registering box — cPanel-cprapid style: 45.79.1.2 →
// "45-79-1-2". Labels are never client-chosen (the completed TCP handshake is
// the proof of IP control), and any non-public / bogon range is refused.
func LabelFromIP(ip net.IP) (string, error) {
	v4 := ip.To4()
	if v4 == nil {
		return "", fmt.Errorf("only IPv4 sources are supported in v1 (got %s)", ip)
	}
	if !v4.IsGlobalUnicast() || v4.IsPrivate() || v4.IsLoopback() || v4.IsLinkLocalUnicast() {
		return "", fmt.Errorf("refusing non-public source address %s", v4)
	}
	for _, n := range bogonV4 {
		if n.Contains(v4) {
			return "", fmt.Errorf("refusing bogon source address %s (%s)", v4, n)
		}
	}
	return strings.ReplaceAll(v4.String(), ".", "-"), nil
}

// CollisionLabel returns the nth fallback label for a base label when several
// boxes share one public IP (NAT): "1-2-3-4" → "1-2-3-4-b", "1-2-3-4-c", …
// n starts at 1 for the first fallback.
func CollisionLabel(base string, n int) string {
	return fmt.Sprintf("%s-%c", base, 'a'+n)
}

// labelRe is the full shape of every label this service ever creates:
// a dash-encoded IPv4 with an optional single-letter collision suffix.
// Defense-in-depth for anything that round-trips a label through storage.
var labelRe = regexp.MustCompile(`^\d{1,3}-\d{1,3}-\d{1,3}-\d{1,3}(-[a-z])?$`)

// ValidLabel reports whether s is a label this service could have issued.
func ValidLabel(s string) bool {
	if len(s) > 63 || !labelRe.MatchString(s) {
		return false
	}
	// Every octet must round-trip as a real IPv4 octet.
	parts := strings.Split(s, "-")
	for i := 0; i < 4; i++ {
		var o int
		if _, err := fmt.Sscanf(parts[i], "%d", &o); err != nil || o > 255 {
			return false
		}
	}
	return true
}

// FQDN returns the fully-qualified hostname for a label.
func FQDN(label string) string { return label + "." + BaseDomain }

// maxChallengeRecordsPerLabel bounds how many ACME challenge TXT records may
// exist simultaneously at one label's _acme-challenge name.
//
// SetChallenge is add-only on purpose: issuing a wildcard cert needs the apex
// and the wildcard challenge live at the SAME name at the same time, so a
// replace would break renewal. That also means nothing stops a valid token
// holder from scripting distinct values, so the count is capped. Legitimate
// use needs 2; 8 leaves room for a retry that raced cleanup.
const maxChallengeRecordsPerLabel = 8
