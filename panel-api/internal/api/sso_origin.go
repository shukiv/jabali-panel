package api

import (
	"net/url"

	"github.com/gin-gonic/gin"
)

// sameOriginStrict is the single exact same-origin (CSRF) guard (ADR-0099) for
// every browser-facing DB-console SSO mint — tenant phpMyAdmin, tenant Adminer,
// and the privileged admin consoles (JAB-304). A state-changing POST is accepted
// only when the Origin header (or, if absent, the Referer header) parses and its
// hostname EXACTLY equals the request host's hostname (port stripped).
//
// Two look-alike-host bypasses this closes on the privileged path, which
// previously used strings.Contains + an absent-header allowance:
//   - Missing BOTH Origin and Referer is rejected (an old-browser/curl POST is
//     less important than blocking a header-stripping CSRF).
//   - A deceptive host that merely CONTAINS the panel host as a substring —
//     panel.example.com.attacker.tld, or evilpanel.example.com — never matches,
//     because comparison is on the parsed hostname with exact equality.
//
// hostnameOf (sso_phpmyadmin.go) handles the nginx `Host $host` port stripping
// and IPv6-literal cases; browsers send Origin with the visible port, so the
// invariant we can actually enforce is scheme-agnostic hostname equality.
func sameOriginStrict(c *gin.Context) bool {
	if origin := c.GetHeader("Origin"); origin != "" {
		return urlHostMatches(c, origin)
	}
	if referer := c.GetHeader("Referer"); referer != "" {
		return urlHostMatches(c, referer)
	}
	return false
}

// urlHostMatches parses raw (an Origin or Referer value) and reports whether its
// hostname equals the request host's hostname. A parse error or empty host is a
// non-match, never a pass.
func urlHostMatches(c *gin.Context, raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	return hostnameOf(u.Host) == hostnameOf(c.Request.Host)
}
