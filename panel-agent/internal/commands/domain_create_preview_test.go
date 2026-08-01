package commands

import (
	"strings"
	"testing"
)

func previewVD() vhostData {
	return vhostData{
		Domain:      "example.com",
		DocRoot:     "/home/alice/domains/example.com/public_html",
		Username:    "alice",
		FPMSocket:   "/run/php/jabali-alice/fpm.sock",
		HasPHP:      true,
		IsEnabled:   true,
		PreviewHost: "example-com.preview.host.tld",
	}
}

// The preview block renders as its own server pair serving the SAME
// docroot, marked noindex, and never inherits the main vhost's cache /
// rate-limit / ACL machinery.
func TestVhostTemplate_PreviewBlock(t *testing.T) {
	out := mustRenderVhost(t, previewVD())

	if got := strings.Count(out, "server_name example-com.preview.host.tld;"); got != 1 {
		t.Fatalf("preview server_name should appear exactly once (HTTP-only, no cert), got %d:\n%s", got, out)
	}
	if !strings.Contains(out, `add_header X-Robots-Tag "noindex, nofollow" always;`) {
		t.Error("preview block must be noindex — temp URLs must not enter search results")
	}
	if !strings.Contains(out, "/var/log/nginx/example.com-preview-access.log") {
		t.Error("preview block must log separately from the main vhost")
	}
	// PHP passthrough uses the domain's own pool socket.
	if !strings.Contains(out, "fastcgi_pass unix:/run/php/jabali-alice/fpm.sock;") {
		t.Error("preview php location must use the domain's FPM socket")
	}
}

func TestVhostTemplate_PreviewTLSWhenCertPresent(t *testing.T) {
	vd := previewVD()
	vd.PreviewCertPath = "/etc/letsencrypt/live/wildcard.preview.host.tld/fullchain.pem"
	vd.PreviewKeyPath = "/etc/letsencrypt/live/wildcard.preview.host.tld/privkey.pem"
	out := mustRenderVhost(t, vd)

	if got := strings.Count(out, "server_name example-com.preview.host.tld;"); got != 2 {
		t.Fatalf("with a cert the preview needs HTTP redirect + HTTPS blocks (2 server_name lines), got %d", got)
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
	out := mustRenderVhost(t, vd)

	for _, forbidden := range []string{"preview", "X-Robots-Tag"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("vhost without preview_host must not contain %q", forbidden)
		}
	}
}
