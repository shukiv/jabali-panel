package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withDockerAppVhostTestDirs(t *testing.T) (string, string) {
	t.Helper()
	av := t.TempDir()
	en := t.TempDir()
	origAv, origEn, origReload := mailVhostSitesAvailable, mailVhostSitesEnabled, nginxTestAndReload
	mailVhostSitesAvailable = av
	mailVhostSitesEnabled = en
	nginxTestAndReload = func(context.Context) error { return nil }
	t.Cleanup(func() {
		mailVhostSitesAvailable = origAv
		mailVhostSitesEnabled = origEn
		nginxTestAndReload = origReload
	})
	return av, en
}

func TestDockerAppVhostApply_RendersProxyVhost(t *testing.T) {
	av, en := withDockerAppVhostTestDirs(t)

	params, _ := json.Marshal(dockerAppVhostApplyParams{
		DomainName:  "forum.example.com",
		Upstream:    "http://127.0.0.1:10000",
		SSLCertPath: "/etc/ssl/x/fullchain.pem",
		SSLKeyPath:  "/etc/ssl/x/privkey.pem",
		Websocket:   true,
		ListenIPv4:  "203.0.113.5",
	})
	resp, err := dockerAppVhostApplyHandler(context.Background(), params)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if r, ok := resp.(webmailVhostResponse); !ok || !r.Ok || !r.Changed {
		t.Fatalf("unexpected resp: %+v", resp)
	}

	conf := filepath.Join(av, "forum.example.com-dockerapp.conf")
	b, err := os.ReadFile(conf)
	if err != nil {
		t.Fatalf("read conf: %v", err)
	}
	got := string(b)
	for _, want := range []string{
		"server_name forum.example.com;",
		"proxy_pass http://127.0.0.1:10000;",
		`proxy_set_header Connection "upgrade";`, // websocket on
		"listen 203.0.113.5:443 ssl",
		"ssl_certificate /etc/ssl/x/fullchain.pem;",
		"location ^~ /.well-known/acme-challenge/",
		"return 301 https://$host$request_uri;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered vhost missing %q\n---\n%s", want, got)
		}
	}
	// Symlink must be enabled.
	if _, err := os.Lstat(filepath.Join(en, "forum.example.com-dockerapp.conf")); err != nil {
		t.Errorf("enabled symlink missing: %v", err)
	}
}

// TestDockerAppVhostApply_GzipsProxiedResponses guards JAB-406: the proxy-only
// vhost must compress upstream responses. gzip_proxied defaults to `off`, so
// `gzip on;` ALONE reproduces the bug (all proxied responses skipped) — the
// load-bearing directive is `gzip_proxied any;`. The block must live in the
// :443 server block (which does the proxying), not the :80 redirect block.
func TestDockerAppVhostApply_GzipsProxiedResponses(t *testing.T) {
	av, _ := withDockerAppVhostTestDirs(t)

	params, _ := json.Marshal(dockerAppVhostApplyParams{
		DomainName:  "app.example.com",
		Upstream:    "http://127.0.0.1:10001",
		SSLCertPath: "/c", SSLKeyPath: "/k",
	})
	if _, err := dockerAppVhostApplyHandler(context.Background(), params); err != nil {
		t.Fatalf("apply: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(av, "app.example.com-dockerapp.conf"))
	if err != nil {
		t.Fatalf("read conf: %v", err)
	}
	got := string(b)

	for _, want := range []string{
		"gzip on;",
		"gzip_proxied any;", // load-bearing: `gzip on` alone still skips proxied responses (the JAB-406 bug)
		"gzip_vary on;",
		"gzip_min_length 1024;",
		"gzip_types application/javascript text/javascript text/css application/json image/svg+xml application/manifest+json;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered vhost missing %q\n---\n%s", want, got)
		}
	}

	// The gzip block must be in the :443 server (the one that proxies), not the
	// :80 redirect block. The :80 block is the only one with the 301 redirect,
	// so gzip_proxied must appear before it.
	gz := strings.Index(got, "gzip_proxied any;")
	redirect := strings.Index(got, "return 301 https://")
	if gz < 0 || redirect < 0 || gz > redirect {
		t.Errorf("gzip_proxied must render inside the :443 server block (before the :80 redirect); gz=%d redirect=%d\n---\n%s", gz, redirect, got)
	}
}

