// Package cacheprobe answers the question the cache health check does not:
// is this site ACTUALLY being cached?
//
// JAB-201. The existing check verifies configuration — plugin active, drop-in
// and constants present, Redis reachable — and reports green on all of it. On
// brown-online.co.il every one of those passed while the page cache had never
// stored a single entry: the site was in maintenance mode, so WordPress emitted
// nocache_headers() on every response and the fail-closed gate (GH #637)
// correctly refused to store anything. The jcache log showed only MISS and
// BYPASS, never HIT, for the life of the site.
//
// Configuration being right and caching working are different claims, and only
// the second one matters to the operator. This makes the second claim testable:
// fetch the page twice and look at what nginx reports.
package cacheprobe

import (
	"net/http"
	"strings"
)

// Verdict is the outcome of a double-fetch probe.
type Verdict string

const (
	// VerdictCaching — the second fetch was served from cache. Working.
	VerdictCaching Verdict = "caching"
	// VerdictNotCaching — repeated fetches never hit. The reason explains why.
	VerdictNotCaching Verdict = "not_caching"
	// VerdictUnknown — nginx did not report a cache status, so this site is not
	// running the jabali cache vhost at all and the probe says nothing.
	VerdictUnknown Verdict = "unknown"
)

// CacheStatusHeader is what the jabali vhost adds:
//
//	add_header X-Jabali-Cache $upstream_cache_status always;
const CacheStatusHeader = "X-Jabali-Cache"

// Result is a probe outcome plus the reason, phrased for an operator.
type Result struct {
	Verdict Verdict
	// Reason names the CAUSE where one is detectable, not just the symptom.
	// "page cache enabled but the site opts out" is actionable; "no HIT" is not.
	Reason string
	First  string // X-Jabali-Cache of the first fetch
	Second string // ...and the second
}

// Classify decides the verdict from two responses' headers.
//
// Pure, so the interesting cases are testable without standing up nginx, a
// tenant site and a maintenance-mode plugin.
func Classify(first, second http.Header) Result {
	f := strings.ToUpper(strings.TrimSpace(first.Get(CacheStatusHeader)))
	s := strings.ToUpper(strings.TrimSpace(second.Get(CacheStatusHeader)))
	r := Result{First: f, Second: s}

	if f == "" && s == "" {
		r.Verdict = VerdictUnknown
		r.Reason = "no " + CacheStatusHeader + " header — this vhost is not running the jabali cache config"
		return r
	}
	if s == "HIT" {
		r.Verdict = VerdictCaching
		return r
	}

	r.Verdict = VerdictNotCaching
	// Prefer the upstream's own opt-out as the explanation: it is the cause,
	// and the one an operator can act on. BYPASS only says the gate fired.
	if why := upstreamOptOut(second); why != "" {
		r.Reason = "the site tells caches not to store this response (" + why +
			"). A maintenance/coming-soon or security plugin emitting nocache_headers() " +
			"does this site-wide; the cache is working as designed by refusing to store it"
		return r
	}
	if s == "BYPASS" {
		r.Reason = "nginx bypassed the cache for this request — a skip gate matched " +
			"(logged-in cookie, Authorization header, or an excluded URI)"
		return r
	}
	if v := second.Get("Vary"); varyDefeatsCache(v) {
		r.Reason = "the response varies on " + v + ", which prevents a shared cache entry"
		return r
	}
	r.Reason = "two consecutive anonymous fetches both missed (" + f + " then " + s +
		") — the response is reaching the cache but never being stored"
	return r
}

// upstreamOptOut reports which directive tells caches not to store, or "".
func upstreamOptOut(h http.Header) string {
	cc := strings.ToLower(h.Get("Cache-Control"))
	for _, tok := range []string{"no-store", "no-cache", "private"} {
		if strings.Contains(cc, tok) {
			return "Cache-Control: " + tok
		}
	}
	// The classic maintenance-plugin signature: WordPress's nocache_headers()
	// sends an Expires in the past, historically 1984.
	if exp := h.Get("Expires"); exp != "" && strings.Contains(exp, "1984") {
		return "Expires: " + exp
	}
	if strings.EqualFold(h.Get("Pragma"), "no-cache") {
		return "Pragma: no-cache"
	}
	return ""
}

// varyDefeatsCache reports whether a Vary header prevents a shared entry.
//
// Vary on Accept-Encoding is normal and fine — nginx keys on it. Anything else
// (Cookie, User-Agent, *) makes a shared cache entry useless.
func varyDefeatsCache(vary string) bool {
	v := strings.TrimSpace(vary)
	if v == "" {
		return false
	}
	for _, part := range strings.Split(v, ",") {
		p := strings.ToLower(strings.TrimSpace(part))
		if p == "" || p == "accept-encoding" {
			continue
		}
		return true
	}
	return false
}
