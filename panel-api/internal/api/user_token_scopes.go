package api

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// User API token scopes (GH #245, ADR-0144). A token with an EMPTY scope set
// has full per-user access (every existing token — no migration). A token with
// a non-empty set is capability-restricted: it may only reach routes whose
// required scope it Has(), and is denied everywhere else (FAIL-CLOSED).
//
// Scopes are `<action>:<area>` (action ∈ read|write; `read:*`/`write:*`
// wildcards via UserAPIScopes.Has), plus the narrow `ddns` grant for the DynDNS
// shim only. Areas are resolved from the route by resolveUserScope.
const ScopeDDNS = "ddns"

// userScopeAreas is the full vocabulary. read:<area> / write:<area> for each,
// plus ddns, are the scopes a token may carry.
var userScopeAreas = []string{
	"dns", "mail", "files", "databases", "apps",
	"domains", "cron", "ssl", "php", "ssh", "logs",
	"notifications", "backups",
}

// Convenience constants for the routes/tests that reference DNS directly.
const (
	ScopeReadDNS  = "read:dns"
	ScopeWriteDNS = "write:dns"
)

// ScopeRedisFlush lets a token flush the owner's Redis keys and nothing else
// (GH #2003), so an app can clear its cache without holding a full token.
// There is no read:redis: GET /me/redis-access returns the Redis password, so
// it stays unmapped and a scoped token can't fetch it.
const ScopeRedisFlush = "write:redis"

// knownUserScopes is the closed set the token-create API accepts. Rejecting
// unknown scopes stops a typo'd scope from silently fail-closing a token.
var knownUserScopes = map[string]bool{}

func init() {
	for _, a := range userScopeAreas {
		knownUserScopes["read:"+a] = true
		knownUserScopes["write:"+a] = true
	}
	knownUserScopes[ScopeDDNS] = true
	knownUserScopes[ScopeRedisFlush] = true
}

// recordScopeRe matches a per-record DDNS constraint scope (GH #245 phase 5):
// `record:<26-char DNS record ULID>`. A token carrying one or more of these may
// update ONLY those DNS records via the DDNS shim.
var recordScopeRe = regexp.MustCompile(`^record:[0-9A-Za-z]{26}$`)

// ValidateUserScopes is the exported entry point for non-handler callers (the
// `jabali user-token` CLI, #570) so they validate against the SAME closed scope
// catalog as the REST mint endpoint — no drift. Returns (badScope, ok).
func ValidateUserScopes(scopes models.UserAPIScopes) (string, bool) {
	return validateUserScopes(scopes)
}

func validateUserScopes(scopes models.UserAPIScopes) (string, bool) {
	for _, s := range scopes {
		if strings.HasPrefix(s, "record:") {
			if !recordScopeRe.MatchString(s) {
				return s, false
			}
			continue
		}
		if !knownUserScopes[s] {
			return s, false
		}
	}
	return "", true
}

// tokenAllowsRecord reports whether a token may update the given DNS record.
// A token with NO record: constraint may update any of the owner's records;
// one with record: constraints may update only the listed record(s).
func tokenAllowsRecord(scopes models.UserAPIScopes, recordID string) bool {
	constrained := false
	for _, s := range scopes {
		if strings.HasPrefix(s, "record:") {
			constrained = true
			if s[len("record:"):] == recordID {
				return true
			}
		}
	}
	return !constrained
}

// userScopeExact maps a bare route pattern (gin c.FullPath) to its area. Used
// for the domain-collection + bare-domain routes, which would otherwise be
// shadowed by a loose /domains/:id prefix (there is deliberately NO such
// catch-all prefix, so a sub-resource we forgot fails CLOSED, never opens up).
var userScopeExact = map[string]string{
	"/api/v1/domains":     "domains",
	"/api/v1/domains/:id": "domains",
	// GH #2003: POST only, so it resolves to write:redis (ScopeRedisFlush).
	"/api/v1/me/redis-access/flush": "redis",
}

type areaRule struct{ prefix, area string }

