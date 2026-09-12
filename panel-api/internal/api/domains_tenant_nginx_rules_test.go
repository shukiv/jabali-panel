package api

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #307: a non-admin owner may set a SAFE SUBSET of the nginx Rule Builder
// (rewrite + custom_header) on their own domain when the admin opts in
// (tenant_domain_options_enabled). proxy_pass stays admin-only (SSRF), and a
// rewrite that targets a URL (scheme/host) is rejected (open-redirect / proxy).
func patchNginxRules(t *testing.T, r interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}, jsonBody string) int {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/domains/d1", bytes.NewBufferString(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w.Code
}

func TestDomainPatch_TenantNginxRules(t *testing.T) {
	// X-Robots-Tag is a benign, tenant-settable response header. (A panel-managed
	// header like X-Frame-Options is NOT tenant-settable — see
	// TestDomainPatch_TenantManagedHeadersDenied, ADR-0169 Phase 2.)
	const safeRewrite = `{"nginx_rules":[{"type":"rewrite","pattern":"^/old$","replacement":"/new","flag":"last"},{"type":"custom_header","name":"X-Robots-Tag","value":"noindex"}]}`

	t.Run("owner opt-in OFF: dropped", func(t *testing.T) {
		dom := &models.Domain{ID: "d1", UserID: "u1", Name: "x.com"}
		r, repo := buildSafeOptionsRouter(&auth.AccessClaims{UserID: "u1"}, false, dom)
		if code := patchNginxRules(t, r, safeRewrite); code != http.StatusOK {
			t.Fatalf("status %d", code)
		}
		if len(repo.domains["d1"].NginxRules) != 0 {
			t.Errorf("owner set nginx_rules while opt-in OFF: %+v", repo.domains["d1"].NginxRules)
		}
	})

	t.Run("owner opt-in ON: safe rules applied", func(t *testing.T) {
		dom := &models.Domain{ID: "d1", UserID: "u1", Name: "x.com"}
		r, repo := buildSafeOptionsRouter(&auth.AccessClaims{UserID: "u1"}, true, dom)
		if code := patchNginxRules(t, r, safeRewrite); code != http.StatusOK {
			t.Fatalf("status %d", code)
		}
		got := repo.domains["d1"].NginxRules
		if len(got) != 2 || got[0].Type != "rewrite" || got[1].Type != "custom_header" {
			t.Errorf("safe rules not applied: %+v", got)
		}
	})

	t.Run("owner opt-in ON: proxy_pass rejected", func(t *testing.T) {
		dom := &models.Domain{ID: "d1", UserID: "u1", Name: "x.com"}
		r, _ := buildSafeOptionsRouter(&auth.AccessClaims{UserID: "u1"}, true, dom)
		body := `{"nginx_rules":[{"type":"proxy_pass","path":"/","target":"http://127.0.0.1:8443"}]}`
		if code := patchNginxRules(t, r, body); code != http.StatusBadRequest {
			t.Fatalf("proxy_pass should be 400 for tenant, got %d", code)
		}
	})

	t.Run("owner opt-in ON: rewrite to URL rejected (open redirect / SSRF)", func(t *testing.T) {
		dom := &models.Domain{ID: "d1", UserID: "u1", Name: "x.com"}
		r, _ := buildSafeOptionsRouter(&auth.AccessClaims{UserID: "u1"}, true, dom)
		body := `{"nginx_rules":[{"type":"rewrite","pattern":"^/x$","replacement":"http://169.254.169.254/","flag":"redirect"}]}`
		if code := patchNginxRules(t, r, body); code != http.StatusBadRequest {
			t.Fatalf("rewrite-to-URL should be 400 for tenant, got %d", code)
		}
	})

	t.Run("admin: proxy_pass applied regardless of toggle", func(t *testing.T) {
		dom := &models.Domain{ID: "d1", UserID: "u1", Name: "x.com"}
		r, repo := buildSafeOptionsRouter(&auth.AccessClaims{UserID: "admin", IsAdmin: true}, false, dom)
		body := `{"nginx_rules":[{"type":"proxy_pass","path":"/","target":"http://backend.example.com:9000"}]}`
		if code := patchNginxRules(t, r, body); code != http.StatusOK {
			t.Fatalf("admin proxy_pass status %d", code)
		}
		if len(repo.domains["d1"].NginxRules) != 1 {
			t.Errorf("admin nginx_rules not applied: %+v", repo.domains["d1"].NginxRules)
		}
	})
}

// ADR-0169 Phase 2: a tenant custom_header may not set a response header the
// panel manages — the four security headers it renders (Strict-Transport-
// Security / X-Frame-Options / X-Content-Type-Options / Referrer-Policy), which
// a tenant server-scope add_header would duplicate or weaken, plus the two
// structural names (Content-Length / Transfer-Encoding) that make a malformed
// response. Header names are case-insensitive, so each is denied in every
// casing. The gate is tenant-only: an admin may still set these.
func TestDomainPatch_TenantManagedHeadersDenied(t *testing.T) {
	managed := []string{
		"Strict-Transport-Security",
		"X-Frame-Options",
		"X-Content-Type-Options",
		"Referrer-Policy",
		"Content-Length",
		"Transfer-Encoding",
	}
	for _, h := range managed {
		for _, name := range []string{h, strings.ToLower(h), strings.ToUpper(h)} {
			t.Run("tenant denied: "+name, func(t *testing.T) {
				dom := &models.Domain{ID: "d1", UserID: "u1", Name: "x.com"}
				r, repo := buildSafeOptionsRouter(&auth.AccessClaims{UserID: "u1"}, true, dom)
				body := fmt.Sprintf(`{"nginx_rules":[{"type":"custom_header","name":%q,"value":"x"}]}`, name)
				if code := patchNginxRules(t, r, body); code != http.StatusBadRequest {
					t.Fatalf("managed header %q should be 400 for tenant, got %d", name, code)
				}
				if len(repo.domains["d1"].NginxRules) != 0 {
					t.Errorf("managed header %q applied despite reject: %+v", name, repo.domains["d1"].NginxRules)
				}
			})
		}
	}

	t.Run("tenant: benign custom header applied", func(t *testing.T) {
		dom := &models.Domain{ID: "d1", UserID: "u1", Name: "x.com"}
		r, repo := buildSafeOptionsRouter(&auth.AccessClaims{UserID: "u1"}, true, dom)
		body := `{"nginx_rules":[{"type":"custom_header","name":"X-Robots-Tag","value":"noindex"}]}`
		if code := patchNginxRules(t, r, body); code != http.StatusOK {
			t.Fatalf("benign header status %d", code)
		}
		if len(repo.domains["d1"].NginxRules) != 1 {
			t.Errorf("benign header not applied: %+v", repo.domains["d1"].NginxRules)
		}
	})

	t.Run("admin: managed header applied (gate is tenant-only)", func(t *testing.T) {
		dom := &models.Domain{ID: "d1", UserID: "u1", Name: "x.com"}
		r, repo := buildSafeOptionsRouter(&auth.AccessClaims{UserID: "admin", IsAdmin: true}, true, dom)
		body := `{"nginx_rules":[{"type":"custom_header","name":"X-Frame-Options","value":"DENY"}]}`
		if code := patchNginxRules(t, r, body); code != http.StatusOK {
			t.Fatalf("admin managed header status %d", code)
		}
		if len(repo.domains["d1"].NginxRules) != 1 {
			t.Errorf("admin managed header not applied: %+v", repo.domains["d1"].NginxRules)
		}
	})
}
