package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// ipBindReq — the primary input is the address. Prefixlen is optional;
// when omitted the agent inherits the selected interface's existing
// primary prefix (typical hosting panel behaviour — secondary IP shares
// the primary's subnet so `ifconfig` renders it and arping works). When
// no existing address of the right family is present on the interface,
// the fallback is /32 for v4 / /128 for v6. Interface is also optional;
// when omitted the agent picks the interface that owns the default
// route — the safe choice when the host has multiple NICs.
type ipBindReq struct {
	Address   string `json:"address"`
	Prefixlen *int   `json:"prefixlen,omitempty"`
	Interface string `json:"interface,omitempty"`
}

// ipBindResp carries both the bind result and the post-bind probe result.
// Reachable=false with Bound=true means the kernel accepted the IP but
// something (firewall, ip_forward disabled, upstream routing) prevents
// local TCP connect via the new address — usually a misconfigured host
// firewall. Caller surfaces this as `degraded=true` in the DB without
// unbinding.
type ipBindResp struct {
	Bound          bool     `json:"bound"`
	Reachable      bool     `json:"reachable"`
	Interface      string   `json:"interface"`
	SuspectedCause string   `json:"suspected_cause,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
}

// ipBindHandler validates, selects the interface, runs `ip addr add`,
// then probes reachability. On any exec failure we surface stderr in
// the wire error so the operator can diagnose from panel-ui.
func ipBindHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req ipBindReq
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

	// Family-derived prefix default; also validates a caller-supplied
	// prefix stays inside the per-family range. Prefix defaulting is
	// deferred until after we've chosen the interface below, so we can
	// inherit the interface's existing primary prefix.
	isV4 := strings.Contains(req.Address, ".") && !strings.Contains(req.Address, ":")
	if req.Prefixlen != nil {
		p := *req.Prefixlen
		if isV4 && (p < 1 || p > 32) {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "prefixlen out of IPv4 range"}
		}
		if !isV4 && (p < 1 || p > 128) {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "prefixlen out of IPv6 range"}
		}
	}

	// Preflight: if the address is already on any interface, short-circuit
	// as "already bound" — idempotent. `ip.list` parse errors propagate
	// as retryable (don't swallow).
	entries, listErr := snapshotAddresses(ctx)
	if listErr != nil {
		return nil, listErr
	}
	for _, e := range entries {
		if e.Address == req.Address {
			return ipBindResp{
				Bound:     true,
				Reachable: true, // pre-existing; assume operator verified
				Interface: e.Interface,
			}, nil
		}
	}

	iface := req.Interface
	if iface == "" {
		var derr error
		iface, derr = defaultRouteInterface(ctx, isV4)
		if derr != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("could not derive target interface from default route: %v — set interface explicitly or fix routing", derr),
			}
		}
	}

	// Never touch loopback (would hide public traffic behind lo) or
	// interfaces that don't exist.
	if iface == "lo" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "refusing to bind on loopback interface",
		}
	}
	nic, err := net.InterfaceByName(iface)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("interface %q does not exist: %v", iface, err),
		}
	}

	// Resolve the final prefix: caller-supplied wins; otherwise inherit
	// the interface's primary prefix (so secondary IPs share the
	// primary's subnet — cPanel/Plesk convention). Fall back to /32 or
	// /128 when the interface has no addresses of the right family.
	prefix := 0
	if req.Prefixlen != nil {
		prefix = *req.Prefixlen
	} else {
		prefix = interfacePrimaryPrefix(nic, isV4)
	}

	cidr := fmt.Sprintf("%s/%d", req.Address, prefix)
	cmd := execCommandContext(ctx, "ip", "addr", "add", cidr, "dev", iface)
	stderr, err := runCaptureStderr(cmd)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("ip addr add %s dev %s: %v (stderr: %s)", cidr, iface, err, stderr),
		}
	}

	resp := ipBindResp{
		Bound:     true,
		Interface: iface,
	}

	// Post-bind connectivity probe: open a listener on the new IP:0,
	// dial back to it within 500ms. Success means the kernel routes
	// local→local traffic via the new address; failure is typically
	// a firewall DROP or conntrack issue. Either way, we report the
	// probe result instead of unbinding — the operator can still use
	// the IP for outbound while they diagnose inbound.
	reachable, probeErr := probeLocalReachability(req.Address)
	resp.Reachable = reachable
	if !reachable {
		resp.SuspectedCause = "firewall or ip_forward"
		msg := "connectivity probe failed; verify host firewall allows inbound to this address"
		if probeErr != nil {
			msg = msg + " (" + probeErr.Error() + ")"
		}
		resp.Warnings = append(resp.Warnings, msg)
	}

	return resp, nil
}

// snapshotAddresses reuses ip.list by calling its handler internally.
// Surface errors as AgentError so the caller gets structured output.
func snapshotAddresses(ctx context.Context) ([]ipListEntry, *agentwire.AgentError) {
	data, err := ipListHandler(ctx, nil)
	if err != nil {
		var ae *agentwire.AgentError
		if errors.As(err, &ae) {
			return nil, ae
		}
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: err.Error()}
	}
	resp, ok := data.(ipListResp)
	if !ok {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "ip.list returned unexpected type"}
	}
	return resp.Entries, nil
}

// defaultRouteInterface parses `ip -j route` output to find the dev that
// owns the default route. When isV4 is true we look at the IPv4 table;
// otherwise the IPv6 table. Returns an error when no default route is
// configured — the operator must either pick an interface explicitly
// or fix networking first.
func defaultRouteInterface(ctx context.Context, isV4 bool) (string, error) {
	args := []string{"-j", "route", "show", "default"}
	if !isV4 {
		args = []string{"-j", "-6", "route", "show", "default"}
	}
	out, err := execCommandContext(ctx, "ip", args...).Output()
	if err != nil {
		return "", fmt.Errorf("ip %v: %w", args, err)
	}
	var routes []struct {
		Dst string `json:"dst"`
		Dev string `json:"dev"`
	}
	if jerr := json.Unmarshal(out, &routes); jerr != nil {
		return "", fmt.Errorf("parse route: %w", jerr)
	}
	for _, r := range routes {
		if (r.Dst == "default" || r.Dst == "0.0.0.0/0" || r.Dst == "::/0") && r.Dev != "" {
			return r.Dev, nil
		}
	}
	return "", fmt.Errorf("no default route found")
}

// probeLocalReachability opens a TCP listener on addr:0, dials back to
// the listener's local address, accepts once, then closes. A 500ms dial
// timeout keeps us responsive under firewall drops. Errors propagate
// back to the caller for diagnostics.
func probeLocalReachability(addr string) (bool, error) {
	listenAddr := net.JoinHostPort(addr, "0")
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return false, fmt.Errorf("listen on %s: %w", listenAddr, err)
	}
	defer ln.Close()

	accepted := make(chan error, 1)
	go func() {
		conn, aerr := ln.Accept()
		if conn != nil {
			conn.Close()
		}
		accepted <- aerr
	}()

	dialer := &net.Dialer{Timeout: 500 * time.Millisecond}
	conn, err := dialer.Dial("tcp", ln.Addr().String())
	if err != nil {
		return false, fmt.Errorf("dial %s: %w", ln.Addr(), err)
	}
	conn.Close()

	select {
	case aerr := <-accepted:
		if aerr != nil {
			return false, fmt.Errorf("accept: %w", aerr)
		}
		return true, nil
	case <-time.After(500 * time.Millisecond):
		return false, fmt.Errorf("accept timed out")
	}
}

// runCaptureStderr is a small wrapper that surfaces stderr as a string
// when the command fails, so error messages don't lose context.
func runCaptureStderr(cmd *exec.Cmd) (string, error) {
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	return strings.TrimSpace(stderr.String()), err
}

// interfacePrimaryPrefix returns the prefix length of the first address
// of the requested family already bound to the interface. Fallback is
// /32 for v4, /128 for v6 when the interface has no such address — keeps
// the function total and never errors callers who want a "best effort"
// default.
func interfacePrimaryPrefix(nic *net.Interface, isV4 bool) int {
	addrs, err := nic.Addrs()
	if err != nil {
		if isV4 {
			return 32
		}
		return 128
	}
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || ipNet.IP == nil {
			continue
		}
		is4 := ipNet.IP.To4() != nil
		if is4 != isV4 {
			continue
		}
		ones, _ := ipNet.Mask.Size()
		if ones > 0 {
			return ones
		}
	}
	if isV4 {
		return 32
	}
	return 128
}

func init() {
	Default.Register("ip.bind", ipBindHandler)
}