// userScopePrefixes maps a route-pattern sub-tree to an area. A request matches
// a rule when its FullPath equals the prefix or sits under it on a segment
// boundary (prefix+"/"). The longest matching prefix wins, so the per-feature
// sub-trees under /domains/:id beat each other unambiguously and nothing falls
// back to a bare-domain grant.
var userScopePrefixes = []areaRule{
	{"/api/v1/domains/:id/dnssec", "dns"},
	{"/api/v1/domains/:id/dns", "dns"},
	{"/api/v1/dns/records", "dns"},

	{"/api/v1/domains/:id/mailboxes", "mail"},
	{"/api/v1/domains/:id/mailbox-group-memberships", "mail"},
	{"/api/v1/domains/:id/mail-certificate", "mail"},
	{"/api/v1/domains/:id/mta-sts", "mail"},
	{"/api/v1/domains/:id/disclaimer", "mail"},
	{"/api/v1/domains/:id/catchall", "mail"},
	{"/api/v1/domains/:id/autoresponders", "mail"},
	{"/api/v1/domains/:id/shared-resources", "mail"},
	{"/api/v1/mailboxes", "mail"},
	{"/api/v1/mailgroups", "mail"},
	{"/api/v1/shared-resources", "mail"},
	{"/api/v1/mail", "mail"},

	{"/api/v1/domains/:id/php-settings", "php"},
	{"/api/v1/domains/:id/php", "php"},

	{"/api/v1/domains/:id/ssl", "ssl"},

	{"/api/v1/files", "files"},
	{"/api/v1/domains/:id/directory-privacy", "files"},
	{"/api/v1/domains/:id/browse", "files"},

	{"/api/v1/databases", "databases"},
	{"/api/v1/database-users", "databases"},
	{"/api/v1/database-user-grants", "databases"},

	{"/api/v1/applications", "apps"},
	{"/api/v1/docker-apps", "apps"},
	{"/api/v1/python-apps", "apps"},

	{"/api/v1/cron", "cron"},
	{"/api/v1/ssh-keys", "ssh"},

	{"/api/v1/domains/:id/whois", "domains"},
	{"/api/v1/domains/:id/acls", "domains"},
	{"/api/v1/domains/:id/cache", "domains"},
	{"/api/v1/domains/:id/ip", "domains"},
	{"/api/v1/domains/:id/bandwidth", "domains"},
	{"/api/v1/user/ips", "domains"},

	{"/api/v1/logs", "logs"},
	{"/api/v1/notifications", "notifications"},

	{"/api/v1/me/backups", "backups"},
	{"/api/v1/backups", "backups"},
	{"/api/v1/backup-destinations", "backups"},
	{"/api/v1/backup-schedules", "backups"},
	{"/api/v1/backup-encryption", "backups"},
}

// resolveUserScope returns the scope a restricted token must hold for a route,
// or ok=false when the route is unmapped (caller fail-closes).
func resolveUserScope(method, fullPath string) (string, bool) {
	area, ok := userScopeExact[fullPath]
	if !ok {
		best := ""
		for _, r := range userScopePrefixes {
			if fullPath == r.prefix || strings.HasPrefix(fullPath, r.prefix+"/") {
				if len(r.prefix) > len(best) {
					best, area = r.prefix, r.area
				}
			}
		}
		if best == "" {
			return "", false
		}
	}
	if method == http.MethodGet || method == http.MethodHead {
		return "read:" + area, true
	}
	return "write:" + area, true
}

// EnforceUserTokenScopes gates the /api/v1 group for restricted API tokens.
// Browser (Kratos cookie) sessions and full tokens (empty scopes) pass through
// untouched; a scoped token is allowed only on a mapped route whose scope it
// holds, and 403'd everywhere else (fail-closed). Mount AFTER the auth
// middleware that stashes the token on the context.
func EnforceUserTokenScopes() gin.HandlerFunc {
	return func(c *gin.Context) {
		tokAny := middleware.UserAPIToken(c)
		if tokAny == nil {
			c.Next() // Kratos cookie auth — already authorized
			return
		}
		tok, ok := tokAny.(*models.UserAPIToken)
		if !ok || len(tok.Scopes) == 0 {
			c.Next() // full token
			return
		}
		want, mapped := resolveUserScope(c.Request.Method, c.FullPath())
		if !mapped {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":   "scope_not_permitted",
				"message": "this API token is scope-restricted and cannot access this endpoint",
			})
			return
		}
		if !tok.Scopes.Has(want) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":   "scope_required",
				"message": "API token is missing the required scope: " + want,
			})
			return
		}
		c.Next()
	}
}
