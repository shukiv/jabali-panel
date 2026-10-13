package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
)

// app_snippet_pool_test.go — the app snippets in /etc/nginx/jabali/<domain>/
// pass PHP to the pool the domain's vhost passes to (a per-version pool has its
// own socket, GH #329), when written at install and on every vhost write after
// a PHP version change. Never to another account's pool.

const (
	bobDefaultSock = "/run/php/jabali-bob/fpm.sock"
	bob83Sock      = "/run/php/jabali-bob-php8.3/fpm.sock"
)

func bobSnippets(t *testing.T) map[string][]byte {
	t.Helper()
	var drupal bytes.Buffer
	tmpl := template.Must(template.New("rewrite").Parse(appRewriteSnippetTemplate))
	if err := tmpl.Execute(&drupal, appRewriteData{AppType: "drupal", Domain: pinsTestDomain, OSUser: "bob", SubdirPath: "/d/"}); err != nil {
		t.Fatal(err)
	}
	return map[string][]byte{
		"drupal subdir":       drupal.Bytes(),
		"openemr subdir":      []byte(openemrNginxConf("bob", "emr")),
		"invoiceshelf subdir": []byte(invoiceShelfNginxConf("bob", "inv", "/home/bob/domains/a.test/public_html/inv")),
		"flarum subdir":       []byte(flarumNginxConf("bob", "forum")),
		"osticket":            []byte(osticketNginxConf("bob")),
	}
}

func TestWithVhostFPMSocket_EveryGenerator(t *testing.T) {
	for name, body := range bobSnippets(t) {
		t.Run(name, func(t *testing.T) {
			passes := strings.Count(string(body), "fastcgi_pass unix:"+bobDefaultSock+";")
			if passes == 0 {
				t.Fatalf("fixture does not pass to the default pool:\n%s", body)
			}
			got, ok := withVhostFPMSocket(body, "bob", bob83Sock)
			if !ok {
				t.Fatal("snippet not moved to the vhost's pool")
			}
			if n := strings.Count(string(got), "fastcgi_pass unix:"+bob83Sock+";"); n != passes || strings.Contains(string(got), bobDefaultSock) {
				t.Fatalf("%d of %d PHP locations moved:\n%s", n, passes, got)
			}
			if _, again := withVhostFPMSocket(got, "bob", bob83Sock); again {
				t.Fatal("a second pass changed the snippet")
			}
		})
	}
}

func TestWithVhostFPMSocket_LeavesOthersAlone(t *testing.T) {
	pass := func(target string) []byte {
		return []byte("location ~ \\.php$ {\n    fastcgi_pass " + target + ";\n    include fastcgi_params;\n}\n")
	}
	for name, body := range map[string][]byte{
		"already on the vhost's pool": pass("unix:" + bob83Sock),
		"another account's pool":      pass("unix:/run/php/jabali-alice/fpm.sock"),
		"a lookalike account's pool":  pass("unix:/run/php/jabali-bobby-php8.4/fpm.sock"),
		"the distro pool":             pass("unix:/run/php/php8.4-fpm.sock"),
		"a TCP backend":               pass("127.0.0.1:9000"),
		"no PHP location":             []byte(xmlrpcBlockContent),
	} {
		if got, ok := withVhostFPMSocket(body, "bob", bob83Sock); ok {
			t.Errorf("%s: snippet was changed:\n%s", name, got)
		}
	}
	// The vhost's socket must be one of the owner's own pools.
	for name, target := range map[string]string{
		"another account's pool":   "/run/php/jabali-alice/fpm.sock",
		"a lookalike account":      "/run/php/jabali-bobby-php8.4/fpm.sock",
		"the distro pool":          "/run/php/php8.4-fpm.sock",
		"no version in the slug":   "/run/php/jabali-bob-php8/fpm.sock",
		"no vhost socket":          "",
		"a path outside /run/php/": "/tmp/jabali-bob/fpm.sock",
	} {
		if got, ok := withVhostFPMSocket([]byte(osticketNginxConf("bob")), "bob", target); ok {
			t.Errorf("vhost socket %s: snippet was changed:\n%s", name, got)
		}
	}
}

