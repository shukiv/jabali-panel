package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// nginx_tunables_http_scope_test.go — Server Settings → Nginx's server_tokens,
// gzip and keepalive_timeout reach nginx even when nginx.conf does not declare
// them. Debian and Ubuntu ship `# server_tokens off;` commented out, and
// Ubuntu 24.04 has no keepalive_timeout line at all, so patching nginx.conf in
// place changed nothing: "Show nginx version" was off on the page while every
// response carried `Server: nginx/1.24.0 (Ubuntu)`.

// ubuntuStockNginxConf is the head of /etc/nginx/nginx.conf on Ubuntu 24.04
// (nginx 1.24), as found on the test box.
const ubuntuStockNginxConf = `user www-data;
worker_processes auto;
pid /run/nginx.pid;
error_log /var/log/nginx/error.log;
include /etc/nginx/modules-enabled/*.conf;

events {
	worker_connections 768;
	# multi_accept on;
}

http {

	##
	# Basic Settings
	##

	sendfile on;
	tcp_nopush on;
	types_hash_max_size 2048;
	# server_tokens off;

	# server_names_hash_bucket_size 64;
	# server_name_in_redirect off;

	include /etc/nginx/mime.types;
	default_type application/octet-stream;

	gzip on;

	include /etc/nginx/conf.d/*.conf;
	include /etc/nginx/sites-enabled/*;
}
`

func tunablesFixture(t *testing.T, mainConf string, confD map[string]string) (mainPath, fragPath string) {
	t.Helper()
	dir := t.TempDir()
	mainPath = filepath.Join(dir, "nginx.conf")
	if err := os.WriteFile(mainPath, []byte(mainConf), 0o644); err != nil {
		t.Fatal(err)
	}
	confDir := filepath.Join(dir, "conf.d")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range confD {
		if err := os.WriteFile(filepath.Join(confDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fragPath = filepath.Join(confDir, "05-jabali-tunables.conf")
	prevMain, prevFrag := nginxMainConfPath, nginxTunablesFragmentPath
	nginxMainConfPath, nginxTunablesFragmentPath = mainPath, fragPath
	t.Cleanup(func() { nginxMainConfPath, nginxTunablesFragmentPath = prevMain, prevFrag })
	return mainPath, fragPath
}

func applyTunables(t *testing.T, p nginxTunablesApplyParams) *nginxTunablesApplyResponse {
	t.Helper()
	raw, _ := json.Marshal(p)
	res, err := nginxTunablesApplyHandler(context.Background(), raw)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	return res.(*nginxTunablesApplyResponse)
}

func pageDefaults() nginxTunablesApplyParams {
	return nginxTunablesApplyParams{
		ClientMaxBodySize: "512m", KeepaliveTimeout: "65s", ServerTokens: false, Gzip: true,
		ClientBodyTimeout: "60s", ClientHeaderTimeout: "60s", SendTimeout: "60s",
		ProxyConnectTimeout: "300s", ProxyReadTimeout: "300s", ProxySendTimeout: "300s",
		WorkerProcesses: "auto", WorkerConnections: 768,
	}
}

// activeDecls counts uncommented `name value;` lines.
func activeDecls(conf, name string) int {
	return len(regexp.MustCompile(`(?m)^[ \t]*`+regexp.QuoteMeta(name)+`[ \t][^;\n]*;`).FindAllString(conf, -1))
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestNginxTunablesApply_StockUbuntuNginxConfGetsEverySetting(t *testing.T) {
	mainPath, fragPath := tunablesFixture(t, ubuntuStockNginxConf, nil)
	applyTunables(t, pageDefaults())
	main, frag := readFile(t, mainPath), readFile(t, fragPath)
	if main != ubuntuStockNginxConf {
		t.Fatalf("nginx.conf changed:\n%s", main)
	}
	for _, d := range []struct{ name, line string }{
		{"server_tokens", "server_tokens off;"},
		{"keepalive_timeout", "keepalive_timeout 65s;"},
	} {
		if n := activeDecls(main, d.name) + activeDecls(frag, d.name); n != 1 {
			t.Fatalf("%s declared %d times across nginx.conf and the fragment, want 1:\n%s", d.name, n, frag)
		}
		if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(d.line) + `$`).MatchString(frag) {
			t.Fatalf("fragment lacks %q:\n%s", d.line, frag)
		}
	}
	// nginx.conf already says gzip on; a second http-level gzip fails nginx -t.
	if n := activeDecls(frag, "gzip"); n != 0 {
		t.Fatalf("fragment declares gzip although nginx.conf does:\n%s", frag)
	}
	if res := applyTunables(t, pageDefaults()); !res.NoChange {
		t.Fatalf("second apply with the same settings changed something: %+v", res)
	}
}

// Wherever else the http block already declares one, the fragment must not:
// nginx refuses a directive declared twice in the same block.
func TestNginxTunablesApply_NoSecondDeclaration(t *testing.T) {
	for name, tc := range map[string]struct {
		main   string
		confD  map[string]string
		custom string
	}{
		"nginx.conf declares it": {
			main: regexp.MustCompile(`# server_tokens off;`).ReplaceAllString(ubuntuStockNginxConf, "server_tokens on;"),
		},
		"another conf.d file declares it": {
			main:  ubuntuStockNginxConf,
			confD: map[string]string{"10-operator.conf": "server_tokens off;\n"},
		},
		"the admin's custom directives declare it": {
			main:   ubuntuStockNginxConf,
			custom: "server_tokens off;",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, fragPath := tunablesFixture(t, tc.main, tc.confD)
			p := pageDefaults()
			p.CustomHTTP = tc.custom
			applyTunables(t, p)
			frag := readFile(t, fragPath)
			want := 0
			if tc.custom != "" {
				want = 1 // the admin's own line, nothing added
			}
			if n := activeDecls(frag, "server_tokens"); n != want {
				t.Fatalf("fragment declares server_tokens %d times, want %d:\n%s", n, want, frag)
			}
		})
	}
}

func TestNginxTunablesApply_NginxConfDeclaresItThenItIsPatchedThere(t *testing.T) {
	main := regexp.MustCompile(`# server_tokens off;`).ReplaceAllString(ubuntuStockNginxConf, "server_tokens on;")
	mainPath, fragPath := tunablesFixture(t, main, nil)
	applyTunables(t, pageDefaults())
	if got := readFile(t, mainPath); activeDecls(got, "server_tokens") != 1 || !regexp.MustCompile(`(?m)^\tserver_tokens off;$`).MatchString(got) {
		t.Fatalf("nginx.conf not patched to off:\n%s", got)
	}
	if n := activeDecls(readFile(t, fragPath), "server_tokens"); n != 0 {
		t.Fatalf("fragment declares server_tokens too")
	}
}

// Without nginx.conf there is no telling what it declares; add nothing rather
// than risk a duplicate that rolls back the whole apply.
func TestNginxTunablesApply_UnreadableNginxConfAddsNothing(t *testing.T) {
	mainPath, fragPath := tunablesFixture(t, ubuntuStockNginxConf, nil)
	if err := os.Remove(mainPath); err != nil {
		t.Fatal(err)
	}
	applyTunables(t, pageDefaults())
	frag := readFile(t, fragPath)
	for _, name := range []string{"server_tokens", "gzip", "keepalive_timeout"} {
		if n := activeDecls(frag, name); n != 0 {
			t.Fatalf("fragment declares %s without knowing nginx.conf:\n%s", name, frag)
		}
	}
}
