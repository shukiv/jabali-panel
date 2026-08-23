// DDNS-protocol shim — speaks the de-facto DynDNS v2 API every router
// firmware (OpenWrt, Mikrotik, pfSense, Ubiquiti) and ddclient /
// inadyn knows how to talk. Translates `GET /nic/update?hostname=&
// myip=` + HTTP Basic auth into a panel-internal DNS record update.
//
//	GET /nic/update?hostname=vpn.example.com[&myip=1.2.3.4]
//
//	Authorization: Basic base64(<anything>:jat_<43-char-base64url>)
//
// The username half of the Basic credentials is ignored — every
// router DDNS dialog asks for "username + password" so we accept any
// non-empty username so the user has something to type. The password
// is the panel API token (`jat_…`) from /api/v1/me/api-tokens.
//
// myip is optional. When omitted we use the client's source IP
// (X-Real-IP / X-Forwarded-For / RemoteAddr) — this matches what
// every DDNS provider does and lets ddclient run with
// `use=web, web=https://dailytxt.sqldbserv.net/nic/update`.
//
// Response format mirrors the No-IP / DynDNS / Cloudflare-DDNS
// spec so existing clients understand it:
//
//	good <ip>     update succeeded; record now points at <ip>
//	nochg <ip>    record already pointed at that IP; no change
//	badauth       Authorization header missing or token invalid
//	nohost        hostname doesn't resolve to a record this token can update
//	notfqdn       hostname is not a valid FQDN
//	911           server-side error (don't retry for a while)
//
// Each response is a single line, content-type text/plain, with the
// status text on stdout. ddclient looks for the literal "good" or
// "nochg" prefix; anything else is treated as a hard error.
package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// DDNSConfig wires the shim's dependencies. Tokens are the same
// repo used by the bearer-auth middleware; we re-verify on every
// /nic/update request rather than reusing the middleware's claim
// pipeline because Basic auth needs a different parser.
type DDNSConfig struct {
	Tokens  repository.UserAPITokenRepository
	Domains repository.DomainRepository
	Zones   repository.DNSZoneRepository
	Records repository.DNSRecordRepository
	// Reconciler nudges a per-domain reconcile after the record
	// changes so PDNS picks up the new content within seconds.
	// Optional — the next regular tick converges anyway.
	Reconciler DNSScheduler
}

// RegisterDDNSRoutes mounts the shim at the top-level `/nic/update`
// route (NOT under /api/v1). Every router firmware hardcodes that
// path. Hosting it at the same path as DynDNS lets clients work
// with zero config beyond hostname + credentials.
func RegisterDDNSRoutes(r gin.IRouter, cfg DDNSConfig) {
	if cfg.Tokens == nil || cfg.Records == nil || cfg.Zones == nil || cfg.Domains == nil {
		return
	}
	h := &ddnsHandler{cfg: cfg}
	// DynDNS spec uses GET; some routers send POST. Accept both.
	r.GET("/nic/update", h.handle)
	r.POST("/nic/update", h.handle)
}

type ddnsHandler struct {
	cfg DDNSConfig
}

func (h *ddnsHandler) handle(c *gin.Context) {
	// 1. Parse + validate Basic auth.
	_, secret, ok := c.Request.BasicAuth()
	if !ok || !strings.HasPrefix(secret, "jat_") {
		ddnsReply(c, http.StatusUnauthorized, "badauth")
		return
	}
	tok, err := h.cfg.Tokens.FindByHash(c.Request.Context(), middleware.HashUserAPIToken(secret))
	if err != nil {
		ddnsReply(c, http.StatusUnauthorized, "badauth")
		return
	}
	if !tok.IsActive(timeNow()) {
		ddnsReply(c, http.StatusUnauthorized, "badauth")
		return
	}
	// GH #245: a scope-restricted token may use the DDNS shim only if it
	// holds `ddns` (router-only) or `write:dns`. Empty scopes = full access.
	if len(tok.Scopes) > 0 && !tok.Scopes.Has(ScopeDDNS) && !tok.Scopes.Has(ScopeWriteDNS) {
		ddnsReply(c, http.StatusForbidden, "badauth")
		return
	}

	// 2. Parse hostname + IP. hostname may be a leaf ("vpn") or an
	//    FQDN ("vpn.example.com"); only the latter is the real
	//    DynDNS contract, and we don't have a "default domain" to
	//    anchor a leaf against, so reject anything without a dot.
	hostname := strings.TrimSpace(strings.ToLower(c.Query("hostname")))
	if hostname == "" {
		hostname = strings.TrimSpace(strings.ToLower(c.Query("host")))
	}
	if hostname == "" || !strings.Contains(hostname, ".") {
		ddnsReply(c, http.StatusBadRequest, "notfqdn")
		return
	}

	ip := strings.TrimSpace(c.Query("myip"))
	if ip == "" {
		ip = strings.TrimSpace(c.Query("ip"))
	}
	if ip == "" {
		ip = clientSourceIP(c)
	}
	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		ddnsReply(c, http.StatusBadRequest, "notfqdn")
		return
	}
	wantType := "A"
	if parsedIP.To4() == nil {
		wantType = "AAAA"
	}

	// 3. Resolve hostname → (zone, record). Strategy: find the
	//    longest-suffix domain the caller owns, then look for a
	//    matching name+type in that zone.
	zone, record, err := h.resolveDDNSRecord(c.Request.Context(), tok.UserID, hostname, wantType)
	if err != nil {
		// Indistinguishable response shape for "no zone", "wrong
		// owner", "no record" — don't leak ownership info to a
		// token holder.
		ddnsReply(c, http.StatusNotFound, "nohost")
		return
	}

	// 4. Update only when content actually changed. nochg matches
	//    what every other DDNS provider returns; ddclient throttles
	//    based on it.
	// GH #245 phase 5: a record-scoped token may only touch its record(s).
	if !tokenAllowsRecord(tok.Scopes, record.ID) {
		ddnsReply(c, http.StatusForbidden, "nohost")
		return
	}
	if record.Content == parsedIP.String() {
		ddnsReply(c, http.StatusOK, "nochg "+parsedIP.String())
		return
	}
	record.Content = parsedIP.String()
	if err := h.cfg.Records.Update(c.Request.Context(), record); err != nil {
		ddnsReply(c, http.StatusInternalServerError, "911")
		return
	}
	if h.cfg.Reconciler != nil {
		// JAB-371: Reconciler.Schedule takes a DOMAIN id and looks the resource
		// up in the domains table. record.ZoneID is a dns_zones id — passing it
		// here made ReconcileOne fail to resolve, so the DNS change only landed
		// on the next full periodic sweep. Zones are 1:1 with domains, so
		// zone.DomainID is the resolvable domain to reconcile immediately.
		h.cfg.Reconciler.Schedule(zone.DomainID)
	}
	ddnsReply(c, http.StatusOK, "good "+parsedIP.String())
}

