package hostedsvc

import (
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

	if len(l.hits[ip]) >= l.cap {
		return false
	}
	l.hits[ip] = append(l.hits[ip], now)
	return true
}
