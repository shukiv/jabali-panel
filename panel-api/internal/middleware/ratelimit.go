package middleware

import (
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
)

// RateLimiterConfig describes two token-bucket tiers. Default applies to
// most routes; Strict is used for credential-sensitive endpoints (/auth/*).
//
// If Strict{Rate,Burst} are zero, calls to Strict() fall back to the
// default bucket so the caller doesn't have to guard against unconfigured
// tiers.
type RateLimiterConfig struct {
	DefaultRate  rate.Limit
	DefaultBurst int
	StrictRate   rate.Limit
	StrictBurst  int
	// KratosRate/KratosBurst gate the /.ory self-service credential POSTs
	// (login, recovery). Separate from Strict so a burst of authenticated
	// /auth/* strict-tier traffic can't starve the login bucket and vice versa.
	KratosRate  rate.Limit
	KratosBurst int
}

// RateLimiter holds per-IP, per-tier token buckets and returns middleware
// functions that consume from them. Buckets are created lazily and can be
// reaped by Cleanup (intended to run periodically from a background goroutine).
type RateLimiter struct {
	cfg RateLimiterConfig

	mu      sync.Mutex
	buckets map[bucketKey]*entry
}

type bucketKey struct {
	ip   string
	tier tier
}

type tier int

const (
	tierDefault tier = iota
	tierStrict
	tierKratos
)

type entry struct {
	limiter *rate.Limiter
	seen    time.Time
}

// NewRateLimiter returns a ready limiter. Callers typically call this once
// at boot and reuse the same limiter across middleware registrations.
func NewRateLimiter(cfg RateLimiterConfig) *RateLimiter {
	return &RateLimiter{cfg: cfg, buckets: map[bucketKey]*entry{}}
}

// Default returns a middleware enforcing the default-tier bucket per client IP.
func (l *RateLimiter) Default() gin.HandlerFunc { return l.handler(tierDefault) }

// Strict returns a middleware enforcing the strict-tier bucket per client IP.
// Applied to expensive/sensitive POST endpoints (e.g. user re-provision) to
// bound replay or burst-retry patterns. Kratos owns its own credential-endpoint
// rate limiting; /.ory/* is not fronted by this middleware.
func (l *RateLimiter) Strict() gin.HandlerFunc { return l.handler(tierStrict) }

// StrictPerActor enforces the strict tier keyed by the AUTHENTICATED user
// rather than the client IP. Some strict-tier routes trigger expensive host
// side effects (JAB-266: an FTP-account PATCH rewrites and reloads global
// sshd), and a per-IP budget multiplies under one session — an actor with N
// addresses gets N×the budget. Keying on the user id bounds the actor
// regardless of source IP. Falls back to the client IP when the request is
// unauthenticated (no claims), so the middleware is still safe if mounted
// before auth.
func (l *RateLimiter) StrictPerActor() gin.HandlerFunc {
	return func(c *gin.Context) {
		key := clientIP(c)
		if claims := ginctx.Claims(c); claims != nil && claims.UserID != "" {
			key = "user:" + claims.UserID
		}
		l.enforce(c, key, tierStrict)
	}
}

// KratosFlows rate-limits the unauthenticated credential POSTs proxied at
// /.ory (self-service login + recovery) per client IP, BEFORE they reach Kratos.
// Only POST submissions to those two flows are gated — flow-init GETs, CSRF
// token fetches, settings, and logout pass through untouched so legitimate
// login/recovery still works. Login and recovery share the tierKratos bucket
// but are logged distinctly. On a limit hit it emits a distinctive WARN line
// (marker=kratos_flow_rate_limited) carrying the same clientIP() the audit log
// + CrowdSec use, so a journalctl-sourced CrowdSec scenario can surface the
// attack in Security → CrowdSec.
func (l *RateLimiter) KratosFlows(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPost {
			c.Next()
			return
		}
		p := c.Request.URL.Path
		var flow string
		switch {
		case strings.Contains(p, "/self-service/login"):
			flow = "login"
		case strings.Contains(p, "/self-service/recovery"):
			flow = "recovery"
		default:
			c.Next()
			return
		}
		ip := clientIP(c)
		if !l.limiterFor(ip, tierKratos).Allow() {
			c.Header("Retry-After", "60")
			if log != nil {
				log.Warn("kratos flow rate limited",
					"marker", "kratos_flow_rate_limited",
					"flow", flow, "client_ip", ip, "path", p)
			}
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "rate_limited"})
			return
		}
		c.Next()
	}
}

func (l *RateLimiter) handler(t tier) gin.HandlerFunc {
	return func(c *gin.Context) {
		l.enforce(c, clientIP(c), t)
	}
}

// enforce consumes one token from the (key, tier) bucket and either passes the
// request through or aborts it 429 with a Retry-After hint. key is the bucket
// identity — the client IP for the per-IP tiers, or a user-scoped string for
// StrictPerActor.
func (l *RateLimiter) enforce(c *gin.Context, key string, t tier) {
	lim := l.limiterFor(key, t)
	res := lim.Reserve()
	if !res.OK() {
		// Can only happen with a misconfigured limit (rate=0 and burst=0);
		// still refuse deterministically.
		c.Header("Retry-After", "60")
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "rate_limited"})
		return
	}
	if d := res.Delay(); d > 0 {
		// We don't sleep — we reject, advising the client when to retry.
		res.Cancel() // refund the reservation we didn't use
		retry := int(math.Ceil(d.Seconds()))
		if retry < 1 {
			retry = 1
		}
		c.Header("Retry-After", strconv.Itoa(retry))
		c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "rate_limited"})
		return
	}
	c.Next()
}

func (l *RateLimiter) limiterFor(ip string, t tier) *rate.Limiter {
	key := bucketKey{ip: ip, tier: t}

	l.mu.Lock()
	defer l.mu.Unlock()

	if e, ok := l.buckets[key]; ok {
		e.seen = time.Now()
		return e.limiter
	}
	r, b := l.tierSettings(t)
	lim := rate.NewLimiter(r, b)
	l.buckets[key] = &entry{limiter: lim, seen: time.Now()}
	return lim
}

func (l *RateLimiter) tierSettings(t tier) (rate.Limit, int) {
	if t == tierStrict && l.cfg.StrictBurst > 0 {
		return l.cfg.StrictRate, l.cfg.StrictBurst
	}
	if t == tierKratos && l.cfg.KratosBurst > 0 {
		return l.cfg.KratosRate, l.cfg.KratosBurst
	}
	return l.cfg.DefaultRate, l.cfg.DefaultBurst
}

// Cleanup removes per-IP entries that haven't been hit in idleFor. Callers
// typically run this in a goroutine with a time.Ticker. Safe to call
// concurrently with request handling.
func (l *RateLimiter) Cleanup(idleFor time.Duration) {
	cutoff := time.Now().Add(-idleFor)
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, e := range l.buckets {
		if e.seen.Before(cutoff) {
			delete(l.buckets, k)
		}
	}
}
