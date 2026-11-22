package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"regexp"
	"slices"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// nginx.default_vhost_listen points the address-specific listen lines of
// /etc/nginx/sites-available/jabali-default.conf at the server's default IPv4,
// then reloads nginx.
//
// install.sh writes `listen <ip>:80 default_server`, `listen <ip>:443 ssl
// default_server` and the landing block's `listen <ip>:443 ssl` with the IPv4
// tenant vhosts bind, because nginx never consults the wildcard `listen 80`
// for an address that has a specific listener. Tenant vhosts follow the IP
// pool's default IPv4 on the next reconcile pass, but nothing rewrote these
// lines: after the default moved to a new address, the old address kept the
// panel's default server and the new one had none, so an unknown name (and
// the panel hostname's HTTP-01 challenge) landed on whichever tenant vhost
// came first.
//
// Change-time, dispatched when the IP pool's IPv4 default changes, for the
// same reason as nginx.panel_landing_rehost: the panel-api daemon's AppArmor
// profile does not let it read /etc/nginx.
//
// The current address is read from the file, not passed in, so a file that
// drifted earlier converges too. An address that is not on any interface of
// this server is refused before anything is written: nginx cannot bind it,
// and a jabali-default.conf it cannot load would stop every vhost.
type nginxDefaultVhostListenParams struct {
	IPv4 string `json:"ipv4"`
}

type nginxDefaultVhostListenResponse struct {
	Rewritten    bool   `json:"rewritten"`
	Reason       string `json:"reason,omitempty"`
	Replacements int    `json:"replacements,omitempty"`
}

// defaultVhostListenRE matches a listen directive on a literal IPv4 and port
// 80 or 443. The wildcard (`listen 80`, `listen [::]:80`) and comment lines
// don't match.
var defaultVhostListenRE = regexp.MustCompile(`(?m)^(\s*listen\s+)(\d{1,3}(?:\.\d{1,3}){3})(:(?:80|443)[\s;])`)

// addrOnInterface reports whether ip is one of this server's interface
// addresses.
func addrOnInterface(ip netip.Addr) bool {
	for _, p := range listOwnAddrs() {
		if p.Addr() == ip {
			return true
		}
	}
	return false
}

func nginxDefaultVhostListenHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p nginxDefaultVhostListenParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	raw := strings.TrimSpace(p.IPv4)
	ip, err := netip.ParseAddr(raw)
	if err != nil || !ip.Is4() || ip.String() != raw || !ip.IsGlobalUnicast() {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid ipv4 %q", p.IPv4),
		}
	}
	target := ip.String()
	if !addrOnInterface(ip) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeFailedPrecondition,
			Message: fmt.Sprintf("%s is not on any network interface of this server; the default vhost keeps its address", target),
		}
	}

	data, err := os.ReadFile(panelLandingVhostPath)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("read %s: %v", panelLandingVhostPath, err),
		}
	}
	matches := defaultVhostListenRE.FindAllSubmatch(data, -1)
	var from []string
	changed := 0
	for _, m := range matches {
		if old := string(m[2]); old != target {
			changed++
			if !slices.Contains(from, old) {
				from = append(from, old)
			}
		}
	}
	if len(from) == 0 {
		reason := "already current"
		if len(matches) == 0 {
			reason = "no address-specific listen line"
		}
		return nginxDefaultVhostListenResponse{Rewritten: false, Reason: reason}, nil
	}
	rewritten := defaultVhostListenRE.ReplaceAll(data, []byte("${1}"+target+"${3}"))

	if err := writeFilePreservingMeta(panelLandingVhostPath, rewritten); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("write %s: %v", panelLandingVhostPath, err),
		}
	}
	// A broken jabali-default.conf would take every vhost down, so validate
	// the whole config and put the old file back on failure.
	if out, terr := execCommandContext(ctx, "nginx", "-t").CombinedOutput(); terr != nil {
		_ = writeFilePreservingMeta(panelLandingVhostPath, data)
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("nginx -t failed after the listen rewrite (rolled back): %s", strings.TrimSpace(string(out))),
		}
	}
	if out, rerr := execCommandContext(ctx, "systemctl", "reload", "nginx").CombinedOutput(); rerr != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("systemctl reload nginx failed: %s", strings.TrimSpace(string(out))),
		}
	}
	return nginxDefaultVhostListenResponse{
		Rewritten:    true,
		Reason:       strings.Join(from, ",") + " -> " + target,
		Replacements: changed,
	}, nil
}

func init() {
	Default.Register("nginx.default_vhost_listen", nginxDefaultVhostListenHandler)
}
