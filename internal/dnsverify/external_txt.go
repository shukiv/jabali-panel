package dnsverify

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

// TXTAnswer is one public resolver's answer for a TXT lookup.
type TXTAnswer struct {
	// Resolver is the resolver's address, e.g. "1.1.1.1:53".
	Resolver string
	// Definitive is true when the resolver answered: records, or an
	// authoritative NXDOMAIN / NODATA. False means it could not be reached or
	// failed (timeout, SERVFAIL); nothing is known from it.
	Definitive bool
	// Records are the TXT values (each record's strings joined, as
	// net.Resolver.LookupTXT returns them). Empty on NXDOMAIN / NODATA.
	Records []string
}

// LookupTXTEachExternal asks EVERY public resolver for the TXT records of name
// and returns one answer per resolver, in externalResolvers order. Unlike the
// first-answer-wins helpers, a caller can require several independent
// resolvers to agree: the domain-ownership proof (GH #1816 / ADR-0170) counts
// a name as proven only when two of three return the challenge value, so one
// poisoned or stale resolver cannot prove it alone.
//
// The resolvers are asked in parallel, each over UDP then TCP, so the worst
// case is one resolver's two timeouts. The local recursor is never used: it
// forwards hosted zones to this server's own authoritative server, which is
// exactly the view a claimant must not be able to prove against.
func LookupTXTEachExternal(ctx context.Context, name string) []TXTAnswer {
	out := make([]TXTAnswer, len(externalResolvers))
	var wg sync.WaitGroup
	for i, addr := range externalResolvers {
		wg.Add(1)
		go func(i int, addr string) {
			defer wg.Done()
			out[i] = lookupTXTOne(ctx, addr, name)
		}(i, addr)
	}
	wg.Wait()
	return out
}

func lookupTXTOne(ctx context.Context, addr, name string) TXTAnswer {
	ans := TXTAnswer{Resolver: addr}
	for _, proto := range []string{"udp", "tcp"} {
		r := newDirectResolver(addr, proto)
		lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		recs, err := r.LookupTXT(lookupCtx, name)
		cancel()
		if err == nil {
			ans.Definitive = true
			ans.Records = recs
			return ans
		}
		var derr *net.DNSError
		if errors.As(err, &derr) && derr.IsNotFound {
			// NXDOMAIN or NODATA: the resolver answered, with nothing.
			ans.Definitive = true
			return ans
		}
		// Timeout, SERVFAIL or connection error: try the next transport.
	}
	return ans
}

// LookupTXTOnServer queries the TXT RRset for name against ONE specific DNS
// server (hostname or IP; port 53 is appended). Used to confirm a freshly
// written ACME challenge is actually being served by the zone's OWN
// authoritative nameservers before telling Let's Encrypt to validate — a
// public-resolver lookup would negative-cache the miss and lie for the
// record's whole SOA-min window, and a blind sleep races the provider's
// propagation (Cloudflare API→edge took >10s on a real zone, JAB-235).
//
// The server hostname itself resolves through the system resolver — it is a
// public name (e.g. chad.ns.cloudflare.com), not a locally-hosted zone, so
// the local-recursor hazard that motivates the rest of this package does not
// apply to it.
func LookupTXTOnServer(ctx context.Context, server, name string) ([]string, error) {
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			d := &net.Dialer{Timeout: 3 * time.Second}
			return d.DialContext(ctx, "udp", net.JoinHostPort(server, "53"))
		},
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return r.LookupTXT(lookupCtx, name)
}
