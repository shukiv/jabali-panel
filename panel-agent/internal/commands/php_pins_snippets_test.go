package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
)

const pinsTestDomain = "a.test"

var pinsTestInclude = "include /etc/nginx/jabali/a.test/php-pins*.params;"

func drupalSubdirSnippet(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	tmpl := template.Must(template.New("rewrite").Parse(appRewriteSnippetTemplate))
	if err := tmpl.Execute(&buf, appRewriteData{AppType: "drupal", Domain: pinsTestDomain, OSUser: "alice", SubdirPath: "/d/"}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The pins file carries exactly the per-domain lines of the vhost's PHP
// locations; a non-PHP vhost has none.
func TestRenderPHPPinParams(t *testing.T) {
	vd := vhostData{HasPHP: true, PHPValueParam: "display_errors=Off\nmemory_limit=256M", PHPAdminValueParam: "allow_url_fopen=0", EnvParams: `        fastcgi_param APP_ENV "prod";`}
	want := "fastcgi_param PHP_VALUE \"display_errors=Off\nmemory_limit=256M\";\n" +
		"fastcgi_param PHP_ADMIN_VALUE \"allow_url_fopen=0\";\n" +
		"        fastcgi_param APP_ENV \"prod\";\n"
	if got := renderPHPPinParams(vd); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	vd.HasPHP = false
	if got := renderPHPPinParams(vd); got != "" {
		t.Fatalf("non-PHP vhost: got %q", got)
	}
}

// Every PHP location of every app snippet gets the include, right after its
// fastcgi_params and at its indentation; the transform is idempotent.
func TestSnippetWithPHPPins_EveryGenerator(t *testing.T) {
	for name, body := range map[string][]byte{
		"drupal subdir":       drupalSubdirSnippet(t),
		"openemr subdir":      []byte(openemrNginxConf("alice", "emr")),
		"invoiceshelf subdir": []byte(invoiceShelfNginxConf("alice", "inv", "/home/alice/domains/a.test/public_html/inv")),
		"flarum subdir":       []byte(flarumNginxConf("alice", "forum")),
		"osticket":            []byte(osticketNginxConf("alice")),
	} {
		t.Run(name, func(t *testing.T) {
			got := string(snippetWithPHPPins(body, pinsTestDomain))
			passes := strings.Count(got, "fastcgi_pass ")
			if passes == 0 {
				t.Fatalf("fixture has no PHP location:\n%s", got)
			}
			if n := strings.Count(got, pinsTestInclude); n != passes {
				t.Fatalf("%d includes for %d PHP locations:\n%s", n, passes, got)
			}
			lines := strings.Split(got, "\n")
			for i, l := range lines {
				if strings.TrimSpace(l) != pinsTestInclude {
					continue
				}
				prev := lines[i-1]
				if strings.TrimSpace(prev) != "include fastcgi_params;" {
					t.Fatalf("include not right after fastcgi_params:\n%s", got)
				}
				if l[:len(l)-len(strings.TrimLeft(l, " "))] != prev[:len(prev)-len(strings.TrimLeft(prev, " "))] {
					t.Fatalf("include indentation differs from fastcgi_params:\n%s", got)
				}
			}
			if again := string(snippetWithPHPPins([]byte(got), pinsTestDomain)); again != got {
				t.Fatal("a second pass changed the snippet")
			}
		})
	}
}

// A snippet with no PHP location, or one setting its own PHP values, is left
// alone.
func TestWithPHPPinsInclude_LeavesOthersAlone(t *testing.T) {
	for name, body := range map[string]string{
		"no php location": xmlrpcBlockContent,
		"operator values": "location ~ \\.php$ {\n    fastcgi_pass unix:/run/php/jabali-alice/fpm.sock;\n    include fastcgi_params;\n    fastcgi_param PHP_VALUE \"x=1\";\n}\n",
	} {
		if _, ok := withPHPPinsInclude([]byte(body), pinsTestDomain); ok {
			t.Errorf("%s: snippet was changed", name)
		}
	}
}

// writeVhost's plan: create the pins file and patch a snippet written before
// this change; once applied, nothing is left to do; a vhost that loses PHP
// removes the pins file. A symlinked .conf is never followed.
func TestPlanPHPPinFiles(t *testing.T) {
	dir := t.TempDir()
	old := drupalSubdirSnippet(t)
	if err := os.WriteFile(filepath.Join(dir, "drupal-d.conf"), old, 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.conf")
	if err := os.WriteFile(outside, old, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link.conf")); err != nil {
		t.Fatal(err)
	}
	params := "fastcgi_param PHP_VALUE \"display_errors=Off\";\n"

	changes := planPHPPinFiles(dir, pinsTestDomain, params)
	if len(changes) != 2 {
		t.Fatalf("want the pins file and one snippet, got %d changes", len(changes))
	}
	if err := applyPinFileChanges(changes); err != nil {
		t.Fatal(err)
	}
	pins, _ := os.ReadFile(filepath.Join(dir, phpPinsFileName))
	if !strings.HasSuffix(string(pins), params) {
		t.Fatalf("pins file = %q", pins)
	}
	snippet, _ := os.ReadFile(filepath.Join(dir, "drupal-d.conf"))
	if !strings.Contains(string(snippet), pinsTestInclude) {
		t.Fatalf("snippet not patched:\n%s", snippet)
	}
	if b, _ := os.ReadFile(outside); !bytes.Equal(b, old) {
		t.Fatal("the symlink target was changed")
	}
	if again := planPHPPinFiles(dir, pinsTestDomain, params); len(again) != 0 {
		t.Fatalf("after apply, %d changes remain", len(again))
	}

	changed := planPHPPinFiles(dir, pinsTestDomain, "fastcgi_param PHP_VALUE \"display_errors=On\";\n")
	if len(changed) != 1 || changed[0].path != filepath.Join(dir, phpPinsFileName) {
		t.Fatalf("a changed value: want one pins file change, got %+v", changed)
	}

	gone := planPHPPinFiles(dir, pinsTestDomain, "")
	if len(gone) != 1 || !gone[0].remove {
		t.Fatalf("a vhost without PHP: want the pins file removed, got %+v", gone)
	}
	if none := planPHPPinFiles(filepath.Join(dir, "missing"), "b.test", ""); len(none) != 0 {
		t.Fatalf("no directory and no PHP: got %d changes", len(none))
	}
}

// A failed nginx -t puts every file back: the patched snippet's old content,
// and no pins file where there was none.
func TestRevertPinFileChanges(t *testing.T) {
	dir := t.TempDir()
	old := drupalSubdirSnippet(t)
	snippet := filepath.Join(dir, "drupal-d.conf")
	if err := os.WriteFile(snippet, old, 0o644); err != nil {
		t.Fatal(err)
	}
	changes := planPHPPinFiles(dir, pinsTestDomain, "fastcgi_param PHP_VALUE \"display_errors=Off\";\n")
	if err := applyPinFileChanges(changes); err != nil {
		t.Fatal(err)
	}
	revertPinFileChanges(changes)
	if b, _ := os.ReadFile(snippet); !bytes.Equal(b, old) {
		t.Fatalf("snippet not restored:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(dir, phpPinsFileName)); !os.IsNotExist(err) {
		t.Fatalf("pins file left behind: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("stray files left: %v", entries)
	}
}
