package api

import (
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// TestValidateNginxRules_TypedLocationRules covers the GH #1624 deny_paths /
// static_cache rule kinds: the accepted shapes and every rejection, with the
// regex-injection case first (the reason the extension charset exists).
func TestValidateNginxRules_TypedLocationRules(t *testing.T) {
	tests := []struct {
		name      string
		rules     models.NginxRules
		wantError bool
		errSubstr string
	}{
		{
			name:      "deny_paths with plain extensions is valid",
			rules:     models.NginxRules{{Type: "deny_paths", Extensions: []string{"env", "sql", "bak", "log"}}},
			wantError: false,
		},
		{
			name:      "static_cache with a non-default extension + duration is valid",
			rules:     models.NginxRules{{Type: "static_cache", Extensions: []string{"pdf", "mp4"}, Duration: "30d"}},
			wantError: false,
		},
		{
			// The core boundary: a short extension carrying a regex/nginx
			// metacharacter that would break out of the panel-built `\.( … )$`
			// group. Kept under the length cap so the charset check is what trips.
			name:      "deny_paths rejects a regex-injection extension",
			rules:     models.NginxRules{{Type: "deny_paths", Extensions: []string{`env)$`}}},
			wantError: true,
			errSubstr: "letters and digits",
		},
		{
			name:      "deny_paths rejects a dotted extension",
			rules:     models.NginxRules{{Type: "deny_paths", Extensions: []string{".env"}}},
			wantError: true,
			errSubstr: "letters and digits",
		},
		{
			name:      "deny_paths rejects an empty extension list",
			rules:     models.NginxRules{{Type: "deny_paths", Extensions: []string{}}},
			wantError: true,
			errSubstr: "at least one",
		},
		{
			name:      "deny_paths rejects php (silent no-op: php location wins)",
			rules:     models.NginxRules{{Type: "deny_paths", Extensions: []string{"php"}}},
			wantError: true,
			errSubstr: "PHP location",
		},
		{
			name:      "static_cache rejects php-family (source disclosure)",
			rules:     models.NginxRules{{Type: "static_cache", Extensions: []string{"phtml"}, Duration: "1h"}},
			wantError: true,
			errSubstr: "PHP source",
		},
		{
			name:      "static_cache rejects php7 (versioned php)",
			rules:     models.NginxRules{{Type: "static_cache", Extensions: []string{"php7"}, Duration: "1h"}},
			wantError: true,
			errSubstr: "PHP source",
		},
		{
			name:      "static_cache rejects an already-template-cached extension",
			rules:     models.NginxRules{{Type: "static_cache", Extensions: []string{"css"}, Duration: "30d"}},
			wantError: true,
			errSubstr: "built-in static-asset",
		},
		{
			name:      "deny_paths rejects a template-cached extension (shadowed, would no-op)",
			rules:     models.NginxRules{{Type: "deny_paths", Extensions: []string{"svg"}}},
			wantError: true,
			errSubstr: "built-in static-asset",
		},
		{
			name:      "deny_paths rejects html (template caches it too)",
			rules:     models.NginxRules{{Type: "deny_paths", Extensions: []string{"html"}}},
			wantError: true,
			errSubstr: "built-in static-asset",
		},
		{
			// A non-breaking space (U+00A0) can ride in on a copy-paste of "30d".
			// It survives a TrimSpace but must NOT survive validation, because
			// Duration is rendered verbatim into `expires <dur>;` and would then
			// fail nginx -t (tearing down the tenant's vhost).
			name:      "static_cache rejects a duration with a non-breaking space",
			rules:     models.NginxRules{{Type: "static_cache", Extensions: []string{"pdf"}, Duration: " 30d"}},
			wantError: true,
			errSubstr: "expires value",
		},
		{
			name:      "static_cache rejects a duration with a trailing space",
			rules:     models.NginxRules{{Type: "static_cache", Extensions: []string{"pdf"}, Duration: "30d "}},
			wantError: true,
			errSubstr: "expires value",
		},
		{
			name:      "static_cache requires a duration",
			rules:     models.NginxRules{{Type: "static_cache", Extensions: []string{"pdf"}}},
			wantError: true,
			errSubstr: "needs a duration",
		},
		{
			name:      "static_cache rejects a bogus duration",
			rules:     models.NginxRules{{Type: "static_cache", Extensions: []string{"pdf"}, Duration: "forever"}},
			wantError: true,
			errSubstr: "expires value",
		},
		{
			name:      "static_cache accepts the max keyword",
			rules:     models.NginxRules{{Type: "static_cache", Extensions: []string{"pdf"}, Duration: "max"}},
			wantError: false,
		},
		{
			name:      "deny_paths rejects a control character in an extension",
			rules:     models.NginxRules{{Type: "deny_paths", Extensions: []string{"en\nv"}}},
			wantError: true,
		},
		{
			name:      "deny_paths rejects too many extensions",
			rules:     models.NginxRules{{Type: "deny_paths", Extensions: manyExts(maxExtensionsPerRule + 1)}},
			wantError: true,
			errSubstr: "too many",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateNginxRules(tc.rules)
			if tc.wantError && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantError && err != nil {
				t.Fatalf("expected no error, got %v", err)
			}
			if tc.errSubstr != "" && (err == nil || !strings.Contains(err.Error(), tc.errSubstr)) {
				t.Fatalf("expected error containing %q, got %v", tc.errSubstr, err)
			}
		})
	}
}

// TestValidateTenantNginxRules_TypedLocationRules confirms both new kinds are in
// the tenant-safe subset, while an admin-only kind (proxy_pass) still is not.
func TestValidateTenantNginxRules_TypedLocationRules(t *testing.T) {
	if err := validateTenantNginxRules(models.NginxRules{
		{Type: "deny_paths", Extensions: []string{"env", "sql"}},
		{Type: "static_cache", Extensions: []string{"pdf"}, Duration: "7d"},
	}); err != nil {
		t.Fatalf("tenant deny_paths+static_cache should be allowed, got %v", err)
	}
	err := validateTenantNginxRules(models.NginxRules{
		{Type: "proxy_pass", Path: "/", Target: "http://127.0.0.1:8080"},
	})
	if err == nil || !strings.Contains(err.Error(), "not available to tenants") {
		t.Fatalf("tenant proxy_pass should be refused, got %v", err)
	}
}

func manyExts(n int) []string {
	out := make([]string, n)
	for i := range out {
		// distinct so the count cap trips, not the dedupe in compile
		out[i] = "ext" + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26))
	}
	return out
}
