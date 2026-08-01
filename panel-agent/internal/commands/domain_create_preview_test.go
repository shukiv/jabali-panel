package commands

import (
	"strings"
	"testing"
)

func previewVD() vhostData {
	return vhostData{
		Domain:          "example.com",
		DocRoot:         "/home/alice/domains/example.com/public_html",
		Username:        "alice",
		IsEnabled:       true,
		SSLCertPath:     "/etc/letsencrypt/live/example.com/fullchain.pem",
		SSLKeyPath:      "/etc/letsencrypt/live/example.com/privkey.pem",
		PreviewHost:     "example-com.preview.host.tld",
		PreviewUpstream: "https://127.0.0.1:443",
	}
}

// The preview block reverse-proxies the domain's own vhost with the real
// Host header (so canonical-URL apps never redirect away) and rewrites
// Location headers, cookie domains, and absolute body URLs — including
// the JSON-escaped form Elementor stores — to the preview host.
func TestVhostTemplate_PreviewProxyBlock(t *testing.T) {
	out := mustRenderVhost(t, previewVD())

	if got := strings.Count(out, "server_name example-com.preview.host.tld;"); got != 1 {
		t.Fatalf("preview server_name should appear exactly once (no front cert yet), got %d:\n%s", got, out)
	}
	for _, want := range []string{
		"proxy_pass https://127.0.0.1:443;",
		"proxy_set_header Host example.com;",
		"proxy_ssl_server_name on;",
		"proxy_ssl_name example.com;",
		"proxy_redirect https://example.com https://$http_host;",
		"proxy_redirect https://www.example.com https://$http_host;",
		"proxy_cookie_domain example.com $host;",
		`sub_filter 'https://example.com' 'https://$http_host';`,
		`sub_filter 'https:\/\/example.com' 'https:\/\/$http_host';`,
		"sub_filter_once off;",
		`proxy_set_header Accept-Encoding "";`,
		"gunzip on;",
		`add_header X-Robots-Tag "noindex, nofollow" always;`,
		"/var/log/nginx/example.com-preview-access.log",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("preview block missing %q", want)
		}
	}
	// The preview block must not serve the docroot directly anymore.
	if strings.Contains(out, "location ~ \\.php$ {\n        try_files $uri =404;\n        fastcgi_pass") &&
		strings.Count(out, "root "+previewVD().DocRoot) > 1 {
		t.Error("preview block must proxy, not serve the docroot via fastcgi")
	}
}

func TestVhostTemplate_PreviewHTTPUpstreamWhenNoBackendTLS(t *testing.T) {
	vd := previewVD()
	vd.SSLCertPath = ""
	vd.SSLKeyPath = ""
	vd.PreviewUpstream = "http://127.0.0.1:80"
	out := mustRenderVhost(t, vd)

	if !strings.Contains(out, "proxy_pass http://127.0.0.1:80;") {
		t.Error("no backend TLS -> proxy over plain http")
	}
	if strings.Contains(out, "proxy_ssl_name") {
		t.Error("proxy_ssl_name must not render for an http upstream")
	}
}

func TestVhostTemplate_PreviewFrontTLSWhenCertPresent(t *testing.T) {
	vd := previewVD()
	vd.PreviewCertPath = "/etc/letsencrypt/live/wildcard.preview.host.tld/fullchain.pem"
	vd.PreviewKeyPath = "/etc/letsencrypt/live/wildcard.preview.host.tld/privkey.pem"
	out := mustRenderVhost(t, vd)

	if got := strings.Count(out, "server_name example-com.preview.host.tld;"); got != 2 {
		t.Fatalf("with a front cert the preview needs HTTP-redirect + HTTPS blocks (2 server_name lines), got %d", got)
	}
	if !strings.Contains(out, "ssl_certificate /etc/letsencrypt/live/wildcard.preview.host.tld/fullchain.pem;") {
		t.Error("preview 443 block must reference the shared wildcard pair")
	}
}

// No preview host -> byte-identical absence: not a single preview artifact
// may leak into a regular vhost.
func TestVhostTemplate_NoPreviewNoArtifacts(t *testing.T) {
	vd := previewVD()
	vd.PreviewHost = ""
	vd.PreviewUpstream = ""
	out := mustRenderVhost(t, vd)

	for _, forbidden := range []string{"preview", "X-Robots-Tag", "sub_filter", "proxy_cookie_domain"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("vhost without preview_host must not contain %q", forbidden)
		}
	}
}
