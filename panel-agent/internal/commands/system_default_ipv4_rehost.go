package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"regexp"
	"slices"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// system.default_ipv4_rehost moves what install.sh pinned to the server's
// IPv4 onto a new default IPv4 of the IP pool, the address tenant vhosts bind:
//
//  1. jabali-default.conf's `listen <ip>:80 default_server`, `listen <ip>:443
//     ssl default_server` and the landing block's `listen <ip>:443 ssl`.
//     nginx never consults the wildcard `listen 80` for an address that has a
//     specific listener, so after the default moved the new address had no
//     default server: an unknown name, and the panel hostname's HTTP-01
//     challenge, landed on whichever tenant vhost came first. nginx -t runs
//     on the result and the old file goes back on failure.
//  2. /etc/hosts lines on the old address (the panel hostname, and mail. /
//     autoconfig.<hostname>, which webmail fetches over the vhost address).
//  3. The CrowdSec self-whitelist (s02-enrich/jabali-self-whitelist.yaml):
//     sites calling themselves now come from the new address, so it is
//     added. The old one is dropped only once it is on no interface of this
//     server: while it is still bound it is still this server, and once it
//     is gone it may be someone else's.
//
// The old address is read from jabali-default.conf, not passed in, so a file
// that drifted earlier converges too. An address that is not on any interface
// is refused before anything is written: nginx cannot bind it, and a
// jabali-default.conf it cannot load would stop every vhost.
//
// Change-time, dispatched when the IP pool's IPv4 default changes, for the
// same reason as nginx.panel_landing_rehost: the panel-api daemon's AppArmor
// profile does not let it read /etc/nginx.
type systemDefaultIPv4RehostParams struct {
	IPv4 string `json:"ipv4"`
}

type systemDefaultIPv4RehostResponse struct {
	From             []string `json:"from,omitempty"`
	ListenRewritten  int      `json:"listen_rewritten"`
	HostsUpdated     bool     `json:"hosts_updated"`
	WhitelistUpdated bool     `json:"whitelist_updated"`
}

// selfWhitelistPath is install.sh's CrowdSec self-whitelist. Overridable in
// tests.
var selfWhitelistPath = "/etc/crowdsec/parsers/s02-enrich/jabali-self-whitelist.yaml"

// defaultVhostListenRE matches a listen directive on a literal IPv4 and port
// 80 or 443. The wildcard (`listen 80`, `listen [::]:80`) and comment lines
// don't match.
var defaultVhostListenRE = regexp.MustCompile(`(?m)^(\s*listen\s+)(\d{1,3}(?:\.\d{1,3}){3})(:(?:80|443)[\s;])`)

// selfWhitelistItemRE matches one `    - "<ip>"` entry of the whitelist's ip
// list as install.sh writes it.
var selfWhitelistItemRE = regexp.MustCompile(`^(\s*-\s*)"([^"]*)"\s*$`)

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

func systemDefaultIPv4RehostHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p systemDefaultIPv4RehostParams
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
			Message: fmt.Sprintf("%s is not on any network interface of this server; nothing was changed", target),
		}
	}

	var resp systemDefaultIPv4RehostResponse
	if err := rehostDefaultVhostListen(ctx, target, &resp); err != nil {
		return nil, err
	}
	if len(resp.From) > 0 {
		hosts, err := os.ReadFile(setHostnameHostsPath)
		if err != nil {
			return nil, fmt.Errorf("read /etc/hosts: %w", err)
		}
		if out, changed := rewriteHostsAddress(hosts, resp.From, target); changed {
			if err := writeFilePreservingMeta(setHostnameHostsPath, out); err != nil {
				return nil, fmt.Errorf("write /etc/hosts: %w", err)
			}
			resp.HostsUpdated = true
		}
	}

	wl, err := os.ReadFile(selfWhitelistPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// The security module is not installed here.
	case err != nil:
		return nil, fmt.Errorf("read %s: %w", selfWhitelistPath, err)
	default:
		var drop []string
		for _, old := range resp.From {
			if a, perr := netip.ParseAddr(old); perr == nil && !addrOnInterface(a) {
				drop = append(drop, old)
			}
		}
		if out, changed := rewriteSelfWhitelist(wl, target, drop); changed {
			if err := writeFilePreservingMeta(selfWhitelistPath, out); err != nil {
				return nil, fmt.Errorf("write %s: %w", selfWhitelistPath, err)
			}
			if err := reloadCrowdsec(ctx); err != nil {
				return nil, err
			}
			resp.WhitelistUpdated = true
		}
	}
	return resp, nil
}

