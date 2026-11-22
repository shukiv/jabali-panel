package settingsops

import "time"

// DefaultIPv4ListenCall is what promoting an IPv4 to the IP pool's default
// applies on the host: jabali-default.conf's address-specific listen lines
// (the panel's default server for that address) follow the address tenant
// vhosts now bind. The REST PATCH /admin/ips/:id and `jabali ip update
// --default` both dispatch it. The agent refuses an address that is not on
// an interface of the server, and leaves the file as it was.
func DefaultIPv4ListenCall(address string) AgentCall {
	return AgentCall{
		Method:  "nginx.default_vhost_listen",
		Params:  map[string]any{"ipv4": address},
		Timeout: 30 * time.Second,
	}
}
