package commands

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"text/template"
)

// TestVhostWordPressPrivateDirs: the folders WordPress plugins keep private
// with an Apache .htaccess (WooCommerce downloads and logs, FunnelKit data,
// All-in-One WP Migration backups) are never served as static files. The
// locations must come before every other regex location, or the static-cache
// or PHP regex would win for a .zip/.log/.php under them.
func TestVhostWordPressPrivateDirs(t *testing.T) {
	t.Parallel()
	tmpl := template.Must(template.New("vhost").Parse(vhostTemplate))
	render := func(vd vhostData) string {
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, vd); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	const internalLoc = "location ~ /wp-content/uploads/woocommerce_uploads/ { internal; }"
	const denyLoc = "location ~ /wp-content/(?:uploads/(?:wc-logs|funnelkit)|ai1wm-backups)/ { deny all; }"

	for name, vd := range map[string]vhostData{
		"php, cache on": {Domain: "a.com", DocRoot: "/home/u/a", HasPHP: true, Username: "u", IsEnabled: true, CacheEnabled: true, CachePath: "/", CacheTTLSeconds: 60},
		"static":        {Domain: "b.com", DocRoot: "/home/u/b", Username: "u", IsEnabled: true},
	} {
		out := render(vd)
		at, deny := strings.Index(out, internalLoc), strings.Index(out, denyLoc)
		if at < 0 || deny < 0 {
			t.Fatalf("%s: private-folder locations missing", name)
		}
		if first := regexp.MustCompile(`(?m)^\s*location ~\*? `).FindStringIndex(out); first == nil || first[0] > at {
			t.Errorf("%s: the woocommerce_uploads location must be the first regex location", name)
		}
		if deny < at {
			t.Errorf("%s: unexpected order", name)
		}
	}

	if rp := render(vhostData{Domain: "rp.com", DocRoot: "/home/u/r", HasPHP: true, Username: "u", IsEnabled: true, RootOverridden: true}); strings.Contains(rp, "woocommerce_uploads") {
		t.Errorf("a root-overridden (proxied) domain leaves these paths to its app")
	}

	// The location regexes (RE2-compatible as written) against request paths.
	internalRe := regexp.MustCompile(`/wp-content/uploads/woocommerce_uploads/`)
	denyRe := regexp.MustCompile(`/wp-content/(?:uploads/(?:wc-logs|funnelkit)|ai1wm-backups)/`)
	for path, want := range map[string]string{
		"/wp-content/uploads/woocommerce_uploads/2024/01/ebook.pdf":      "internal",
		"/shop/wp-content/uploads/woocommerce_uploads/x.zip":             "internal",
		"/wp-content/uploads/wc-logs/fatal-errors-2026-11-24-abc.log":    "deny",
		"/wp-content/uploads/funnelkit/transients/x.json":                "deny",
		"/wp-content/ai1wm-backups/site-20261124.wpress":                 "deny",
		"/blog/wp-content/ai1wm-backups/web.config":                      "deny",
		"/wp-content/uploads/2024/01/photo.jpg":                          "",
		"/wp-content/uploads/wc-logs-archive/readme.txt":                 "",
		"/wp-content/plugins/woocommerce/assets/js/frontend/cart.min.js": "",
	} {
		got := ""
		switch {
		case internalRe.MatchString(path):
			got = "internal"
		case denyRe.MatchString(path):
			got = "deny"
		}
		if got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}
