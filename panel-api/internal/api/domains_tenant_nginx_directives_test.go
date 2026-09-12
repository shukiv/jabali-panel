package api

import (
	"net/http"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// GH #1624 / ADR-0169 Phase 4: the tenant "advanced directives" value-grammar.
// A tenant textarea reaches the server block through this validator, so it is
// deliberately tiny and exhaustive: one statement per line, no blocks, and only
// add_header / expires / etag.
func TestValidateNginxDirectivesTenant(t *testing.T) {
	// Valid inputs — must return "" (accepted).
	valid := []struct {
		name string
		in   string
	}{
		{"empty clears the field", ""},
		{"simple custom header", "add_header X-Robots-Tag noindex;"},
		{"header with always flag", "add_header X-App-Version 1.2.3 always;"},
		{"quoted value with spaces", `add_header X-Powered-By "Jabali Panel";`},
		{"CSP with quoted semicolons", `add_header Content-Security-Policy "default-src 'self'; script-src 'self'" always;`},
		{"expires days", "expires 30d;"},
		{"expires minutes", "expires 5m;"},
		{"expires modified", "expires modified 1h;"},
		{"expires keyword max", "expires max;"},
		{"expires negative", "expires -1;"},
		{"expires off", "expires off;"},
		{"etag on", "etag on;"},
		{"etag off", "etag off;"},
		{"multi-line mix", "add_header X-A a;\nexpires 5m;\netag on;"},
		{"comment then directive", "# cache policy\nadd_header X-A a;"},
		{"blank lines allowed", "\n\nadd_header X-A a;\n\n"},
	}
	for _, tc := range valid {
		t.Run("valid/"+tc.name, func(t *testing.T) {
			if msg := ValidateNginxDirectivesTenant(tc.in); msg != "" {
				t.Errorf("expected accepted, got rejection %q for input %q", msg, tc.in)
			}
		})
	}

	// Invalid inputs — must return a non-empty rejection message.
	invalid := []struct {
		name string
		in   string
	}{
		// The headline security case: the shared scanner only inspects the first
		// token of a line, so a second statement on one line must be rejected or
		// a tenant could hide proxy_pass behind an allowlisted add_header.
		{"multi-statement hides proxy_pass", "add_header X-A a; proxy_pass http://169.254.169.254/;"},
		{"multi-statement hides root", "add_header X-A a; root /etc/jabali-panel/;"},
		// Non-allowlisted directives.
		{"proxy_pass", "proxy_pass http://127.0.0.1:9000;"},
		{"root", "root /etc/jabali-panel/;"},
		{"alias", "alias /etc/;"},
		{"return redirect", "return 302 https://evil.example/;"},
		{"rewrite", "rewrite ^ /elsewhere last;"},
		{"error_page", "error_page 404 /custom.html;"},
		{"gzip duplicate", "gzip on;"},
		{"fastcgi_param jailbreak", "fastcgi_param PHP_ADMIN_VALUE \"open_basedir=\";"},
		{"location block", "location / { add_header X-A a; }"},
		// Panel-managed response headers (three casings each).
		{"managed HSTS", `add_header Strict-Transport-Security "max-age=0";`},
		{"managed HSTS lower", "add_header strict-transport-security x;"},
		{"managed HSTS upper", "add_header STRICT-TRANSPORT-SECURITY x;"},
		{"managed X-Frame-Options", "add_header X-Frame-Options ALLOWALL;"},
		{"managed Content-Length", "add_header Content-Length 0;"},
		{"managed Transfer-Encoding", "add_header Transfer-Encoding chunked;"},
		// Structural / grammar violations.
		{"brace only", "add_header X-A a; }"},
		{"open brace", "add_header X-A a; {"},
		{"no trailing semicolon", "add_header X-A a"},
		{"unbalanced quote", `add_header X-A "unterminated;`},
		// Backslash smuggle: our naive quote toggle sees `\"` as a close, but
		// nginx treats it as a literal quote, so the string stays open and
		// swallows following config. Banned outright.
		{"escaped-quote smuggle", `add_header X "abc\"def;`},
		{"backslash in value", `add_header X-A a\b;`},
		{"double backslash", `add_header X-A "a\\";`},
		{"control char", "add_header X-A \x01;"},
		{"null byte", "add_header X-A \x00;"},
		{"missing value", "add_header X-Foo;"},
		{"four args unquoted value", "add_header X-Foo a b c;"},
		{"invalid header name", "add_header X$Foo value;"},
		{"bad third arg", "add_header X-Foo value nope;"},
		// Bad per-directive values.
		{"expires banana", "expires banana;"},
		{"expires missing", "expires;"},
		{"etag maybe", "etag maybe;"},
	}
	for _, tc := range invalid {
		t.Run("invalid/"+tc.name, func(t *testing.T) {
			if msg := ValidateNginxDirectivesTenant(tc.in); msg == "" {
				t.Errorf("expected rejection, got accepted for input %q", tc.in)
			}
		})
	}

	t.Run("invalid/size cap", func(t *testing.T) {
		big := strings.Repeat("add_header X-A a;\n", 600) // ~10 KB
		if msg := ValidateNginxDirectivesTenant(big); msg == "" {
			t.Error("expected size-cap rejection")
		}
	})
	t.Run("invalid/line cap", func(t *testing.T) {
		lines := strings.Repeat("add_header X-A a;\n", tenantDirectivesMaxLines+2)
		if msg := ValidateNginxDirectivesTenant(lines); msg == "" {
			t.Error("expected line-cap rejection")
		}
	})
}

// TestDomainPatch_TenantNginxDirectives covers the PATCH gate: the field is
// tenant-writable only when the admin opt-in is on, is silently dropped when off
// (so unrelated fields still PATCH), and is always validated with the tenant
// grammar — an admin bypasses the opt-in but not the grammar.
func TestDomainPatch_TenantNginxDirectives(t *testing.T) {
	const safe = `{"nginx_tenant_directives":"add_header X-Robots-Tag noindex;\nexpires 30d;"}`

	t.Run("owner opt-in OFF: dropped", func(t *testing.T) {
		dom := &models.Domain{ID: "d1", UserID: "u1", Name: "x.com"}
		r, repo := buildSafeOptionsRouter(&auth.AccessClaims{UserID: "u1"}, false, dom)
		if code := patchNginxRules(t, r, safe); code != http.StatusOK {
			t.Fatalf("status %d", code)
		}
		if repo.domains["d1"].NginxTenantDirectives != nil {
			t.Errorf("owner set directives while opt-in OFF: %q", *repo.domains["d1"].NginxTenantDirectives)
		}
	})

	t.Run("owner opt-in ON: applied", func(t *testing.T) {
		dom := &models.Domain{ID: "d1", UserID: "u1", Name: "x.com"}
		r, repo := buildSafeOptionsRouter(&auth.AccessClaims{UserID: "u1"}, true, dom)
		if code := patchNginxRules(t, r, safe); code != http.StatusOK {
			t.Fatalf("status %d", code)
		}
		got := repo.domains["d1"].NginxTenantDirectives
		if got == nil || !strings.Contains(*got, "X-Robots-Tag") {
			t.Errorf("safe directives not applied: %v", got)
		}
	})

	t.Run("owner opt-in ON: proxy_pass smuggle rejected", func(t *testing.T) {
		dom := &models.Domain{ID: "d1", UserID: "u1", Name: "x.com"}
		r, _ := buildSafeOptionsRouter(&auth.AccessClaims{UserID: "u1"}, true, dom)
		body := `{"nginx_tenant_directives":"add_header X-A a; proxy_pass http://127.0.0.1:8443;"}`
		if code := patchNginxRules(t, r, body); code != http.StatusBadRequest {
			t.Fatalf("multi-statement smuggle should be 400 for tenant, got %d", code)
		}
	})

	t.Run("admin: applied regardless of toggle but still grammar-checked", func(t *testing.T) {
		dom := &models.Domain{ID: "d1", UserID: "u1", Name: "x.com"}
		r, repo := buildSafeOptionsRouter(&auth.AccessClaims{UserID: "admin", IsAdmin: true}, false, dom)
		if code := patchNginxRules(t, r, safe); code != http.StatusOK {
			t.Fatalf("admin status %d", code)
		}
		if repo.domains["d1"].NginxTenantDirectives == nil {
			t.Error("admin directives not applied")
		}
		// Even an admin is held to the tenant grammar on this field (they have
		// their own nginx_custom_directives for the powerful surface).
		dom2 := &models.Domain{ID: "d1", UserID: "u1", Name: "x.com"}
		r2, _ := buildSafeOptionsRouter(&auth.AccessClaims{UserID: "admin", IsAdmin: true}, false, dom2)
		bad := `{"nginx_tenant_directives":"root /etc/jabali-panel/;"}`
		if code := patchNginxRules(t, r2, bad); code != http.StatusBadRequest {
			t.Fatalf("admin root on tenant field should be 400, got %d", code)
		}
	})
}
