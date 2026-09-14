package api

import (
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// TestValidatePageRedirects_InjectionHardening (GH #1624) pins the boundary
// guard that stops a tenant redirect source from injecting nginx directives.
// The wildcard branch of redirects.Compile renders the source UNQUOTED into a
// `rewrite ^<escapeRegex(source)>/?(.*)$` line; a literal space lets a tenant
// complete a two-argument rewrite and then inject a sibling directive (e.g. a
// proxy_pass to an internal service) inside their own location block, past
// validateProxyPassTarget. A redirect source is a URL path prefix and never
// needs whitespace, so the validator rejects it at the boundary.
func TestValidatePageRedirects_InjectionHardening(t *testing.T) {
	// Sources carrying the whitespace an injection depends on are rejected.
	for _, src := range []string{
		"/x y; return 204; #",                     // 2-arg rewrite + injected return
		"/a proxy_pass http://internal:9; #", // SSRF shape (neutral target)
		"/a\tb",                                   // tab separator
		"/a\rb",                                   // carriage return
	} {
		prs := models.PageRedirects{{Source: src, Destination: "http://ok.example", Type: "301", Wildcard: true}}
		if err := validatePageRedirects(prs); err == nil {
			t.Errorf("source %q carrying whitespace must be rejected", src)
		}
	}

	// Legitimate path-prefix sources (no whitespace) still pass, including a
	// wildcard redirect and one with regex-ish chars escapeRegex neutralizes.
	for _, src := range []string{"/old", "/old-path/sub", "/blog.php", "/a{2}"} {
		prs := models.PageRedirects{{Source: src, Destination: "https://ok.example/new", Type: "301", Wildcard: true}}
		if err := validatePageRedirects(prs); err != nil {
			t.Errorf("legitimate source %q should be allowed, got %v", src, err)
		}
	}
}
