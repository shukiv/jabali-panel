package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type ipUnbindReq struct {
	Address   string `json:"address"`
	Prefixlen *int   `json:"prefixlen,omitempty"`
	Interface string `json:"interface,omitempty"`
}

type ipUnbindResp struct {
	Unbound bool `json:"unbound"`
}

// ipUnbindHandler is idempotent by design: `Cannot assign requested
// address` (ENOADDR) and `Cannot find device` map to success because
// both mean "the end state we want is already in place". Any other
// non-zero exit surfaces as an AgentError so the operator knows
// something actually blocked the removal.
func ipUnbindHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req ipUnbindReq
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: err.Error()}
	}
	req.Address = strings.TrimSpace(req.Address)
	if req.Address == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "address required"}
	}
	ip := net.ParseIP(req.Address)
	if ip == nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "address not parseable"}
	}

	isV4 := strings.Contains(req.Address, ".") && !strings.Contains(req.Address, ":")
	prefix := 0
	switch {
	case req.Prefixlen != nil:
		prefix = *req.Prefixlen
	case isV4:
		prefix = 32
	default:
		prefix = 128
	}

	// Resolve the interface: if not supplied, look it up from ip.list —
	// matching the current location of the address. Idempotent if the
	// address isn't found.
	iface := req.Interface
	if iface == "" {
		entries, err := snapshotAddresses(ctx)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.Address == req.Address {
				iface = e.Interface
				break
			}
		}
	}
	if iface == "" {
		// Already absent; treat as success.
		return ipUnbindResp{Unbound: true}, nil
	}

	cidr := fmt.Sprintf("%s/%d", req.Address, prefix)
	stderr, err := runCaptureStderr(execCommandContext(ctx, "ip", "addr", "del", cidr, "dev", iface))
	if err != nil {
		if isAddressMissing(stderr) {
			// Operator-supplied interface didn't actually hold the
			// address — could be DB drift (address rebound manually to
			// another NIC after panel recorded the original binding).
			// Fall back to a snapshot lookup so unbind still converges.
			entries, sErr := snapshotAddresses(ctx)
			if sErr == nil {
				for _, e := range entries {
					if e.Address == req.Address && e.Interface != iface {
						iface = e.Interface
						cidr = fmt.Sprintf("%s/%d", req.Address, prefix)
						stderr, err = runCaptureStderr(execCommandContext(ctx, "ip", "addr", "del", cidr, "dev", iface))
						break
					}
				}
			}
		}
	}
	if err != nil {
		if isAddressMissing(stderr) {
			// Address genuinely absent on every interface — unbind is a
			// no-op, which is the caller's desired end state.
			return ipUnbindResp{Unbound: true}, nil
		}
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("ip addr del %s dev %s: %v (stderr: %s)", cidr, iface, err, stderr),
		}
	}
	return ipUnbindResp{Unbound: true}, nil
}

// isAddressMissing matches every stderr variant Linux/iproute2 returns
// when the address isn't bound where we expect it. Kernel wording has
// drifted across versions (5.10: "Cannot assign requested address";
// 6.x + newer iproute2: "Address not found"; some IPv6 paths return
// "No such device"). Treat all as idempotent success signals.
func isAddressMissing(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "cannot assign requested address") ||
		strings.Contains(s, "address not found") ||
		strings.Contains(s, "cannot find device") ||
		strings.Contains(s, "no such device")
}

func init() {
	Default.Register("ip.unbind", ipUnbindHandler)
}
