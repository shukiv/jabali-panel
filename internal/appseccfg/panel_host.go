package appseccfg

import (
	"net"
	"os"
	"regexp"
	"sort"
	"strings"
)

// PanelPort is the port the panel is served on. A client puts it in the Host
// header, and the CrowdSec nginx bouncer hands Host to AppSec as it came, so a
// request to https://<host>:8443/ carries "<host>:8443".
const PanelPort = "8443"

// panelHostFilter scopes the panel-path skips to the panel (GH #706): Host is
// one of the panel's names, bare or with PanelPort. nginx picks the server
// block by Host, so a request naming the panel never reaches a hosted site.
// "" when no panel host is given (path only). With a host that is not a valid
// name and no addresses it is "false": the skips then apply nowhere.
func panelHostFilter(o Opts) string {
	if strings.TrimSpace(o.PanelHost) == "" {
		return ""
	}
	forms := PanelHostForms(o.PanelHost, o.PanelAddrs)
	if len(forms) == 0 {
		return "false"
	}
	quoted := make([]string, len(forms))
	for i, f := range forms {
		quoted[i] = `"` + f + `"`
	}
	return "req.Host in [" + strings.Join(quoted, ", ") + "]"
}

// PanelHostForms lists the Host values that name the panel: host, then each
// IPv4 address in addrs, each bare and with PanelPort. Anything that is not a
// valid host name or IPv4 address is left out, and so are duplicates.
func PanelHostForms(host string, addrs []string) []string {
	var out []string
	for _, h := range sanitizeWebmailHosts([]string{host}) {
		if net.ParseIP(h) == nil {
			out = append(out, h, h+":"+PanelPort)
		}
	}
	seen := map[string]bool{}
	var ips []string
	for _, a := range addrs {
		ip := net.ParseIP(strings.TrimSpace(a)).To4()
		if ip == nil || seen[ip.String()] {
			continue
		}
		seen[ip.String()] = true
		ips = append(ips, ip.String())
	}
	sort.Strings(ips)
	for _, ip := range ips {
		out = append(out, ip, ip+":"+PanelPort)
	}
	return out
}

// LocalPanelHost is this host's name, which is the panel's (M6.4).
// `jabali-panel appsec render-config` and the agent both scope the panel-path
// skips with it, so neither re-render undoes the other.
func LocalPanelHost() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(h))
}

// DefaultVhostPath is jabali's default nginx vhost (install.sh
// install_nginx_default_vhost).
const DefaultVhostPath = "/etc/nginx/sites-available/jabali-default.conf"

// LocalPanelAddrs lists the IPv4 addresses an admin can open the panel on by
// address here: the ones jabali's default vhost holds as default_server on
// :443 (DefaultServerAddrs).
func LocalPanelAddrs() []string {
	return DefaultServerAddrs(DefaultVhostPath)
}

var defaultServerListen = regexp.MustCompile(`^listen\s+(\d{1,3}(?:\.\d{1,3}){3}):443\s[^;#]*\bdefault_server\b`)

// DefaultServerAddrs lists, sorted, the IPv4 addresses the nginx config at
// path listens on as default_server for :443. On those, a Host naming no site
// reaches jabali's own server, never a hosted site, so the address is safe to
// name the panel. A hosted site bound to an address of its own is that
// address's default, which is why the server's other addresses are not used.
func DefaultServerAddrs(path string) []string {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(string(body), "\n") {
		m := defaultServerListen.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		ip := net.ParseIP(m[1]).To4()
		if ip == nil || seen[ip.String()] {
			continue
		}
		seen[ip.String()] = true
		out = append(out, ip.String())
	}
	sort.Strings(out)
	return out
}
