package commands

import (
	"context"
	"encoding/json"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// ipListReq has no fields — `ip.list` enumerates every address on every
// interface unconditionally. Caller filters in Go.
type ipListReq struct{}

// ipListEntry is one row of the response. We flatten iproute2's
// per-interface nesting here so panel-api doesn't need to walk a
// two-level structure to find a specific address.
type ipListEntry struct {
	Address   string `json:"address"`
	Family    string `json:"family"`    // "ipv4" | "ipv6"
	Prefixlen int    `json:"prefixlen"` // 0..32 for v4, 0..128 for v6
	Interface string `json:"interface"`
	Scope     string `json:"scope"` // "global" / "host" / "link"
}

type ipListResp struct {
	Entries []ipListEntry `json:"entries"`
}

// ipAddrShowJSON is the structure iproute2 emits for `ip -j addr show`.
// Only the fields we read are declared; iproute2 emits many more (txqlen,
// link/ether, etc.) which the JSON decoder ignores by default.
type ipAddrShowJSON struct {
	IfIndex  int    `json:"ifindex"`
	IfName   string `json:"ifname"`
	AddrInfo []struct {
		Family    string `json:"family"`     // "inet" / "inet6"
		Local     string `json:"local"`      // the IP literal
		Prefixlen int    `json:"prefixlen"`
		Scope     string `json:"scope"`
	} `json:"addr_info"`
}

func ipListHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	out, err := execCommandContext(ctx, "ip", "-j", "addr", "show").Output()
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("ip -j addr show failed: %v", err),
		}
	}

	var raw []ipAddrShowJSON
	if err := json.Unmarshal(out, &raw); err != nil {
		// Hard-fail: a parse error here means iproute2's JSON shape has
		// drifted, and silently returning empty would mask binds that
		// are about to conflict. Caller treats this as "retry later".
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to parse ip addr list: %v", err),
		}
	}

	resp := ipListResp{Entries: make([]ipListEntry, 0, 16)}
	for _, iface := range raw {
		for _, a := range iface.AddrInfo {
			fam := ""
			switch a.Family {
			case "inet":
				fam = "ipv4"
			case "inet6":
				fam = "ipv6"
			default:
				continue
			}
			resp.Entries = append(resp.Entries, ipListEntry{
				Address:   a.Local,
				Family:    fam,
				Prefixlen: a.Prefixlen,
				Interface: iface.IfName,
				Scope:     a.Scope,
			})
		}
	}
	return resp, nil
}

func init() {
	Default.Register("ip.list", ipListHandler)
}
