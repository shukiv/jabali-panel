package appseccfg

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const panelPaths = `(req.URL.Path startsWith "/api/v1/" || req.URL.Path startsWith "/phpmyadmin/" || req.URL.Path startsWith "/jabali-adminer/" || req.URL.Path startsWith "/dav/")`

// The panel is reached as https://<host>:8443/, and the bouncer hands AppSec
// the Host header as the client sent it, port and all. The scope names the
// host bare and with the port, and the same for each address.
func TestPanelHostForms(t *testing.T) {
	for name, tc := range map[string]struct {
		host  string
		addrs []string
		want  []string
	}{
		"host":            {"panel.example.com", nil, []string{"panel.example.com", "panel.example.com:8443"}},
		"host upper case": {" Panel.Example.COM ", nil, []string{"panel.example.com", "panel.example.com:8443"}},
		"single label":    {"panel", nil, []string{"panel", "panel:8443"}},
		"addresses sorted, deduped": {"panel.example.com", []string{"203.0.113.9", "198.51.100.7", "203.0.113.9"},
			[]string{"panel.example.com", "panel.example.com:8443", "198.51.100.7", "198.51.100.7:8443", "203.0.113.9", "203.0.113.9:8443"}},
		// Never part of the expression: quotes, spaces, operators.
		"injected host":    {`x" || true || "y`, nil, nil},
		"injected address": {"panel.example.com", []string{`1.2.3.4" || true || "`}, []string{"panel.example.com", "panel.example.com:8443"}},
		"IPv6 left out":    {"panel.example.com", []string{"2001:db8::1"}, []string{"panel.example.com", "panel.example.com:8443"}},
		"address as host":  {"203.0.113.5", nil, nil},
	} {
		if got := PanelHostForms(tc.host, tc.addrs); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

// The on_match allowlist skips the panel's paths only for Host values that
// name the panel. nginx routes by Host, so none of them reaches a hosted
// site's server block.
func TestRender_PanelAllowlistScopedToPanelHostForms(t *testing.T) {
	out := Render(Opts{AdminAllowlist: true, PanelHost: "panel.example.com", PanelAddrs: []string{"203.0.113.5"}})
	want := " - filter: " + panelPaths +
		` && req.Host in ["panel.example.com", "panel.example.com:8443", "203.0.113.5", "203.0.113.5:8443"]` + "\n" +
		"   apply:\n    - CancelEvent()\n    - CancelAlert()\n    - SetRemediation(\"allow\")\n"
	if !strings.Contains(out, want) {
		t.Fatalf("panel allowlist not scoped to the panel's Host forms:\n%s", out)
	}
	if strings.Contains(out, " - filter: req.URL.Path startsWith") {
		t.Fatalf("a path-only filter is left:\n%s", out)
	}
}

// A panel host that is no valid name, with no address, scopes the skips to
// nothing.
func TestRender_InvalidPanelHostSkipsNothing(t *testing.T) {
	out := Render(Opts{AdminAllowlist: true, PanelHost: `x" || true || "y`})
	if !strings.Contains(out, " - filter: "+panelPaths+" && false\n") {
		t.Fatalf("want the panel allowlist to match nothing:\n%s", out)
	}
	if strings.Contains(out, "true ||") {
		t.Fatalf("the host reached the expression:\n%s", out)
	}
	exempt := RenderBotExempt(Opts{PanelHost: `x" || true || "y`})
	if !strings.Contains(exempt, `req.URL.Path startsWith "/dav/") && false`+"\n") {
		t.Fatalf("want the panel exemption to match nothing:\n%s", exempt)
	}
}

// Only an address jabali's default vhost holds as default_server on :443
// names the panel: a Host of that address reaches jabali's server there. An
// address a hosted site listens on without jabali's default_server, a comment,
// :80 and IPv6 do not count.
func TestDefaultServerAddrs(t *testing.T) {
	conf := `server {
    listen 80 default_server;
    listen 203.0.113.5:80 default_server;
    # listen 198.51.100.9:443 ssl default_server;
}
server {
    listen 443 ssl default_server http2;
    listen [::]:443 ssl default_server http2;
    listen 203.0.113.5:443 ssl default_server http2;
    listen [2001:db8::1]:443 ssl default_server;
    server_name _;
}
server {
    listen 203.0.113.5:443 ssl http2;
    listen 192.0.2.7:443 ssl http2;
    server_name panel.example.com;
}
server {
	listen 198.51.100.20:443 ssl default_server;
}
`
	path := filepath.Join(t.TempDir(), "jabali-default.conf")
	if err := os.WriteFile(path, []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	want := []string{"198.51.100.20", "203.0.113.5"}
	if got := DefaultServerAddrs(path); !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := DefaultServerAddrs(filepath.Join(t.TempDir(), "absent.conf")); got != nil {
		t.Errorf("missing file: got %q, want none", got)
	}
}
