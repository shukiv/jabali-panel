package settingsops

import (
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// NameserverAddrCall is what a change of ns1_ipv4/ns2_ipv4 applies on the
// host: PowerDNS is restarted if it does not listen on a new nameserver
// address that is on an interface of this server (dns.pdns_rebind decides).
// In an IP swap the operator moves the IP pool's default first and Settings
// last, so by now the apex A records already name the new address. Nil when
// neither address changed or both are empty.
func NameserverAddrCall(before, after *models.ServerSettings) *AgentCall {
	if before.NS1IPv4 == after.NS1IPv4 && before.NS2IPv4 == after.NS2IPv4 {
		return nil
	}
	var addrs []string
	for _, a := range []string{after.NS1IPv4, after.NS2IPv4} {
		if a != "" && (len(addrs) == 0 || addrs[0] != a) {
			addrs = append(addrs, a)
		}
	}
	if len(addrs) == 0 {
		return nil
	}
	return &AgentCall{
		Method:  "dns.pdns_rebind",
		Params:  map[string]any{"addresses": addrs},
		Timeout: 60 * time.Second,
	}
}
