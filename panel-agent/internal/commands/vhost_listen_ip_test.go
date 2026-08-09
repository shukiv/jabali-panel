package commands

import (
	"bytes"
	"strings"
	"testing"
	"text/template"
)

// renderVhost executes vhostTemplate with sane defaults and returns the
// output — used by the Step 6 listen-IP tests so each case can focus on
// the listen directives alone.
func renderVhost(t *testing.T, v4, v6 string, ssl bool) string {
	t.Helper()
	tmpl, err := template.New("vhost").Parse(vhostTemplate)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	vd := vhostData{
		Domain:         "example.com",
		DocRoot:        "/home/testuser/public_html/example.com",
		Username:       "testuser",
		PHPVersion:     "8.3",
		IndexDirective: "index index.html index.php;",
		IsEnabled:      true,
		HasPHP:         true,
		ListenIPv4:     v4,
		ListenIPv6:     v6,
	}
	if ssl {
		vd.SSLCertPath = "/etc/ssl/example.com.pem"
		vd.SSLKeyPath = "/etc/ssl/example.com.key"
		// GH #896: the redirect + :443 block is now gated on RedirectHTTPS
		// (a trusted cert), not merely on SSLCertPath. ssl=true here models a
		// domain serving HTTPS, so set it.
		vd.RedirectHTTPS = true
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vd); err != nil {
		t.Fatalf("execute: %v", err)
	}
	return buf.String()
}

// assertContainsNotContains is a tiny helper that keeps the table-driven
// tests readable. Each wantIn line must appear; each wantOut line must not.
func assertContainsNotContains(t *testing.T, out string, wantIn, wantOut []string) {
	t.Helper()
	for _, needle := range wantIn {
		if !strings.Contains(out, needle) {
			t.Errorf("expected output to contain %q, got:\n%s", needle, out)
		}
	}
	for _, bad := range wantOut {
		if strings.Contains(out, bad) {
			t.Errorf("expected output NOT to contain %q, got:\n%s", bad, out)
		}
	}
}

// TestVhostListenIP_NoBinding covers the pre-M24 fallback: both fields
// empty, both listen lines use the all-interfaces form. This is the
// default when the panel's DomainHandlerConfig.ManagedIPs is unset OR
// when the admin hasn't picked a specific IP for the domain.
func TestVhostListenIP_NoBinding(t *testing.T) {
	out := renderVhost(t, "", "", false)
	assertContainsNotContains(t, out,
		[]string{"listen 80;", "listen [::]:80;"},
		[]string{"listen :80", "listen []:80"},
	)
}

// TestVhostListenIP_V4Only covers the admin binding a v4 IP but
// leaving v6 unset. The v4 line gets the explicit address, the v6
// line stays on [::]:80.
func TestVhostListenIP_V4Only(t *testing.T) {
	out := renderVhost(t, "203.0.113.50", "", false)
	assertContainsNotContains(t, out,
		[]string{"listen 203.0.113.50:80;", "listen [::]:80;"},
		[]string{"listen 80;", "listen :80", "listen []:80"},
	)
}

// TestVhostListenIP_V6Only covers the inverse — v6 bound, v4 falls
// back to all-interfaces.
func TestVhostListenIP_V6Only(t *testing.T) {
	out := renderVhost(t, "", "2001:db8::99", false)
	assertContainsNotContains(t, out,
		[]string{"listen 80;", "listen [2001:db8::99]:80;"},
		[]string{"listen [::]:80;", "listen []:80"},
	)
}

// TestVhostListenIP_Both covers a fully-pinned domain. Both listen
// lines carry the explicit addresses; the bracketed [::] fallback
// must not appear anywhere.
func TestVhostListenIP_Both(t *testing.T) {
	out := renderVhost(t, "203.0.113.50", "2001:db8::99", false)
	assertContainsNotContains(t, out,
		[]string{"listen 203.0.113.50:80;", "listen [2001:db8::99]:80;"},
		[]string{"listen 80;", "listen [::]:80;"},
	)
}

// TestVhostListenIP_SSLBlockMirrorsHTTP verifies the HTTPS block
// renders the same listen variant as the HTTP block. The 443 lines
// must gain the IP prefix when set, and keep the fallback otherwise.
func TestVhostListenIP_SSLBlockMirrorsHTTP(t *testing.T) {
	out := renderVhost(t, "203.0.113.50", "2001:db8::99", true)
	assertContainsNotContains(t, out,
		[]string{
			"listen 203.0.113.50:80;",
			"listen [2001:db8::99]:80;",
			"listen 203.0.113.50:443 ssl",
			"listen [2001:db8::99]:443 ssl",
		},
		[]string{"listen 443 ssl", "listen [::]:443 ssl"},
	)
}

// TestVhostListenIP_SSLBlockFallback verifies the HTTPS block's
// fallback form when no IP is pinned.
func TestVhostListenIP_SSLBlockFallback(t *testing.T) {
	out := renderVhost(t, "", "", true)
	assertContainsNotContains(t, out,
		[]string{"listen 443 ssl", "listen [::]:443 ssl"},
		[]string{"listen :443", "listen []:443"},
	)
}
