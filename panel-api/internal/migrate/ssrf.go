package migrate

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"syscall"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssrfguard"
)

// ADR-0095 decision 8: SSRF protection for migration outbound dials.
//
// Every dial from internal/migrate/ (discover-accounts, secrets push,
// pull-source) must call ValidateHost first. The returned Dialer rejects
// at connect-time too — defeats DNS-rebinding attacks where a hostname
// resolves to an allowed public IP at validation and to an internal IP
// when Dial actually fires.
//
// Addresses are classified by ssrfguard.CheckIP, the same rules as every
// other outbound connection a tenant can point somewhere.
//
// Default-deny ranges (always blocked regardless of operator override):
//   - 127.0.0.0/8 loopback, ::1/128 IPv6 loopback
//   - 169.254.0.0/16 link-local + cloud metadata, fe80::/10 IPv6 link-local
//   - 0.0.0.0/8 "this network", ::/96 IPv4-compatible IPv6, multicast
//
// Soft-deny ranges (blocked unless AllowPrivate=true):
//   - 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16 RFC1918, fc00::/7 IPv6 ULA
//   - 100.64.0.0/10 CGNAT (Tailscale addresses among them)
//   - 198.18.0.0/15, 240.0.0.0/4, fec0::/10, 64:ff9b:1::/48
//
// A NAT64 (64:ff9b::/96) or 6to4 (2002::/16) address is held to the rule of
// the IPv4 address it carries.

// HostValidator captures the AllowPrivate toggle and the resolved set
// of IPs for one hostname. The dialer reuses these to assert peer-IP
// at connect time.
type HostValidator struct {
	Hostname     string
	AllowPrivate bool
	resolved     []net.IP
}

// ValidateHost resolves hostname to A/AAAA records and rejects if any
// resolved IP falls in a blocked range. Returns a validator that must
// be passed to Dialer() for the eventual TCP connection.
func ValidateHost(ctx context.Context, hostname string, allowPrivate bool) (*HostValidator, error) {
	if strings.TrimSpace(hostname) == "" {
		return nil, errors.New("ssrf: empty hostname")
	}
	// Resolve via context-aware resolver so the operator-level dial
	// timeout (typically 5–10s) covers DNS too.
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", hostname)
	if err != nil {
		return nil, fmt.Errorf("ssrf: resolve %s: %w", hostname, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("ssrf: %s resolved to zero records", hostname)
	}
	for _, ip := range ips {
		if err := checkIP(ip, allowPrivate); err != nil {
			return nil, fmt.Errorf("ssrf: %s → %s: %w", hostname, ip.String(), err)
		}
	}
	return &HostValidator{
		Hostname:     hostname,
		AllowPrivate: allowPrivate,
		resolved:     ips,
	}, nil
}

// DialAddr returns "ip:port" for a VALIDATED resolved IP (IPv4 preferred), so
// callers connect to an already-checked address instead of re-resolving the
// hostname at dial time. A second resolution can disagree with the validated
// set on some hosts' resolvers (systemd-resolved caching / ordering) and trip
// the rebind guard on a perfectly stable public IP (GH #671). The Control hook
// still fires as belt-and-suspenders.
func (v *HostValidator) DialAddr(port string) string {
	ip := v.resolved[0]
	for _, cand := range v.resolved {
		if cand.To4() != nil {
			ip = cand
			break
		}
	}
	return net.JoinHostPort(ip.String(), port)
}

// Dialer returns a net.Dialer whose Control hook re-checks the peer IP
// the kernel is about to connect to. If a DNS rebinding attack flipped
// the record between ValidateHost() and Dial(), the new IP fails the
// allow-list and the connection is refused before the TCP handshake.
func (v *HostValidator) Dialer() *net.Dialer {
	allowed := make(map[string]struct{}, len(v.resolved))
	for _, ip := range v.resolved {
		allowed[ip.String()] = struct{}{}
	}
	allow := v.AllowPrivate
	return &net.Dialer{
		Control: func(network, address string, _ syscall.RawConn) error {
			// address is "ip:port" — extract the IP.
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("ssrf: split %q: %w", address, err)
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return fmt.Errorf("ssrf: unparseable peer IP %q", host)
			}
			if _, ok := allowed[ip.String()]; !ok {
				return fmt.Errorf("ssrf: dial peer %s not in resolved set (DNS rebinding suspected)", ip)
			}
			if err := checkIP(ip, allow); err != nil {
				return fmt.Errorf("ssrf: dial-time check %s: %w", ip, err)
			}
			return nil
		},
	}
}

// DialTCP is the safe outbound entrypoint for every migrate/* package.
// Combines ValidateHost (resolve + range check) with Dialer (peer-IP
// re-check at connect time) in one call. Callers pass the operator
// override flag pulled from server_settings.migration_allow_private_hosts.
//
// Returns a net.Conn ready for ssh.NewClientConn or any other higher-
// level handshake. Caller owns the conn lifetime.
func DialTCP(ctx context.Context, host string, port int, allowPrivate bool, timeout time.Duration) (net.Conn, error) {
	v, err := ValidateHost(ctx, host, allowPrivate)
	if err != nil {
		return nil, err
	}
	d := v.Dialer()
	d.Timeout = timeout
	return d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
}

// checkIP applies ssrfguard.CheckIP. An address the override would admit
// says how to admit it.
func checkIP(ip net.IP, allowPrivate bool) error {
	err := ssrfguard.CheckIP(ip, allowPrivate)
	if err != nil && !allowPrivate && ssrfguard.CheckIP(ip, true) == nil {
		return fmt.Errorf("%w; flip migration_allow_private_hosts to override", err)
	}
	return err
}
