package commands

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"text/template"
)

// Gitea #416/#417/#419: the page-cache gate must be fail-CLOSED (default skip,
// allow only no-cookie / benign-analytics) and must NOT advertise public/CDN
// caching for dynamic PHP responses.
func TestVhostTemplate_PageCacheIsFailClosed(t *testing.T) {
	t.Parallel()
	tmpl, err := template.New("vhost").Parse(vhostTemplate)
	if err != nil {
		t.Fatal(err)
	}
	vd := vhostData{
		Domain: "example.com", DocRoot: "/home/u/public_html/example.com",
		HasPHP: true, PHPVersion: "8.3", Username: "u", IsEnabled: true,
		CacheEnabled: true, CacheKeyZone: "jabali", CacheTTL: "60s",
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vd); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "set $jabali_skip 1;") {
		t.Error("page cache must default to skip (fail-closed)")
	}
	if !strings.Contains(out, "_ga") || !strings.Contains(out, "wordpress_test_cookie") {
		t.Error("missing benign-cookie allowlist")
	}
	// PHP responses must be private/no-cache, never public to clients/CDNs.
	if !strings.Contains(out, "private, no-cache, max-age=0") {
		t.Error("PHP block must emit private/no-cache Cache-Control")
	}
	if strings.Contains(out, "set $jabali_cc") {
		t.Error("PHP block must not advertise public/CDN caching for dynamic responses")
	}
}

// Gitea #420: the page cache must be scoped to the install path prefix.
func TestVhostTemplate_PathScopedCache(t *testing.T) {
	t.Parallel()
	tmpl := template.Must(template.New("v").Parse(vhostTemplate))
	// The gate is now driven by CacheGate (buildCacheGate output), computed in
	// writeVhost from cache_path/cache_paths. Render with the same computation.
	render := func(paths []string, cachePath string) string {
		var b bytes.Buffer
		_ = tmpl.Execute(&b, vhostData{
			Domain: "ex.com", DocRoot: "/home/u/public_html/ex.com", HasPHP: true,
			PHPVersion: "8.3", Username: "u", IsEnabled: true,
			CacheEnabled: true, CacheKeyZone: "jabali", CacheTTL: "60s",
			CachePath: cachePath, CacheGate: buildCacheGate(paths, cachePath),
		})
		return b.String()
	}
	// whole-domain ("/" or empty, or any union containing a root install): no gate.
	for _, root := range []string{"/", ""} {
		if strings.Contains(render(nil, root), `!~ "^`) {
			t.Errorf("CachePath=%q must not emit a path gate", root)
		}
	}
	if strings.Contains(render([]string{"/blog", "/"}, "/"), `!~ "^`) {
		t.Error("a root install in the union must drop the gate (whole domain)")
	}
	// single subdir: gate scopes to that prefix.
	if !strings.Contains(render(nil, "/blog"), `if ($jabali_cache_path !~ "^(/blog)(/|$)") { set $jabali_skip 1; }`) {
		t.Errorf("CachePath=/blog must scope cache to ^(/blog)\n%s", render(nil, "/blog"))
	}
	// GH #601 multi-path: union of two subdir installs → alternation gate.
	if !strings.Contains(render([]string{"/blog", "/shop"}, "/"), `if ($jabali_cache_path !~ "^(/blog|/shop)(/|$)") { set $jabali_skip 1; }`) {
		t.Errorf("cache_paths [/blog /shop] must gate ^(/blog|/shop)\n%s", render([]string{"/blog", "/shop"}, "/"))
	}
}