// writeVhost's plan moves a stale snippet to the vhost's pool, settles (a
// second plan is empty, so the reconcile tick does not reload nginx every
// minute), and follows the domain back when its PHP version changes again.
func TestPlanPHPPinFiles_FollowsTheVhostPool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "osticket.conf")
	if err := os.WriteFile(path, []byte(osticketNginxConf("bob")), 0o644); err != nil {
		t.Fatal(err)
	}
	params := "fastcgi_param PHP_VALUE \"display_errors=Off\";\n"

	changes := planPHPPinFiles(dir, pinsTestDomain, "bob", params, bob83Sock)
	if len(changes) != 2 {
		t.Fatalf("want the pins file and the snippet, got %d changes", len(changes))
	}
	if err := applyPinFileChanges(changes); err != nil {
		t.Fatal(err)
	}
	snippet, _ := os.ReadFile(path)
	if !strings.Contains(string(snippet), "fastcgi_pass unix:"+bob83Sock+";") || strings.Contains(string(snippet), bobDefaultSock) ||
		!strings.Contains(string(snippet), pinsTestInclude) {
		t.Fatalf("snippet not on the vhost's pool with the pins include:\n%s", snippet)
	}
	if again := planPHPPinFiles(dir, pinsTestDomain, "bob", params, bob83Sock); len(again) != 0 {
		t.Fatalf("after apply, %d changes remain", len(again))
	}

	back := planPHPPinFiles(dir, pinsTestDomain, "bob", params, bobDefaultSock)
	if len(back) != 1 || back[0].path != path || !strings.Contains(string(back[0].want), "fastcgi_pass unix:"+bobDefaultSock+";") {
		t.Fatalf("back on the default pool: want the snippet moved back, got %+v", back)
	}

	if gone := planPHPPinFiles(dir, pinsTestDomain, "bob", "", ""); len(gone) != 1 || !gone[0].remove {
		t.Fatalf("a vhost without PHP: want only the pins file removed, got %+v", gone)
	}
}

// At install a snippet takes the pool from the domain's vhost; without a
// readable vhost, or with one on another account's pool, it keeps the socket
// it was written with (the next vhost write settles it).
func TestSnippetWithPHPPins_TakesTheVhostPool(t *testing.T) {
	sites := t.TempDir()
	old := nginxSitesDir
	nginxSitesDir = sites
	t.Cleanup(func() { nginxSitesDir = old })
	vhost := func(socket string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(sites, pinsTestDomain+".conf"), []byte(vhostWith(socket)), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	vhost(bob83Sock)
	got := string(snippetWithPHPPins([]byte(osticketNginxConf("bob")), pinsTestDomain, "bob"))
	if !strings.Contains(got, "fastcgi_pass unix:"+bob83Sock+";") || strings.Contains(got, bobDefaultSock) || !strings.Contains(got, pinsTestInclude) {
		t.Fatalf("per-version vhost: snippet not on its pool with the pins include:\n%s", got)
	}

	vhost("/run/php/jabali-alice/fpm.sock")
	got = string(snippetWithPHPPins([]byte(osticketNginxConf("bob")), pinsTestDomain, "bob"))
	if !strings.Contains(got, "fastcgi_pass unix:"+bobDefaultSock+";") {
		t.Fatalf("vhost on another account's pool: snippet socket changed:\n%s", got)
	}

	got = string(snippetWithPHPPins([]byte(osticketNginxConf("bob")), "b.test", "bob"))
	if !strings.Contains(got, "fastcgi_pass unix:"+bobDefaultSock+";") {
		t.Fatalf("no vhost: snippet socket changed:\n%s", got)
	}
}
