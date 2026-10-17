// Package dnsverify provides authoritative-resolution helpers that
// bypass /etc/hosts AND the host's local recursor. We need this in two
// places that have the same hazard: the panel-cert routability gate
// (would the LE validator see what the panel sees?) and the per-tenant
// SAN filter (would the LE validator resolve `mail.<tenant>` if we
// asked it to?).
//
// On a typical jabali host /etc/resolv.conf points at the local
// pdns-recursor which has pdns-auth as its forward path. The auth
// server is authoritative for every domain hosted on this server,
// including tenant zones — so a local lookup of `mail.<tenant>`
// succeeds even when the tenant delegates DNS to a foreign registrar
// and that registrar has no `mail` record. Let's Encrypt queries the
// real authoritative nameservers, gets NXDOMAIN, and the challenge
// fails with acme:error:dns. The fix is to ask a public resolver
// directly so we see the world's view.
//
// Resolver list: Cloudflare, Quad9, Google. Each tried over UDP first
// then TCP so carriers that block outbound UDP/53 (some LXC providers
// do) still succeed.
//
// Address lookups send raw A/AAAA queries (external_raw.go): a Go resolver
// answers a name in /etc/hosts from the file even with a custom Dial, so it
// can't show the world's view of the panel hostname or mail.<hostname>,
// which install.sh and Debian put in /etc/hosts (GH #2166).
package dnsverify

import (
	"context"
	"errors"
	"net"
	"time"
)

// externalResolvers is the ordered list of public resolvers the
// shadow-guard retries against. Cloudflare, Quad9, Google.
var externalResolvers = []string{"1.1.1.1:53", "9.9.9.9:53", "8.8.8.8:53"}

// LookupHostExternal walks externalResolvers (UDP then TCP per host)
// and returns the first non-empty LookupHost result. Returns nil if
// every attempt fails. Each attempt has a tight per-resolver timeout
// so a single unreachable resolver can't stall a reconciler tick.
func LookupHostExternal(ctx context.Context, host string) []string {
	addrs, _ := LookupHostExternalResult(ctx, host)
	return addrs
}

// LookupHostExternalResult is like LookupHostExternal but also reports
// whether any external resolver gave a DEFINITIVE answer — records, or an
// authoritative NXDOMAIN / NODATA. queried=false means every resolver was
// unreachable (timeout / blocked outbound :53): the result is INCONCLUSIVE
// and callers must NOT treat an empty addrs as "the world can't see this
// name". This lets the panel-cert gate distinguish "public DNS has no
// record" (park with a clear reason) from "we couldn't reach public DNS"
// (fall back to the local view rather than wrongly blocking issuance).
func LookupHostExternalResult(ctx context.Context, host string) (addrs []string, queried bool) {
	for _, addr := range externalResolvers {
		for _, proto := range []string{"udp", "tcp"} {
			lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			out, err := lookupHostDirect(lookupCtx, addr, proto, host)
			cancel()
			if err == nil {
				if len(out) > 0 {
					return out, true
				}
				// Resolver answered with no addresses (NODATA) — the
				// name exists but has no A/AAAA. Definitive empty.
				return nil, true
			}
			var derr *net.DNSError
			if errors.As(err, &derr) && derr.IsNotFound {
				// Authoritative NXDOMAIN from a public resolver — the
				// world genuinely has no record for this name.
				return nil, true
			}
			// Timeout / connection error: this resolver is unreachable,
			// try the next transport / resolver before giving up.
		}
	}
	return nil, false
}

// newDirectResolver returns a pure-Go resolver that sends its queries to
// the given DNS server:port. It is for NS and TXT lookups only, which never
// read /etc/hosts: for a name in /etc/hosts, LookupHost on it answers from
// the file without asking the server (use lookupHostDirect).
func newDirectResolver(serverAddr, proto string) *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			d := &net.Dialer{Timeout: 3 * time.Second}
			return d.DialContext(ctx, proto, serverAddr)
		},
	}
}