// SECURITY (nginx template/config injection): the agent must reject any cache
// path that isn't "/" or a single safe segment, falling back to "/".
func TestSanitizeCachePath(t *testing.T) {
	safe := map[string]string{"/": "/", "/blog": "/blog", "/shop2": "/shop2", "": "/"}
	for in, want := range safe {
		if got := sanitizeCachePath(in); got != want {
			t.Errorf("sanitizeCachePath(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{
		`/a" { return 444; } location /x {`, "/a/b", "/UP", "/a;b", "/a\n}", "/../etc", "/a ", "blog", "/-x",
	} {
		if got := sanitizeCachePath(bad); got != "/" {
			t.Errorf("sanitizeCachePath(%q) = %q, want \"/\" (unsafe must fall back)", bad, got)
		}
	}
}

// GH #616: a sanitized URL-exclusion suffix must appear inside the built-in
// bypass regex (both PHP location blocks), so those paths always bypass.
func TestVhostTemplate_ExtraBypass(t *testing.T) {
	t.Parallel()
	tmpl := template.Must(template.New("v").Parse(vhostTemplate))
	var b bytes.Buffer
	_ = tmpl.Execute(&b, vhostData{
		Domain: "ex.com", DocRoot: "/home/u/public_html/ex.com", HasPHP: true,
		PHPVersion: "8.3", Username: "u", IsEnabled: true,
		CacheEnabled: true, CacheKeyZone: "jabali", CacheTTL: "60s",
		CacheExtraBypass: sanitizeBypassPaths([]string{"/private", "/members"}),
	})
	out := b.String()
	// both location blocks carry the merged bypass regex
	if n := strings.Count(out, `/edd-api/|/private|/members") { set $jabali_skip 1; }`); n != 2 {
		t.Errorf("extra bypass folded into %d blocks, want 2\n%s", n, out)
	}
}

// WooCommerce's order attribution (on by default since WC 8.5) sets sbjs_*
// cookies from JS on the first page view; its PHP never reads them. They must
// not turn every shop visitor into a bypass from the second page on, while a
// cart, session or login cookie next to them still bypasses.
func TestVhostTemplate_CookieGateAllowsWooAttributionCookies(t *testing.T) {
	t.Parallel()
	out := renderQAllowVhost(t, canonicalCacheVhostData())
	m := regexp.MustCompile(`if \(\$http_cookie ~\* "([^"]+)"\) \{ set \$jabali_skip 0; \}`).FindStringSubmatch(out)
	if m == nil {
		t.Fatal("cookie gate not found in the rendered vhost")
	}
	gate := regexp.MustCompile("(?i)" + m[1]) // ~* is case-insensitive
	for cookie, cacheable := range map[string]bool{
		"sbjs_migrations=1418474375998%3D1; sbjs_current_add=fd%3D2026; sbjs_first=typ%3Dtypein; sbjs_udata=vst%3D1; sbjs_session=pgs%3D1": true,
		"_ga=GA1.1.1.1; sbjs_current=typ%3Dorganic":                      true,
		"sbjs_session=pgs%3D2; woocommerce_items_in_cart=1":              false,
		"sbjs_first=typ%3Dtypein; wp_woocommerce_session_abc=1%7C%7C2":   false,
		"sbjs_first=typ%3Dtypein; wordpress_logged_in_abc=u%7C1%7Ct%7Ch": false,
		"pys_session_limit=true; sbjs_first=typ%3Dtypein":                false,
		"cmplz_marketing=allow":                                          false,
	} {
		if got := gate.MatchString(cookie); got != cacheable {
			t.Errorf("cookie %q: cacheable=%v, want %v", cookie, got, cacheable)
		}
	}
}

// With the cache on, the static-asset and .html locations win over location /.
// A file that isn't on disk must still reach the app, as with the cache off:
// plugin-served virtual .js/.ico/images and .html permalinks are real URLs.
func TestVhostTemplate_CacheStaticMissFallsBackToApp(t *testing.T) {
	t.Parallel()
	block := func(out, open string) string {
		i := strings.Index(out, open)
		if i < 0 {
			t.Fatalf("location %q not rendered", open)
		}
		return out[i : i+strings.Index(out[i:], "}")]
	}
	const assets = `location ~* \.(?:css|js|jpe?g|png|gif|ico|svg|webp|woff2?|ttf|eot)$ {`
	const html = `location ~* \.html?$ {`
	const fallback = "try_files $uri $uri/ /index.php?$query_string;"

	php := renderQAllowVhost(t, canonicalCacheVhostData())
	for _, loc := range []string{assets, html} {
		if !strings.Contains(block(php, loc), fallback) {
			t.Errorf("PHP domain: %s must fall back to the front controller", loc)
		}
	}

	static := canonicalCacheVhostData()
	static.HasPHP = false
	overridden := canonicalCacheVhostData()
	overridden.RootOverridden = true
	for name, vd := range map[string]vhostData{"no PHP": static, "root overridden": overridden} {
		out := renderQAllowVhost(t, vd)
		for _, loc := range []string{assets, html} {
			if strings.Contains(block(out, loc), "try_files") {
				t.Errorf("%s: %s must keep the plain static 404", name, loc)
			}
		}
	}
}
