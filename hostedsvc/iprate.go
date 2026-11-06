package hostedsvc

import (
	"net"
	"sync"
	"time"
)

// POST /v1/register is unauthenticated, internet-facing, and causes the
// service to SEND MAIL through its own authenticated, reputation-clean relay.
// The only throttle was a 60s resend gap keyed on the EMAIL, which stops
// nothing that matters:
//
//   - target one victim address forever (a verification mail every 60s, from a
//     relay they can't easily block — harassment the service is the vector for)
//   - fan out across millions of distinct addresses, using the service as a
//     spam relay and burning its sending reputation until the provider acts
//
// Both need a per-SOURCE-IP bound, which is what this adds. The key is
// ClientIP(r) — the same address the label derivation trusts, established by
// the TCP handshake and (behind Cloudflare) rewritten at the edge, so it is
// not client-forgeable the way an X-Forwarded-For header would be.

// registerIPHourlyCap is the number of /v1/register calls allowed from one
// source address per hour. A real operator registers once, occasionally twice
// after a typo; NAT'd offices share an address, so the cap is well above one
// person's need while still bounding a bulk sender to a rounding error.
const registerIPHourlyCap = 10

// registerIPWindow is the sliding window the cap applies over.
const registerIPWindow = time.Hour

// POST /v1/claim checks a code. The attempt cap stops guessing one code;
// these bound the tries one source address makes in an hour, and the wrong
// codes one email address gets in an hour across all the codes it is sent.
// Only a wrong code against a live code counts for the email, so an address
// with no code pending is never held back. A real operator claims once or
// twice.
const (
	claimIPHourlyCap    = 30
	claimEmailHourlyCap = 20
)

// rateKey is the limiter key for a source address: an IPv4 address as is,
// an IPv6 address by its /64, since one host is commonly given a whole /64.
func rateKey(ip net.IP) string {
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// ipRateLimiter is a small sliding-window counter keyed by source IP.
//
// Entries are pruned opportunistically on every call, so the map cannot grow
// without bound from a rotating source — the failure mode that made the
// support-claim limiter itself a memory-exhaustion vector.
type ipRateLimiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
	now  func() time.Time
	cap  int
	win  time.Duration
}

func newIPRateLimiter(capacity int, window time.Duration) *ipRateLimiter {
	return &ipRateLimiter{
		hits: map[string][]time.Time{},
		now:  time.Now,
		cap:  capacity,
		win:  window,
	}
}

// allow records a hit for ip and reports whether it is within the cap.
func (l *ipRateLimiter) allow(ip string) bool {
	if ip == "" {
		// No usable source address: don't hand out a free pass, but don't
		// wedge the service either — the caller still has the per-email gap.
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.prune()
	if len(l.hits[ip]) >= l.cap {
		return false
	}
	l.hits[ip] = append(l.hits[ip], now)
	return true
}

// full reports whether key is at the cap, without counting a hit.
func (l *ipRateLimiter) full(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune()
	return len(l.hits[key]) >= l.cap
}

// hit counts one hit for key.
func (l *ipRateLimiter) hit(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.prune()
	l.hits[key] = append(l.hits[key], now)
}

// prune drops hits older than the window and returns now. Caller holds mu.
func (l *ipRateLimiter) prune() time.Time {
	now := l.now()
	cutoff := now.Add(-l.win)

	// Prune every key, not just this one: a rotating source would otherwise
	// leave a permanent entry per address it ever used.
	for k, ts := range l.hits {
		kept := ts[:0]
		for _, t := range ts {
			if t.After(cutoff) {
				kept = append(kept, t)
			}
		}
		if len(kept) == 0 {
			delete(l.hits, k)
			continue
		}
		l.hits[k] = kept
	}
	return now
}
