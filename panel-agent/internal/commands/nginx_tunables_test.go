package commands

import (
	"strings"
	"testing"
)

func TestBuildNginxTunablesFragment(t *testing.T) {
	p := &nginxTunablesApplyParams{
		ClientMaxBodySize:   "50m",
		ClientBodyTimeout:   "60s",
		ClientHeaderTimeout: "60s",
		SendTimeout:         "60s",
		ProxyConnectTimeout: "300s",
		ProxyReadTimeout:    "300s",
		ProxySendTimeout:    "300s",
		CustomHTTP:          "add_header X-Demo 1;",
	}
	got := BuildNginxTunablesFragment(p, nil)
	for _, want := range []string{
		"client_max_body_size 50m;",
		"client_body_timeout 60s;",
		"proxy_read_timeout 300s;",
		"# --- Admin custom directives",
		"add_header X-Demo 1;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("fragment missing %q\n--- got ---\n%s", want, got)
		}
	}
	// server_tokens / gzip / keepalive_timeout appear only when passed as
	// httpLines (nothing else declares them, nginx_tunables_http_scope_test.go);
	// otherwise they'd duplicate nginx.conf's declarations.
	for _, banned := range []string{"server_tokens", "gzip", "keepalive_timeout"} {
		if strings.Contains(got, banned) {
			t.Errorf("fragment must NOT contain %q (it's patched into nginx.conf):\n%s", banned, got)
		}
	}
}

func TestBuildNginxTunablesFragment_EmptyCustom(t *testing.T) {
	got := BuildNginxTunablesFragment(&nginxTunablesApplyParams{ClientMaxBodySize: "50m"}, nil)
	if strings.Contains(got, "Admin custom directives") {
		t.Errorf("empty custom snippet should not emit Advanced header:\n%s", got)
	}
}

func TestValidateNginxTunables(t *testing.T) {
	cases := []struct {
		name    string
		p       nginxTunablesApplyParams
		wantErr bool
	}{
		{"valid", nginxTunablesApplyParams{ClientMaxBodySize: "50m", KeepaliveTimeout: "65s", WorkerProcesses: "auto", WorkerConnections: 1024}, false},
		{"valid bare seconds", nginxTunablesApplyParams{ProxyReadTimeout: "300"}, false},
		{"valid size 0", nginxTunablesApplyParams{ClientMaxBodySize: "0"}, false},
		{"bad size", nginxTunablesApplyParams{ClientMaxBodySize: "50mb"}, true},
		{"size injection", nginxTunablesApplyParams{ClientMaxBodySize: "50m; }"}, true},
		{"bad time", nginxTunablesApplyParams{KeepaliveTimeout: "65sec"}, true},
		{"bad worker_processes", nginxTunablesApplyParams{WorkerProcesses: "lots"}, true},
		{"worker_connections too high", nginxTunablesApplyParams{WorkerConnections: 2000000}, true},
		{"custom too long", nginxTunablesApplyParams{CustomHTTP: strings.Repeat("a", 4001)}, true},
		{"custom NUL", nginxTunablesApplyParams{CustomHTTP: "a\x00b"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := validateNginxTunables(&c.p); (err != nil) != c.wantErr {
				t.Errorf("validateNginxTunables(%+v) err=%v, wantErr=%v", c.p, err, c.wantErr)
			}
		})
	}
}

func TestPatchNginxMainConf(t *testing.T) {
	// An nginx.conf that declares server_tokens (stock Debian and Ubuntu ship
	// it commented out; see nginx_tunables_http_scope_test.go) and gzip, with
	// keepalive_timeout absent, plus a trailing comment to prove it survives.
	conf := []byte("user www-data;\nworker_processes auto;\npid /run/nginx.pid;\nevents {\n\tworker_connections 768;\n}\nhttp {\n\tserver_tokens off; # Recommended practice is to turn this off\n\tgzip on;\n}\n")

	p := &nginxTunablesApplyParams{WorkerProcesses: "4", WorkerConnections: 2048, ServerTokens: true, Gzip: false, KeepaliveTimeout: "90s"}
	out, changed := patchNginxMainConf(conf, p)
	if !changed {
		t.Fatal("expected changed=true")
	}
	s := string(out)
	for _, want := range []string{
		"worker_processes 4;",
		"\tworker_connections 2048;", // indentation preserved
		"\tserver_tokens on;",        // flipped on
		"\tgzip off;",                // flipped off
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q after patch:\n%s", want, s)
		}
	}
	// Trailing comment on server_tokens must survive (match stops at ';').
	if !strings.Contains(s, "# Recommended practice is to turn this off") {
		t.Errorf("trailing comment lost:\n%s", s)
	}
	// keepalive_timeout absent from conf -> skip-if-absent, NOT inserted.
	if strings.Contains(s, "keepalive_timeout") {
		t.Errorf("keepalive_timeout must not be inserted when absent:\n%s", s)
	}

	// Idempotent: applying the SAME desired state to the patched conf is a no-op.
	pSame := &nginxTunablesApplyParams{WorkerProcesses: "4", WorkerConnections: 2048, ServerTokens: true, Gzip: false}
	if _, c := patchNginxMainConf(out, pSame); c {
		t.Errorf("expected idempotent no-change on second patch")
	}

	// Default-value save against stock conf (server_tokens off, gzip on) is a
	// true no-op INCLUDING the comment.
	if _, c := patchNginxMainConf(conf, &nginxTunablesApplyParams{ServerTokens: false, Gzip: true}); c {
		t.Errorf("default-value save should be a no-op on stock conf")
	}
}
