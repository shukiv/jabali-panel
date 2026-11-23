package commands

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// dns.pdns_rebind restarts PowerDNS when a nameserver address the panel
// was just given (server_settings.ns1_ipv4/ns2_ipv4) is on an interface of
// this server but PowerDNS does not listen on it.
//
// PowerDNS binds the addresses in local-address= once, at start. The
// pdns-local-address helper (ExecStartPre, from install.sh) rewrites that
// line to every address the server has, so a restart picks up an address
// added since; until then the nameserver address answers nothing. After an
// IP swap that took every zone whose NS records name this server offline
// until someone restarted pdns by hand.
//
// Dispatched when Settings change ns1_ipv4/ns2_ipv4, which in an IP swap comes
// after the IP pool's default moved and the apex A records followed it:
// restarting earlier would hand out the old apex addresses on the new one.
// Nothing restarts when every address is already listened on, is not on this
// server (a nameserver elsewhere), or the helper is not installed (a restart
// would bind the same addresses again).
type dnsPDNSRebindParams struct {
	Addresses []string `json:"addresses"`
}

type dnsPDNSRebindResponse struct {
	Restarted bool     `json:"restarted"`
	Added     []string `json:"added,omitempty"`
	Reason    string   `json:"reason,omitempty"`
}

// Paths dns.pdns_rebind reads; vars so tests can redirect them.
var (
	pdnsJabaliConfPath     = "/etc/powerdns/pdns.d/01-jabali-mysql.conf"
	pdnsLocalAddressDropIn = "/etc/systemd/system/pdns.service.d/20-jabali-local-address.conf"
)

func dnsPDNSRebindHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dnsPDNSRebindParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}
	if len(p.Addresses) == 0 || len(p.Addresses) > 4 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "addresses: 1 to 4 required"}
	}
	var want []netip.Addr
	for _, s := range p.Addresses {
		a, err := netip.ParseAddr(s)
		if err != nil || a.String() != s || a.Zone() != "" || !a.IsGlobalUnicast() {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("invalid address %q", s)}
		}
		if !slices.Contains(want, a) { // ns1 and ns2 often share one address
			want = append(want, a)
		}
	}

	conf, err := os.ReadFile(pdnsJabaliConfPath)
	if errors.Is(err, os.ErrNotExist) {
		return dnsPDNSRebindResponse{Reason: "PowerDNS is not set up by jabali here"}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", pdnsJabaliConfPath, err)
	}
	listened := pdnsLocalAddresses(conf)
	var missing []string
	for _, a := range want {
		if addrOnInterface(a) && !listened[a] {
			missing = append(missing, a.String())
		}
	}
	if len(missing) == 0 {
		return dnsPDNSRebindResponse{Reason: "already listening"}, nil
	}
	if _, err := os.Stat(pdnsLocalAddressDropIn); err != nil {
		return dnsPDNSRebindResponse{Reason: "pdns-local-address helper not installed; restart pdns after `jabali update`"}, nil
	}
	// try-restart: a stopped pdns stays stopped; it binds them when started.
	if out, err := execCommandContext(ctx, "systemctl", "try-restart", "pdns").CombinedOutput(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("systemctl try-restart pdns failed: %s", strings.TrimSpace(string(out))),
		}
	}
	return dnsPDNSRebindResponse{Restarted: true, Added: missing}, nil
}

// pdnsLocalAddresses returns the addresses of the first local-address= line:
// `1.2.3.4:53, [2001:db8::1]:53, 127.0.0.1:5300` (a bare address means :53).
func pdnsLocalAddresses(conf []byte) map[netip.Addr]bool {
	out := map[netip.Addr]bool{}
	sc := bufio.NewScanner(bytes.NewReader(conf))
	for sc.Scan() {
		v, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "local-address=")
		if !ok {
			continue
		}
		for _, e := range strings.Split(v, ",") {
			e = strings.TrimSpace(e)
			if ap, err := netip.ParseAddrPort(e); err == nil {
				out[ap.Addr()] = true
			} else if a, err := netip.ParseAddr(e); err == nil {
				out[a] = true
			}
		}
		break
	}
	return out
}

func init() {
	Default.Register("dns.pdns_rebind", dnsPDNSRebindHandler)
}
