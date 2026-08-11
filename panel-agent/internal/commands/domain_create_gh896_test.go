package commands

import (
	"strings"
	"testing"
)

// gh896VhostData is a minimal enabled PHP domain; the caller flips RedirectHTTPS
// and the cert paths to model the self-signed bootstrap vs the trusted-cert
// state.
func gh896VhostData(redirect bool) vhostData {
	vd := vhostData{
		Domain:         "example.com",
		DocRoot:        "/home/u/domains/example.com/public_html",
		Username:       "u",
		PHPVersion:     "8.3",
		FPMSocket:      "/run/php/jabali-u/fpm.sock",
		IndexDirective: "index index.php index.html;",
		IsEnabled:      true,
		HasPHP:         true,
		// A cert exists on disk in BOTH states (the self-signed placeholder is
		// still written); RedirectHTTPS is what gates the :443 + redirect.
		SSLCertPath:   "/etc/ssl/jabali-selfsigned/example.com/fullchain.pem",
		SSLKeyPath:    "/etc/ssl/jabali-selfsigned/example.com/privkey.pem",
		RedirectHTTPS: redirect,
		ServeHTTPS:    redirect, // pre-JAB-237 coupling: these tests pin the DIRECT-domain shape
	}
	return vd
}

// TestVhostGH896_SelfSignedBootstrapServesHTTP verifies the fix: with
// RedirectHTTPS=false (a fresh LE-mode domain still on its self-signed
// placeholder), the :80 vhost serves the docroot directly — no HTTP→HTTPS
// redirect, no :443 block — while still exposing the ACME challenge location.
func TestVhostGH896_SelfSignedBootstrapServesHTTP(t *testing.T) {
	out := mustRenderVhost(t, gh896VhostData(false))

	mustNotContain := map[string]string{
		"return 301 https":                     "must not redirect to HTTPS while self-signed",
		"listen 443 ssl":                       "must not open a :443 listener while self-signed",
		"listen [::]:443 ssl":                  "must not open a :443 IPv6 listener while self-signed",
		"ssl_certificate /etc/ssl/jabali-self": "must not reference the self-signed cert in a :443 block",
	}
	for needle, why := range mustNotContain {
		if strings.Contains(out, needle) {
			t.Errorf("%s — found %q in:\n%s", why, needle, out)
		}
	}

	mustContain := map[string]string{
		"location ^~ /.well-known/acme-challenge/":     "ACME challenge must still be served on :80",
		"root /home/u/domains/example.com/public_html": "docroot must be served on the :80 server",
		"listen 80;": "must listen on :80",
	}
	for needle, why := range mustContain {
		if !strings.Contains(out, needle) {
			t.Errorf("%s — missing %q in:\n%s", why, needle, out)
		}
	}
}

// TestVhostGH896_TrustedCertRedirects verifies the other half: once a trusted
// cert lands (RedirectHTTPS=true) the :80 vhost 301s to HTTPS and the :443
// block renders — the historical behavior, unchanged.
func TestVhostGH896_TrustedCertRedirects(t *testing.T) {
	out := mustRenderVhost(t, gh896VhostData(true))

	for _, needle := range []string{
		"return 301 https://$host$request_uri",
		"listen 443 ssl",
		"ssl_certificate /etc/ssl/jabali-selfsigned/example.com/fullchain.pem;",
		"location ^~ /.well-known/acme-challenge/",
	} {
		if !strings.Contains(out, needle) {
			t.Errorf("trusted-cert vhost missing %q in:\n%s", needle, out)
		}
	}
}
