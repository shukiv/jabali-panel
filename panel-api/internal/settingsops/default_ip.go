package settingsops

import "time"

// DefaultIPv4RehostCall is what promoting an IPv4 to the IP pool's default
// applies on the host: the panel's default server (jabali-default.conf's
// address-specific listen lines), the /etc/hosts lines and the CrowdSec
// self-whitelist entry install.sh pinned to the old address follow the
// address tenant vhosts now bind. The REST PATCH /admin/ips/:id and `jabali ip
// update --default` both dispatch it. The agent refuses an address that is
// not on an interface of the server, and then changes nothing.
func DefaultIPv4RehostCall(address string) AgentCall {
	return AgentCall{
		Method:  "system.default_ipv4_rehost",
		Params:  map[string]any{"ipv4": address},
		Timeout: 30 * time.Second,
	}
}
