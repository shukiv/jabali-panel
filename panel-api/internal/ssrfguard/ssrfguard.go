// Package ssrfguard is a zero-dependency leaf providing SSRF-safe outbound
// dialing: resolve a hostname, reject any IP in a blocked range, then pin the
// TCP connection to a validated IP so a DNS rebind between resolve and dial is
// refused at connect time.
//
// It is used by the notification senders (internal/notifications/senders) to
// guard tenant-supplied channel URLs (ntfy, discord, webhook) against pointing
// at loopback / link-local (cloud metadata) / private space. The senders select
// a guarded client for tenant-owned channels (channel.UserID != nil) and their
// normal client for server-wide/admin channels, which an operator may
// legitimately point at an internal host — so the guard is fail-closed per
// channel, independent of the call site.
//
// The IP-classification + rebind-pin logic mirrors the migration SSRF guard
// (internal/migrate/ssrf.go, ADR-0095 decision 8). It is duplicated here rather
// than shared so this leaf stays dependency-free and the proven migration path
// is untouched; the two should be unified into this package in a later cleanup.
package ssrfguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"
)

// DefaultDialTimeout bounds one guarded dial (DNS + handshake).
const DefaultDialTimeout = 10 * time.Second

// cgnatNet is carrier-grade NAT space (RFC 6598). Go's net.IP.IsPrivate covers
// RFC1918 and ULA but NOT 100.64.0.0/10, and some VPS/cloud providers put
// internal service endpoints there — so without this a tenant-supplied channel
// URL could still reach infrastructure the guard exists to exclude. Gated on the
// same allowPrivate flag as RFC1918 so the admin-controlled
// migration_allow_private_hosts override keeps working for a CGNAT-hosted source.
var cgnatNet = net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// thisNetworkNets are never a peer to connect to, whatever allowPrivate says:
// "this network" (0.0.0.0/8) and IPv4-compatible IPv6 (::/96, deprecated).
// net.IP.IsUnspecified covers only 0.0.0.0 and :: themselves.
var thisNetworkNets = mustCIDRs("0.0.0.0/8", "::/96")

// nonPublicNets are not public unicast space but escape net.IP's own tests:
// benchmarking (198.18.0.0/15), reserved (240.0.0.0/4, broadcast included),
// IPv6 site-local (fec0::/10, deprecated) and the local-use NAT64 prefix
// (64:ff9b:1::/48, RFC 8215). Gated on allowPrivate, as CGNAT is.
var nonPublicNets = mustCIDRs("198.18.0.0/15", "240.0.0.0/4", "fec0::/10", "64:ff9b:1::/48")

// nat64Net (RFC 6052, the IPv4 address in the last 32 bits) and sixToFourNet
// (2002::/16, the IPv4 address in bits 16-48) carry an IPv4 address that a
// gateway connects to, so that address is checked as well.
var (
	nat64Net     = mustCIDRs("64:ff9b::/96")[0]
	sixToFourNet = mustCIDRs("2002::/16")[0]
)

func mustCIDRs(cidrs ...string) []*net.IPNet {
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic(err)
		}
		nets = append(nets, n)
	}
	return nets
}

func inAny(nets []*net.IPNet, ip net.IP) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Validator captures the AllowPrivate toggle and the resolved IPs for one host.
type Validator struct {
	Hostname     string
	AllowPrivate bool
	resolved     []net.IP
}

// ValidateHost resolves hostname to A/AAAA and rejects if any resolved IP is in
// a blocked range. The returned Validator must be used via Dialer()+DialAddr()
// for the connection so the peer IP is re-checked at connect time.
func ValidateHost(ctx context.Context, hostname string, allowPrivate bool) (*Validator, error) {
	if hostname == "" {
		return nil, errors.New("ssrf: empty hostname")
	}
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
	return &Validator{Hostname: hostname, AllowPrivate: allowPrivate, resolved: ips}, nil
}

// DialAddr returns "ip:port" for a validated resolved IP (IPv4 preferred) so the
// dial connects to an already-checked address instead of re-resolving.
func (v *Validator) DialAddr(port string) string {
	ip := v.resolved[0]
	for _, cand := range v.resolved {
		if cand.To4() != nil {
			ip = cand
			break
		}
	}
	return net.JoinHostPort(ip.String(), port)
}

// Dialer returns a net.Dialer whose Control hook re-checks the peer IP the
// kernel is about to connect to, refusing a DNS-rebound IP outside the resolved
// set before the TCP handshake.
func (v *Validator) Dialer() *net.Dialer {
	allowed := make(map[string]struct{}, len(v.resolved))
	for _, ip := range v.resolved {
		allowed[ip.String()] = struct{}{}
	}
	allow := v.AllowPrivate
	return &net.Dialer{
		Timeout: DefaultDialTimeout,
		Control: func(_, address string, _ syscall.RawConn) error {
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

// GuardedDialContext is a net/http Transport.DialContext that validates and
// pins every outbound connection, always with private ranges blocked. It is the
// dial hook for the tenant-facing HTTP client; plug it into
// http.Transport.DialContext.
func GuardedDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("ssrf: split %q: %w", addr, err)
	}
	v, err := ValidateHost(ctx, host, false)
	if err != nil {
		return nil, err
	}
	return v.Dialer().DialContext(ctx, network, v.DialAddr(port))
}

func checkIP(ip net.IP, allowPrivate bool) error {
	if ip == nil {
		return errors.New("nil IP")
	}
	if ip.IsLoopback() {
		return errors.New("loopback rejected")
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return errors.New("link-local rejected (cloud metadata blocked)")
	}
	if ip.IsUnspecified() || inAny(thisNetworkNets, ip) {
		return errors.New("0.0.0.0/8 / ::/96 rejected")
	}
	if ip.IsMulticast() {
		return errors.New("multicast rejected")
	}
	if !allowPrivate && ip.IsPrivate() {
		return errors.New("private (RFC1918 / ULA) address rejected")
	}
	if !allowPrivate && cgnatNet.Contains(ip) {
		return errors.New("CGNAT (RFC 6598) address rejected")
	}
	if !allowPrivate && inAny(nonPublicNets, ip) {
		return errors.New("non-public (benchmarking / reserved / site-local / local-use NAT64) address rejected")
	}
	if ip16 := ip.To16(); ip.To4() == nil && ip16 != nil {
		var inner net.IP
		switch {
		case nat64Net.Contains(ip):
			inner = net.IP(ip16[12:16])
		case sixToFourNet.Contains(ip):
			inner = net.IP(ip16[2:6])
		}
		if inner != nil {
			if err := checkIP(inner, allowPrivate); err != nil {
				return fmt.Errorf("IPv4 inside %s: %w", ip, err)
			}
		}
	}
	return nil
}
