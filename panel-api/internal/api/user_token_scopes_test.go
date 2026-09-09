package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// ctxKey mirrors the unexported middleware key the auth layer stashes the
// token under (middleware.UserAPIToken reads it).
const userTokenCtxKey = "jabali_user_api_token"

func scopeTestRouter(tok *models.UserAPIToken) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1", func(c *gin.Context) {
		if tok != nil {
			c.Set(userTokenCtxKey, tok)
		}
		c.Next()
	}, EnforceUserTokenScopes())
	ok := func(c *gin.Context) { c.Status(http.StatusOK) }
	// A couple of mapped DNS routes + one unmapped (mail) route.
	v1.GET("/domains/:id/dns/records", ok)
	v1.POST("/domains/:id/dns/records", ok)
	v1.PATCH("/dns/records/:recordId", ok)
	v1.DELETE("/domains/:id/dns/zone", ok)    // GH #1611 destructive DNS-zone delete
	v1.GET("/domains/:id/mail/mailboxes", ok) // unmapped
	return r
}

func reqStatus(r *gin.Engine, method, path string) int {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	r.ServeHTTP(w, req)
	return w.Code
}

func TestEnforceUserTokenScopes(t *testing.T) {
	full := &models.UserAPIToken{Scopes: models.UserAPIScopes{}} // empty = full
	readDNS := &models.UserAPIToken{Scopes: models.UserAPIScopes{ScopeReadDNS}}
	writeDNS := &models.UserAPIToken{Scopes: models.UserAPIScopes{ScopeWriteDNS}}
	ddns := &models.UserAPIToken{Scopes: models.UserAPIScopes{ScopeDDNS}}

	cases := []struct {
		name   string
		tok    *models.UserAPIToken
		method string
		path   string
		want   int
	}{
		{"cookie auth (no token) allowed", nil, "GET", "/api/v1/domains/d1/dns/records", http.StatusOK},
		{"full token any route", full, "GET", "/api/v1/domains/d1/mail/mailboxes", http.StatusOK},
		{"read:dns reads dns", readDNS, "GET", "/api/v1/domains/d1/dns/records", http.StatusOK},
		{"read:dns cannot write dns", readDNS, "POST", "/api/v1/domains/d1/dns/records", http.StatusForbidden},
		{"write:dns writes dns", writeDNS, "POST", "/api/v1/domains/d1/dns/records", http.StatusOK},
		{"write:dns on record route", writeDNS, "PATCH", "/api/v1/dns/records/r1", http.StatusOK},
		{"read:dns cannot delete zone", readDNS, "DELETE", "/api/v1/domains/d1/dns/zone", http.StatusForbidden},
		{"write:dns deletes zone", writeDNS, "DELETE", "/api/v1/domains/d1/dns/zone", http.StatusOK},
		{"dns token denied on unmapped mail", writeDNS, "GET", "/api/v1/domains/d1/mail/mailboxes", http.StatusForbidden},
		{"ddns token denied on dns REST", ddns, "GET", "/api/v1/domains/d1/dns/records", http.StatusForbidden},
		{"ddns token denied on unmapped", ddns, "GET", "/api/v1/domains/d1/mail/mailboxes", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := scopeTestRouter(tc.tok)
			if got := reqStatus(r, tc.method, tc.path); got != tc.want {
				t.Errorf("%s %s: got %d want %d", tc.method, tc.path, got, tc.want)
			}
		})
	}
}

func TestValidateUserScopes(t *testing.T) {
	if _, ok := validateUserScopes(models.UserAPIScopes{ScopeReadDNS, ScopeWriteDNS, ScopeDDNS}); !ok {
		t.Error("known scopes should validate")
	}
	if _, ok := validateUserScopes(models.UserAPIScopes{}); !ok {
		t.Error("empty scopes (full) should validate")
	}
	if bad, ok := validateUserScopes(models.UserAPIScopes{"write:everything"}); ok || bad != "write:everything" {
		t.Errorf("unknown scope should fail, got bad=%q ok=%v", bad, ok)
	}
}

// TestResolveUserScope audits the phase-3 area mapping: representative routes
// resolve to the expected scope, sub-trees under /domains/:id don't leak into
// the bare-domain grant, and unmapped routes fail closed.
func TestResolveUserScope(t *testing.T) {
	cases := []struct {
		method, path, want string
		ok                 bool
	}{
		{"GET", "/api/v1/domains", "read:domains", true},
		{"POST", "/api/v1/domains", "write:domains", true},
		{"GET", "/api/v1/domains/:id", "read:domains", true},
		{"DELETE", "/api/v1/domains/:id", "write:domains", true},
		{"GET", "/api/v1/domains/:id/dns/records", "read:dns", true},
		{"PATCH", "/api/v1/dns/records/:recordId", "write:dns", true},
		{"GET", "/api/v1/domains/:id/dnssec", "read:dns", true},
		{"POST", "/api/v1/domains/:id/mailboxes", "write:mail", true},
		{"PATCH", "/api/v1/mailboxes/:mbid", "write:mail", true},
		{"GET", "/api/v1/mail/forwarders", "read:mail", true},
		{"GET", "/api/v1/domains/:id/mail-certificate", "read:mail", true}, // not domains
		{"GET", "/api/v1/domains/:id/php-settings", "read:php", true},
		{"PATCH", "/api/v1/domains/:id/php", "write:php", true},
		{"DELETE", "/api/v1/domains/:id/ssl", "write:ssl", true},
		{"GET", "/api/v1/files", "read:files", true},
		{"POST", "/api/v1/files/upload", "write:files", true},
		{"GET", "/api/v1/databases", "read:databases", true},
		{"POST", "/api/v1/database-users", "write:databases", true},
		{"GET", "/api/v1/applications", "read:apps", true},
		{"POST", "/api/v1/cron", "write:cron", true},
		{"GET", "/api/v1/ssh-keys", "read:ssh", true},
		{"GET", "/api/v1/logs/access", "read:logs", true},
		{"GET", "/api/v1/me/backups", "read:backups", true},
		// fail-closed
		{"GET", "/api/v1/me/api-tokens", "", false},
		{"GET", "/api/v1/branding", "", false},
		{"GET", "/api/v1/domains/:id/unmapped-feature", "", false},
	}
	for _, c := range cases {
		got, ok := resolveUserScope(c.method, c.path)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("%s %s: got (%q,%v) want (%q,%v)", c.method, c.path, got, ok, c.want, c.ok)
		}
	}
}

// TestTokenAllowsRecord — per-record DDNS constraint (GH #245 phase 5).
func TestTokenAllowsRecord(t *testing.T) {
	const recA = "01AAAAAAAAAAAAAAAAAAAAAAAA"
	const recB = "01BBBBBBBBBBBBBBBBBBBBBBBB"
	// No record: constraint → any record allowed.
	if !tokenAllowsRecord(models.UserAPIScopes{"ddns"}, recA) {
		t.Error("unconstrained token should update any record")
	}
	// Constrained to recA.
	sc := models.UserAPIScopes{"ddns", "record:" + recA}
	if !tokenAllowsRecord(sc, recA) {
		t.Error("constrained token should update its record")
	}
	if tokenAllowsRecord(sc, recB) {
		t.Error("constrained token must NOT update a different record")
	}

	// validate accepts well-formed record scopes, rejects malformed.
	if _, ok := validateUserScopes(models.UserAPIScopes{"ddns", "record:" + recA}); !ok {
		t.Error("valid record scope should pass")
	}
	if _, ok := validateUserScopes(models.UserAPIScopes{"record:short"}); ok {
		t.Error("malformed record scope should fail")
	}
}