// rehostDefaultVhostListen rewrites jabali-default.conf's address-specific
// listen lines to target and reloads nginx; resp.From gets the addresses they
// were on. Already current is a no-op.
func rehostDefaultVhostListen(ctx context.Context, target string, resp *systemDefaultIPv4RehostResponse) error {
	data, err := os.ReadFile(panelLandingVhostPath)
	if err != nil {
		return &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("read %s: %v", panelLandingVhostPath, err),
		}
	}
	for _, m := range defaultVhostListenRE.FindAllSubmatch(data, -1) {
		if old := string(m[2]); old != target {
			resp.ListenRewritten++
			if !slices.Contains(resp.From, old) {
				resp.From = append(resp.From, old)
			}
		}
	}
	if resp.ListenRewritten == 0 {
		return nil
	}
	rewritten := defaultVhostListenRE.ReplaceAll(data, []byte("${1}"+target+"${3}"))
	if err := writeFilePreservingMeta(panelLandingVhostPath, rewritten); err != nil {
		return &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("write %s: %v", panelLandingVhostPath, err),
		}
	}
	// A broken jabali-default.conf would take every vhost down, so validate
	// the whole config and put the old file back on failure.
	if out, terr := execCommandContext(ctx, "nginx", "-t").CombinedOutput(); terr != nil {
		_ = writeFilePreservingMeta(panelLandingVhostPath, data)
		return &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("nginx -t failed after the listen rewrite (rolled back): %s", strings.TrimSpace(string(out))),
		}
	}
	if out, rerr := execCommandContext(ctx, "systemctl", "reload", "nginx").CombinedOutput(); rerr != nil {
		return &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("systemctl reload nginx failed: %s", strings.TrimSpace(string(out))),
		}
	}
	return nil
}

// rewriteHostsAddress puts target in place of the address of every line whose
// address is one of from, keeping the names, spacing and comments. Other
// lines are kept as they were.
func rewriteHostsAddress(data []byte, from []string, target string) ([]byte, bool) {
	lines := strings.SplitAfter(string(data), "\n")
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		fields := strings.Fields(trimmed)
		if len(fields) < 2 || !slices.Contains(from, fields[0]) {
			continue
		}
		lines[i] = line[:len(line)-len(trimmed)] + target + trimmed[len(fields[0]):]
	}
	out := strings.Join(lines, "")
	return []byte(out), out != string(data)
}

// rewriteSelfWhitelist adds target to the whitelist's ip list (after its last
// entry, same indentation) unless it is there, and removes the entries in
// drop. A file with no ip entry is not one install.sh wrote; it is left alone.
func rewriteSelfWhitelist(data []byte, target string, drop []string) ([]byte, bool) {
	lines := strings.SplitAfter(string(data), "\n")
	prefix, insertAt, have := "", 0, false
	var out []string
	for _, line := range lines {
		m := selfWhitelistItemRE.FindStringSubmatch(strings.TrimRight(line, "\r\n"))
		if m == nil {
			out = append(out, line)
			continue
		}
		prefix = m[1]
		if slices.Contains(drop, m[2]) && m[2] != target {
			insertAt = len(out)
			continue
		}
		have = have || m[2] == target
		out = append(out, line)
		insertAt = len(out)
	}
	if prefix == "" {
		return data, false
	}
	if !have {
		if insertAt > 0 && !strings.HasSuffix(out[insertAt-1], "\n") {
			out[insertAt-1] += "\n"
		}
		out = slices.Insert(out, insertAt, prefix+`"`+target+`"`+"\n")
	}
	res := strings.Join(out, "")
	return []byte(res), res != string(data)
}

func init() {
	Default.Register("system.default_ipv4_rehost", systemDefaultIPv4RehostHandler)
}
