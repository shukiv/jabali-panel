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
	if ip.IsUnspecified() {
		return errors.New("0.0.0.0 / :: rejected")
	}
	if ip.IsMulticast() {
		return errors.New("multicast rejected")
	}
	if !allowPrivate && ip.IsPrivate() {
		return errors.New("private (RFC1918 / ULA) address rejected")
	}
	return nil
}