func TestDockerAppVhostApply_NoWebsocketOmitsUpgrade(t *testing.T) {
	av, _ := withDockerAppVhostTestDirs(t)
	params, _ := json.Marshal(dockerAppVhostApplyParams{
		DomainName: "f.example.com", Upstream: "http://127.0.0.1:8080",
		SSLCertPath: "/c", SSLKeyPath: "/k", Websocket: false,
	})
	if _, err := dockerAppVhostApplyHandler(context.Background(), params); err != nil {
		t.Fatalf("apply: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(av, "f.example.com-dockerapp.conf"))
	if strings.Contains(string(b), `Connection "upgrade"`) {
		t.Errorf("websocket=false should not emit upgrade headers")
	}
}

func TestDockerAppVhostApply_RejectsBadUpstream(t *testing.T) {
	withDockerAppVhostTestDirs(t)
	for _, up := range []string{
		"http://10.0.0.1:10000",         // not loopback
		"http://127.0.0.1:10000/inject", // path
		"https://127.0.0.1:10000",       // https
		"http://127.0.0.1",              // no port
		"http://evil.com:80",            // arbitrary host
		"",                              // empty
	} {
		params, _ := json.Marshal(dockerAppVhostApplyParams{
			DomainName: "f.example.com", Upstream: up,
			SSLCertPath: "/c", SSLKeyPath: "/k",
		})
		if _, err := dockerAppVhostApplyHandler(context.Background(), params); err == nil {
			t.Errorf("upstream %q should be rejected", up)
		}
	}
}

func TestDockerAppVhostApply_RequiresCert(t *testing.T) {
	withDockerAppVhostTestDirs(t)
	params, _ := json.Marshal(dockerAppVhostApplyParams{
		DomainName: "f.example.com", Upstream: "http://127.0.0.1:9000",
	})
	if _, err := dockerAppVhostApplyHandler(context.Background(), params); err == nil {
		t.Errorf("missing cert paths should be rejected")
	}
}

func TestDockerAppVhostRemove_Idempotent(t *testing.T) {
	av, en := withDockerAppVhostTestDirs(t)
	apply, _ := json.Marshal(dockerAppVhostApplyParams{
		DomainName: "f.example.com", Upstream: "http://127.0.0.1:9000",
		SSLCertPath: "/c", SSLKeyPath: "/k",
	})
	if _, err := dockerAppVhostApplyHandler(context.Background(), apply); err != nil {
		t.Fatalf("apply: %v", err)
	}
	rm, _ := json.Marshal(dockerAppVhostRemoveParams{DomainName: "f.example.com"})
	resp, err := dockerAppVhostRemoveHandler(context.Background(), rm)
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if r := resp.(webmailVhostResponse); !r.Changed {
		t.Errorf("first remove should report changed")
	}
	if _, err := os.Stat(filepath.Join(av, "f.example.com-dockerapp.conf")); !os.IsNotExist(err) {
		t.Errorf("conf should be gone")
	}
	if _, err := os.Lstat(filepath.Join(en, "f.example.com-dockerapp.conf")); !os.IsNotExist(err) {
		t.Errorf("symlink should be gone")
	}
	// Second remove is a no-op (idempotent).
	resp2, err := dockerAppVhostRemoveHandler(context.Background(), rm)
	if err != nil {
		t.Fatalf("second remove: %v", err)
	}
	if r := resp2.(webmailVhostResponse); r.Changed {
		t.Errorf("second remove should report no change")
	}
}
