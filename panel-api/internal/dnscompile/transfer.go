package dnscompile

import (
	"net/netip"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// TransferTargets is who may transfer a hosted zone (PowerDNS ALLOW-AXFR-FROM)
// and who is sent a NOTIFY when it changes (ALSO-NOTIFY). The reconciler's
// zone push and the DNS-01 hook (`jabali dns acme-hook`) both send these, so
// they come from one place.
//
// The targets are ns2_ipv4 and the secondary DNS servers (dns_secondary_ips,
// GH #2131), ns2 first, each once. This server's own public addresses are left
// out: it needs no AXFR permission for itself, and PowerDNS logs a failure for
// each NOTIFY it sends itself. On a single-server install ns2 is such an
// address, so there are no targets. ns1_ipv4 is not treated as this server's
// address, because in a hidden-primary setup ns1 is the DNS provider. 127.0.0.1
// is allowed for `dig AXFR @127.0.0.1` troubleshooting and is never notified.
func TransferTargets(srv *models.ServerSettings) (allowAXFR, alsoNotify []string) {
	seen := map[string]bool{"": true}
	if srv != nil {
		seen[canonicalIP(srv.PublicIPv4)] = true
		seen[canonicalIP(srv.PublicIPv6)] = true
	}
	add := func(ip string) {
		key := canonicalIP(ip)
		if seen[key] {
			return
		}
		seen[key] = true
		alsoNotify = append(alsoNotify, ip)
	}
	if srv != nil {
		add(srv.NS2IPv4)
	}
	for _, ip := range models.DNSSecondaryIPList(srv) {
		add(ip)
	}
	allowAXFR = append(append(allowAXFR, alsoNotify...), "127.0.0.1")
	return allowAXFR, alsoNotify
}

// canonicalIP makes two spellings of one address compare equal. A value that
// isn't an address comes back as it is.
func canonicalIP(s string) string {
	if addr, err := netip.ParseAddr(s); err == nil {
		return addr.Unmap().String()
	}
	return s
}
