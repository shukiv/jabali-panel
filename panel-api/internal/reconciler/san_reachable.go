package reconciler

import (
	"context"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/dnsverify"
)

// san_reachable.go — JAB-226.
//
// resolvableSANs keeps any SAN that resolves in public DNS AT ALL. That was the
// right fix for its own incident (SANs with no record anywhere tanked the cert),
// but it is not sufficient: a name can resolve perfectly and still point
// somewhere else.
//
// That is the normal state of a migration. Until the source nameservers stop
// being authoritative, mail./autoconfig./autodiscover.<domain> still answer with
// the OLD box's address. They resolve, so they survive the filter, certbot asks
// Let's Encrypt to validate them, the challenge lands on the old server and
// 404s — and one failed name fails the WHOLE certificate, apex included. The
// domain stays on a self-signed origin, which behind Cloudflare Full (strict) is
// a 526. Observed on newhighsite: "Requesting a certificate for … and 3 more
// domains" -> autoconfig./autodiscover./mail. 404 -> "Some challenges failed".
//
// So the question is not "does this name resolve" but "will a challenge for it
// reach US".

// sanReachability decides which SANs can plausibly pass HTTP-01 against this
// server.
//
// A SAN is kept when either:
//
//   - it resolves to one of this server's public addresses — the direct case; or
//   - it resolves to the same address(es) as the apex — the fronted case. When a
//     domain sits behind a CDN or proxy, NOTHING resolves to us directly, yet
//     HTTP-01 still succeeds because the proxy forwards the challenge. Requiring
//     an exact match with our IP would drop every SAN on every proxied domain
//     and break setups that work today.
//
// Dropping only the names that resolve somewhere OTHER than wherever the apex
// lives is what separates "not cut over yet" from "behind Cloudflare".
//
// apexAddrs or ourAddrs being empty means we cannot make the judgement, so
// everything is kept — this filter narrows, it never invents a reason to fail.
func sanReachability(sanAddrs, apexAddrs, ourAddrs []string) bool {
	if len(sanAddrs) == 0 {
		return false // no DNS at all: the pre-existing resolvableSANs rule
	}
	if len(apexAddrs) == 0 && len(ourAddrs) == 0 {
		return true // nothing to compare against — do not guess
	}
	if intersects(sanAddrs, ourAddrs) {
		return true
	}
	if intersects(sanAddrs, apexAddrs) {
		return true
	}
	return false
}

func intersects(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	set := make(map[string]struct{}, len(b))
	for _, x := range b {
		set[x] = struct{}{}
	}
	for _, x := range a {
		if _, ok := set[x]; ok {
			return true
		}
	}
	return false
}

// reachableSANs filters names to those a Let's Encrypt HTTP-01 challenge could
// actually reach on this server. Layered on top of resolvableSANs' "must resolve
// at all" rule rather than replacing it.
//
// Best-effort by construction: when the apex cannot be resolved and this
// server's public address is unknown, every name is kept and the behaviour is
// exactly what it was before.
func (r *Reconciler) reachableSANs(ctx context.Context, apex string, names []string) []string {
	if len(names) == 0 {
		return nil
	}

	var ourAddrs []string
	if r.serverSettings != nil {
		if srv, err := r.serverSettings.Get(ctx); err == nil && srv != nil {
			if srv.PublicIPv4 != "" {
				ourAddrs = append(ourAddrs, srv.PublicIPv4)
			}
			if srv.PublicIPv6 != "" {
				ourAddrs = append(ourAddrs, srv.PublicIPv6)
			}
		}
	}

	apexCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	apexAddrs := dnsverify.LookupHostExternal(apexCtx, apex)
	cancel()

	if len(apexAddrs) == 0 && len(ourAddrs) == 0 {
		return names // cannot judge — unchanged behaviour
	}

	type result struct {
		name string
		keep bool
	}
	resCh := make(chan result, len(names))
	for _, n := range names {
		go func(name string) {
			lookupCtx, c := context.WithTimeout(ctx, 6*time.Second)
			defer c()
			addrs := dnsverify.LookupHostExternal(lookupCtx, name)
			resCh <- result{name: name, keep: sanReachability(addrs, apexAddrs, ourAddrs)}
		}(n)
	}

	keep := make([]string, 0, len(names))
	for i := 0; i < len(names); i++ {
		res := <-resCh
		if res.keep {
			keep = append(keep, res.name)
			continue
		}
		r.log.Warn("ssl: skipping SAN — it resolves somewhere other than this server or the apex, "+
			"so an HTTP-01 challenge for it would land elsewhere and fail the whole certificate",
			"san", res.name, "apex", apex)
	}
	return keep
}
