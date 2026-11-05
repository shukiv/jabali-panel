package commands

import (
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/appseccfg"
)

// vhostPanelScope names the panel: this host's name and the addresses
// jabali's default vhost holds, as the AppSec config's panel-path allowlist
// names it. A var for tests.
var vhostPanelScope = func() (host string, addrs []string) {
	return appseccfg.LocalPanelHost(), appseccfg.LocalPanelAddrs()
}

// panelHostGuard is the Host pattern a domain's vhost refuses with 421: the
// panel's names, bare and with its port (appseccfg.PanelHostPattern). A
// request naming the panel is the panel's, never a domain's, not even on an
// address the domain has to itself. "" for a vhost that serves the panel's own
// name (names holds every name in its server_name), and when the panel's name
// cannot be read.
func panelHostGuard(names ...string) string {
	host, addrs := vhostPanelScope()
	if host == "" {
		return ""
	}
	for _, n := range names {
		if strings.EqualFold(strings.TrimSpace(n), host) {
			return ""
		}
	}
	return appseccfg.PanelHostPattern(host, addrs)
}