// resolveDDNSRecord finds the record the caller can update for
// hostname. Returns ErrNotFound on any failure (caller maps to nohost).
//
// Matching:
//  1. Walk every domain the user owns; longest .Name that is a suffix
//     of hostname wins (so vpn.foo.bar matches `foo.bar` not just `bar`).
//  2. Within that domain's zone, find a record whose .Name + .Type
//     match. .Name in dns_records is RELATIVE to the zone apex (e.g.
//     "vpn" for vpn.foo.bar in zone foo.bar; "@" for the apex itself).
func (h *ddnsHandler) resolveDDNSRecord(ctx context.Context, userID, hostname, wantType string) (*models.DNSZone, *models.DNSRecord, error) {
	// List ALL user domains. The per-tenant universe is small (10s,
	// not 10ks) so the linear scan is fine and avoids a fragile LIKE
	// query against the domains table.
	domains, _, err := h.cfg.Domains.ListByUserID(ctx, userID, repository.ListOptions{
		Limit: 10000,
	})
	if err != nil {
		return nil, nil, err
	}

	// Pick the longest suffix match. "@" / apex requested when
	// hostname EQUALS a domain name; otherwise the leftover is the
	// relative record name.
	var (
		bestDom  *models.Domain
		relative string
	)
	for i := range domains {
		dom := &domains[i]
		dn := strings.ToLower(dom.Name)
		switch {
		case hostname == dn:
			if bestDom == nil || len(dn) > len(bestDom.Name) {
				bestDom = dom
				relative = "@"
			}
		case strings.HasSuffix(hostname, "."+dn):
			if bestDom == nil || len(dn) > len(bestDom.Name) {
				bestDom = dom
				relative = hostname[:len(hostname)-len(dn)-1] // strip ".dom"
			}
		}
	}
	if bestDom == nil {
		return nil, nil, errors.New("no matching domain")
	}

	// Zone lookup by domain id (zone is 1:1 with domain in jabali).
	zone, err := h.cfg.Zones.FindByDomainID(ctx, bestDom.ID)
	if err != nil || zone == nil {
		return nil, nil, errors.New("no zone for domain")
	}

	records, err := h.cfg.Records.ListByZoneID(ctx, zone.ID)
	if err != nil {
		return nil, nil, err
	}
	for i := range records {
		r := &records[i]
		if strings.EqualFold(r.Name, relative) && strings.EqualFold(r.Type, wantType) {
			return zone, r, nil
		}
	}
	return nil, nil, errors.New("no matching record")
}

// ddnsReply writes the DynDNS-style single-line text response. Both
// the status text and content-type are exactly what router firmware
// expects — every deviation breaks at least one client.
func ddnsReply(c *gin.Context, status int, body string) {
	c.Header("Cache-Control", "no-store")
	c.Data(status, "text/plain; charset=utf-8", []byte(body+"\n"))
}

// clientSourceIP returns the most-trusted client IP the request carries.
// Same logic the bearer-auth middleware uses, duplicated here so the
// DDNS shim doesn't need to import the middleware package (circular
// dependency hazard given middleware imports repository).
func clientSourceIP(c *gin.Context) string {
	if v := c.GetHeader("X-Real-IP"); v != "" {
		return v
	}
	if v := c.GetHeader("X-Forwarded-For"); v != "" {
		if i := strings.IndexByte(v, ','); i >= 0 {
			return strings.TrimSpace(v[:i])
		}
		return strings.TrimSpace(v)
	}
	if host, _, err := net.SplitHostPort(c.Request.RemoteAddr); err == nil {
		return host
	}
	return c.Request.RemoteAddr
}

var timeNow = func() time.Time { return time.Now().UTC() }
