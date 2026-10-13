package settingsops

import (
	"fmt"
	"net"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// maxDNSSecondaryIPs caps dns_secondary_ips (GH #2131). A secondary DNS
// provider publishes a handful of transfer addresses; the cap keeps the
// per-zone ALLOW-AXFR-FROM / ALSO-NOTIFY metadata small.
const maxDNSSecondaryIPs = 16

// NormalizeIPList is how dns_secondary_ips is stored: entries split on
// commas and whitespace, IP addresses in their canonical form, joined with
// commas. An entry that is not an IP address is kept as typed, so Validate
// can name it.
func NormalizeIPList(raw string) string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	for i, f := range fields {
		if ip := net.ParseIP(f); ip != nil {
			fields[i] = ip.String()
		}
	}
	return strings.Join(fields, ",")
}

// validateDNSSecondaryIPs checks dns_secondary_ips. Every entry may transfer
// every hosted zone, so only a plain, routable address is accepted: no
// netmask (PowerDNS's ALSO-NOTIFY can't take one), no loopback (already
// allowed for local troubleshooting), no unspecified, multicast or link-local
// address. Private ranges are fine: a hidden secondary can live there.
func validateDNSSecondaryIPs(s *models.ServerSettings) error {
	ips := models.DNSSecondaryIPList(s)
	if len(ips) > maxDNSSecondaryIPs {
		return fmt.Errorf("dns_secondary_ips: at most %d addresses", maxDNSSecondaryIPs)
	}
	seen := map[string]bool{}
	for _, v := range ips {
		ip := net.ParseIP(v)
		switch {
		case ip == nil:
			return fmt.Errorf("dns_secondary_ips: %q is not an IP address", v)
		case ip.IsLoopback():
			return fmt.Errorf("dns_secondary_ips: %s is a loopback address", v)
		case ip.IsUnspecified():
			return fmt.Errorf("dns_secondary_ips: %s is an unspecified address", v)
		case ip.IsMulticast():
			return fmt.Errorf("dns_secondary_ips: %s is a multicast address", v)
		case ip.IsLinkLocalUnicast():
			return fmt.Errorf("dns_secondary_ips: %s is a link-local address", v)
		}
		key := ip.String()
		if seen[key] {
			return fmt.Errorf("dns_secondary_ips: %s is listed twice", v)
		}
		seen[key] = true
	}
	return nil
}
