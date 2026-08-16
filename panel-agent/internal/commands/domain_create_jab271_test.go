package commands

import (
	"strings"
	"testing"
)

// JAB-271: the :80 vhost must serve the real docroot (never the stock
// "Welcome to nginx" page) whenever it is NOT force-redirecting to https —
// the CF-Flexible / grey-cloud / plain-http case. Regression for the
// fronted shape (serve :443, no :80 redirect) that fell through to nginx's
// compiled-in /usr/share/nginx/html.

func jab271Vhost(t *testing.T, redirect, serve bool) string {
	t.Helper()
	vd := canonicalCacheVhostData()
	vd.RedirectHTTPS = redirect
	vd.ServeHTTPS = serve
	vd.SSLCertPath = "/etc/ssl/x/fullchain.pem"
	vd.SSLKeyPath = "/etc/ssl/x/privkey.pem"
	return renderQAllowVhost(t, vd)
}

// port80Block returns the text of the first server block (the :80 one).
func port80Block(vhost string) string {
	i := strings.Index(vhost, "server {")
	if i < 0 {
		return ""
	}
	rest := vhost[i+len("server {"):]
	// the :80 block ends at the next "server {" (the :443 block) or EOF.
	if j := strings.Index(rest, "\nserver {"); j >= 0 {
		return rest[:j]
	}
	return rest
}

// Fronted: ServeHTTPS=true, RedirectHTTPS=false. :80 must serve the
// docroot (root + location /) and must NOT 301, and :443 must also serve.
func TestVhostJAB271_FrontedServesDocrootOn80(t *testing.T) {
	out := jab271Vhost(t, false, true)
	p80 := port80Block(out)
	if strings.Contains(p80, "return 301 https://") {
		t.Fatal(":80 must not 301 in the fronted (no-redirect) shape")
	}
	if !strings.Contains(p80, "root /home/u/public_html/example.com;") {
		t.Fatalf(":80 block has no docroot — the JAB-271 welcome-page bug:\n%s", p80)
	}
	if !strings.Contains(p80, "location / {") {
		t.Fatal(":80 block has no location / — falls through to the nginx default root")
	}
	// :443 still serves the docroot too.
	if !strings.Contains(out, "listen 443 ssl") {
		t.Fatal("fronted domain must still serve :443")
	}
	if strings.Count(out, "root /home/u/public_html/example.com;") < 2 {
		t.Fatal("docroot must appear on BOTH :80 and :443 for a fronted domain")
	}
}

// Direct + trusted cert: ServeHTTPS=true, RedirectHTTPS=true. :80 must
// 301 (force-https) and NOT serve the docroot; :443 serves it.
func TestVhostJAB271_DirectTrustedRedirects80(t *testing.T) {
	out := jab271Vhost(t, true, true)
	p80 := port80Block(out)
	if !strings.Contains(p80, "return 301 https://") {
		t.Fatal(":80 must 301 when RedirectHTTPS is set")
	}
	// disable_symlinks is servebody-unique (the ACME location also sets
	// `root DocRoot`, so the bare root directive is not a safe marker).
	if strings.Contains(p80, "disable_symlinks") {
		t.Fatal(":80 must NOT serve the docroot when force-redirecting")
	}
	if !strings.Contains(out, "listen 443 ssl") {
		t.Fatal("must serve :443")
	}
}

// Bootstrap: ServeHTTPS=false, RedirectHTTPS=false. :80 serves the
// docroot (self-signed :443 not served yet, #896); no :443 block.
func TestVhostJAB271_BootstrapServesDocrootOn80(t *testing.T) {
	out := jab271Vhost(t, false, false)
	p80 := port80Block(out)
	if !strings.Contains(p80, "root /home/u/public_html/example.com;") {
		t.Fatal("bootstrap :80 must serve the docroot")
	}
	if strings.Contains(out, "listen 443 ssl") {
		t.Fatal("bootstrap must NOT render a :443 block (no trusted cert yet)")
	}
}

// A DISABLED domain previously showed the stock welcome page on :80 too
// (no serving location at all). It must now show the branded 503 disabled
// page on :80 — the SAME servebody as :443 — never the stock nginx root.
func TestVhostJAB271_DisabledServes503On80(t *testing.T) {
	vd := canonicalCacheVhostData()
	vd.IsEnabled = false
	vd.RedirectHTTPS = false
	vd.ServeHTTPS = true
	vd.SSLCertPath = "/etc/ssl/x/fullchain.pem"
	vd.SSLKeyPath = "/etc/ssl/x/privkey.pem"
	out := renderQAllowVhost(t, vd)
	p80 := port80Block(out)
	if !strings.Contains(p80, "=503") {
		t.Fatalf("disabled :80 must serve the 503 disabled page, got:\n%s", p80)
	}
	if strings.Contains(p80, "root /usr/share/nginx/html") {
		t.Fatal("disabled :80 must not fall through to the stock nginx root")
	}
}

// The stock nginx default root must never appear in any shape.
func TestVhostJAB271_NeverStockRoot(t *testing.T) {
	for _, tc := range []struct{ redirect, serve bool }{{false, true}, {true, true}, {false, false}} {
		out := jab271Vhost(t, tc.redirect, tc.serve)
		// the directive, not the bare path (a code comment mentions the path).
		if strings.Contains(out, "root /usr/share/nginx/html") {
			t.Fatalf("stock nginx root leaked (redirect=%v serve=%v)", tc.redirect, tc.serve)
		}
	}
}
