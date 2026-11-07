package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// Each vhost renderer refuses a param carrying nginx syntax before anything is
// written, and still renders the well-formed values the panel sends.

func requireInvalidArgument(t *testing.T, name string, err error) {
	t.Helper()
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument {
		t.Fatalf("%s: want InvalidArgument, got %v", name, err)
	}
}

func requireNothingWritten(t *testing.T, name, dir string) {
	t.Helper()
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("%s: a refused apply wrote %d files", name, len(entries))
	}
}

func TestWebmailVhostApply_RefusesNginxSyntaxInParams(t *testing.T) {
	av, _ := withDockerAppVhostTestDirs(t)
	base := func() webmailVhostApplyParams {
		return webmailVhostApplyParams{
			DomainName:    "shop.example.com",
			SSLCertPath:   "/etc/letsencrypt/live/shop.example.com/fullchain.pem",
			SSLKeyPath:    "/etc/letsencrypt/live/shop.example.com/privkey.pem",
			DocRoot:       "/home/alice/domains/shop.example.com/public_html",
			ListenIPv4:    "203.0.113.5",
			ListenIPv6:    "2001:db8::5",
			PanelHostname: "mx.example.net",
		}
	}
	for name, mutate := range map[string]func(*webmailVhostApplyParams){
		"comment in the domain":      func(p *webmailVhostApplyParams) { p.DomainName = "shop.example.com#x" },
		"directive end in cert path": func(p *webmailVhostApplyParams) { p.SSLCertPath = "/etc/ssl/a.pem; return 200" },
		"relative key path":          func(p *webmailVhostApplyParams) { p.SSLKeyPath = "privkey.pem" },
		"block end in doc root":      func(p *webmailVhostApplyParams) { p.DocRoot = "/home/alice/public_html;}" },
		"newline in doc root":        func(p *webmailVhostApplyParams) { p.DocRoot = "/home/alice/public_html\n" },
		"quote in panel hostname":    func(p *webmailVhostApplyParams) { p.PanelHostname = `mx.example.net"` },
		"newline in panel hostname":  func(p *webmailVhostApplyParams) { p.PanelHostname = "mx.example.net\nserver {" },
		"not an IPv4 listen address": func(p *webmailVhostApplyParams) { p.ListenIPv4 = "203.0.113.5:80 default_server" },
		"IPv6 in the IPv4 listen":    func(p *webmailVhostApplyParams) { p.ListenIPv4 = "2001:db8::5" },
		"IPv4 in the IPv6 listen":    func(p *webmailVhostApplyParams) { p.ListenIPv6 = "203.0.113.5" },
		"not an IPv6 listen address": func(p *webmailVhostApplyParams) { p.ListenIPv6 = "2001:db8::5]:443; listen [::" },
	} {
		p := base()
		mutate(&p)
		raw, _ := json.Marshal(p)
		_, err := webmailVhostApplyHandler(context.Background(), raw)
		requireInvalidArgument(t, name, err)
		requireNothingWritten(t, name, av)
	}

	raw, _ := json.Marshal(base())
	if _, err := webmailVhostApplyHandler(context.Background(), raw); err != nil {
		t.Fatalf("well-formed params: %v", err)
	}
}

func TestDockerAppVhostApply_RefusesNginxSyntaxInParams(t *testing.T) {
	av, _ := withDockerAppVhostTestDirs(t)
	base := func() dockerAppVhostApplyParams {
		return dockerAppVhostApplyParams{
			DomainName:  "forum.example.com",
			Upstream:    "http://127.0.0.1:10000",
			SSLCertPath: "/etc/ssl/x/fullchain.pem",
			SSLKeyPath:  "/etc/ssl/x/privkey.pem",
			ListenIPv4:  "203.0.113.5",
		}
	}
	for name, mutate := range map[string]func(*dockerAppVhostApplyParams){
		"comment in the domain":      func(p *dockerAppVhostApplyParams) { p.DomainName = "forum.example.com#" },
		"block start in cert path":   func(p *dockerAppVhostApplyParams) { p.SSLCertPath = "/etc/ssl/x/fullchain.pem{" },
		"variable in key path":       func(p *dockerAppVhostApplyParams) { p.SSLKeyPath = "/etc/ssl/$host/privkey.pem" },
		"not an IPv4 listen address": func(p *dockerAppVhostApplyParams) { p.ListenIPv4 = "203.0.113.5 default_server" },
		"IPv4 in the IPv6 listen":    func(p *dockerAppVhostApplyParams) { p.ListenIPv6 = "203.0.113.5" },
	} {
		p := base()
		mutate(&p)
		raw, _ := json.Marshal(p)
		_, err := dockerAppVhostApplyHandler(context.Background(), raw)
		requireInvalidArgument(t, name, err)
		requireNothingWritten(t, name, av)
	}

	raw, _ := json.Marshal(base())
	if _, err := dockerAppVhostApplyHandler(context.Background(), raw); err != nil {
		t.Fatalf("well-formed params: %v", err)
	}
}

func TestMailWebadminApply_RefusesNginxSyntaxInParams(t *testing.T) {
	av, _ := withDockerAppVhostTestDirs(t)
	origHt := stalwartAdminHtpasswd
	stalwartAdminHtpasswd = filepath.Join(t.TempDir(), "htpasswd")
	t.Cleanup(func() { stalwartAdminHtpasswd = origHt })
	for name, p := range map[string]map[string]any{
		"comment in the server name": {"server_name": "mx.example.com#"},
		"directive end in cert path": {"ssl_cert_path": "/etc/jabali/tls/panel.crt; ssl_verify_client off"},
		"relative key path":          {"ssl_key_path": "panel.key"},
	} {
		params := map[string]any{
			"enabled":       true,
			"server_name":   "mx.example.com",
			"port":          8449,
			"ssl_cert_path": "/etc/jabali/tls/panel.crt",
			"ssl_key_path":  "/etc/jabali/tls/panel.key",
		}
		for k, v := range p {
			params[k] = v
		}
		raw, _ := json.Marshal(params)
		_, err := mailWebadminApplyHandler(context.Background(), raw)
		requireInvalidArgument(t, name, err)
		requireNothingWritten(t, name, av)
	}
}
