package dnsverify

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"
)

// LookupNSExternal resolves the NS RRset for name against the external
// resolvers — same rationale as LookupHostExternal: the local recursor
// forwards to this box's own authoritative server, which claims every
// locally-hosted zone regardless of where the public delegation actually
// points, so only a public resolver can tell us who is REALLY authoritative
// (JAB-235: a migrated domain keeps a stale local zone row while its NS
// lives at Cloudflare).
//
// Hosts are returned lowercased without the trailing dot. queried=false
// means every resolver was unreachable — the answer is inconclusive and
// callers must fail open (behave as before), never treat it as "no
// delegation". A definitive empty answer (NODATA/NXDOMAIN — normal for any
// label below a zone apex) returns (nil, true).
func LookupNSExternal(ctx context.Context, name string) (hosts []string, queried bool) {
	for _, addr := range externalResolvers {
		for _, proto := range []string{"udp", "tcp"} {
			r := newDirectResolver(addr, proto)
			lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			nss, err := r.LookupNS(lookupCtx, name)
			cancel()
			if err == nil {
				out := make([]string, 0, len(nss))
				for _, ns := range nss {
					out = append(out, strings.ToLower(strings.TrimSuffix(ns.Host, ".")))
				}
				return out, true
			}
			var derr *net.DNSError
			if errors.As(err, &derr) && derr.IsNotFound {
				return nil, true
			}
			// Unreachable resolver/transport — try the next one.
		}
	}
	return nil, false
}
