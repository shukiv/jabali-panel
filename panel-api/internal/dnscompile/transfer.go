package dnscompile

import "git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"

// TransferTargets is who may transfer a hosted zone (PowerDNS ALLOW-AXFR-FROM)
// and who is sent a NOTIFY when it changes (ALSO-NOTIFY). The reconciler's
// zone push and the DNS-01 hook (`jabali dns acme-hook`) both send these, so
// they come from one place.
//
// The targets are ns2_ipv4 and the secondary DNS servers (dns_secondary_ips,
// GH #2131), ns2 first, each once. ns1 is this server, so it never needs AXFR
// permission for itself; 127.0.0.1 is allowed for `dig AXFR @127.0.0.1`
// troubleshooting and is never notified.
func TransferTargets(srv *models.ServerSettings) (allowAXFR, alsoNotify []string) {
	seen := map[string]bool{}
	add := func(ip string) {
		if ip == "" || seen[ip] {
			return
		}
		seen[ip] = true
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
