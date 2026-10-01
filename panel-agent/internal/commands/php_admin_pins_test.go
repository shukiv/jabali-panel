package commands

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"text/template"
)

// stubPoolConfs points poolConfDirGlob at a temp tree holding the given pool
// files (name -> body) under PHP 8.4.
func stubPoolConfs(t *testing.T, files map[string]string) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "etc", "php", "8.4", "fpm", "pool.d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	orig := poolConfDirGlob
	poolConfDirGlob = filepath.Join(root, "etc", "php", "*", "fpm", "pool.d")
	t.Cleanup(func() { poolConfDirGlob = orig })
}

const alicePoolOpenBasedir = "/home/alice:/run/mysqld/mysqld.sock:/tmp:/var/tmp:/run/jabali-wp-purge"

// A rendered pool file: the comment above the directive mentions open_basedir
// and must not be read as its value.
var alicePoolConf = "[jabali-alice]\n; Jailbreak defense: open_basedir is hard-coded\nphp_admin_value[open_basedir] = " + alicePoolOpenBasedir + "\n"

const aliceDocRoot = "/home/alice/domains/a.test/public_html"

func aliceParams() *domainCreateParams {
	return &domainCreateParams{HasPHP: true, PHPVersion: "8.4", Username: "alice", Domain: "a.test", DocRoot: aliceDocRoot}
}

// The domain's own values are expanded and pinned.
func TestPHPAdminValuePins_DomainValues(t *testing.T) {
	stubPHPIniBaseline(t, map[string]string{"allow_url_fopen": "1"}, nil)
	stubPoolConfs(t, map[string]string{"jabali-alice.conf": alicePoolConf})
	p := aliceParams()
	p.PHPOpenBasedir = "{DOCROOT}:{TMP}"
	p.PHPAllowURLFopen = boolp(false)
	got := phpAdminValuePins(context.Background(), p)
	want := []string{
		"open_basedir=" + aliceDocRoot + ":/tmp:/var/tmp:/run/mysqld/mysqld.sock:/run/jabali-wp-purge",
		"allow_url_fopen=0",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A domain with no value of its own pins what it inherits: the open_basedir in
// its pool's file and the php.ini allow_url_fopen. Without these pins a sibling
// domain's values would stick on the shared worker.
func TestPHPAdminValuePins_InheritedValues(t *testing.T) {
	stubPHPIniBaseline(t, map[string]string{"allow_url_fopen": "1"}, nil)
	stubPoolConfs(t, map[string]string{"jabali-alice.conf": alicePoolConf})
	got := phpAdminValuePins(context.Background(), aliceParams())
	want := []string{"open_basedir=" + alicePoolOpenBasedir, "allow_url_fopen=1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// A domain on a per-version pool (GH #329) reads that pool's file.
func TestPHPAdminValuePins_VersionedPool(t *testing.T) {
	stubPHPIniBaseline(t, map[string]string{}, nil)
	versioned := strings.ReplaceAll(alicePoolConf, "/home/alice:", "/home/alice:/srv/v82:")
	stubPoolConfs(t, map[string]string{"jabali-alice.conf": alicePoolConf, "jabali-alice-php8.2.conf": versioned})
	p := aliceParams()
	p.FPMSocket = "/run/php/jabali-alice-php8.2/fpm.sock"
	got := phpAdminValuePins(context.Background(), p)
	if len(got) != 1 || !strings.Contains(got[0], "/srv/v82") {
		t.Fatalf("got %q, want the per-version pool's open_basedir", got)
	}
}

// The pool behind the socket must be the domain owner's: a socket naming
// another user's pool, or a mangled one, pins no open_basedir.
func TestPHPAdminValuePins_ForeignOrBadSocket(t *testing.T) {
	stubPHPIniBaseline(t, map[string]string{}, nil)
	stubPoolConfs(t, map[string]string{"jabali-alice.conf": alicePoolConf, "jabali-bob.conf": alicePoolConf, "jabali-alicex.conf": alicePoolConf})
	for _, sock := range []string{
		"/run/php/jabali-bob/fpm.sock",
		"/run/php/jabali-alicex/fpm.sock",
		"/run/php/jabali-../alice/fpm.sock",
		"/tmp/fpm.sock",
	} {
		p := aliceParams()
		p.FPMSocket = sock
		if got := phpAdminValuePins(context.Background(), p); len(got) != 0 {
			t.Errorf("socket %s: got %q, want no pin", sock, got)
		}
	}
}

// A stored value that fails the agent's own check (it bypassed the panel, or
// the docroot moved out of the home) falls back to the pool's value.
func TestPHPAdminValuePins_InvalidDomainValueFallsBackToPool(t *testing.T) {
	stubPHPIniBaseline(t, map[string]string{}, nil)
	stubPoolConfs(t, map[string]string{"jabali-alice.conf": alicePoolConf})
	for _, tc := range []struct{ value, docRoot string }{
		{"/home/bob", aliceDocRoot},
		{"/", aliceDocRoot},
		{"{DOCROOT}", "/home/bob/public_html"},
		{`{DOCROOT}:/srv/$x`, aliceDocRoot},
	} {
		p := aliceParams()
		p.PHPOpenBasedir, p.DocRoot = tc.value, tc.docRoot
		got := phpAdminValuePins(context.Background(), p)
		if !reflect.DeepEqual(got, []string{"open_basedir=" + alicePoolOpenBasedir}) {
			t.Errorf("value %q docroot %q: got %q, want the pool's open_basedir", tc.value, tc.docRoot, got)
		}
	}
}

// No readable pool file, or one with no open_basedir or an unsafe one: a
// domain without its own value pins nothing rather than a guess.
func TestPHPAdminValuePins_NoUsablePoolFile(t *testing.T) {
	stubPHPIniBaseline(t, map[string]string{}, nil)
	for name, files := range map[string]map[string]string{
		"missing":   {},
		"no jail":   {"jabali-alice.conf": "[jabali-alice]\n"},
		"unsafe":    {"jabali-alice.conf": "php_admin_value[open_basedir] = /home/alice:$x\n"},
		"two files": {"jabali-alice.conf": alicePoolConf},
	} {
		t.Run(name, func(t *testing.T) {
			stubPoolConfs(t, files)
			if name == "two files" {
				// The same pool under a second version: ambiguous.
				other := strings.Replace(poolConfDirGlob, "*", "8.3", 1)
				if err := os.MkdirAll(other, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(other, "jabali-alice.conf"), []byte(alicePoolConf), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := phpAdminValuePins(context.Background(), aliceParams()); len(got) != 0 {
				t.Fatalf("got %q, want no pin", got)
			}
		})
	}
}

// allow_url_fopen: with the pool's overrides unknown nothing is pinned from
// the baseline, like every inherited pin; a non-PHP vhost gets no pins.
func TestPHPAdminValuePins_InheritUnknownAndNonPHP(t *testing.T) {
	calls := stubPHPIniBaseline(t, map[string]string{"allow_url_fopen": "1"}, nil)
	stubPoolConfs(t, map[string]string{"jabali-alice.conf": alicePoolConf})
	p := aliceParams()
	p.PHPFlagsInheritUnknown = true
	got := phpAdminValuePins(context.Background(), p)
	if !reflect.DeepEqual(got, []string{"open_basedir=" + alicePoolOpenBasedir}) || *calls != 0 {
		t.Fatalf("got %q with %d php.ini reads, want only the pool open_basedir and no read", got, *calls)
	}
	p.HasPHP = false
	if got := phpAdminValuePins(context.Background(), p); got != nil {
		t.Fatalf("non-PHP vhost got %q", got)
	}
	if buildPHPAdminValueParam(false, []string{"allow_url_fopen=0"}) != "" {
		t.Fatal("non-PHP vhost must render no PHP_ADMIN_VALUE")
	}
}

// Every PHP location of the vhost carries PHP_ADMIN_VALUE: location =
// /index.php, location ~ \.php$ and the PATH_INFO location. An empty value
// renders no line at all.
func TestPHPAdminValueRenderedInEveryPHPLocation(t *testing.T) {
	render := func(admin string) string {
		tmpl := template.Must(template.New("vhost").Parse(vhostTemplate))
		var buf bytes.Buffer
		err := tmpl.Execute(&buf, vhostData{
			Domain: "a.test", DocRoot: aliceDocRoot, HasPHP: true, PHPVersion: "8.4", Username: "alice",
			FPMSocket: "/run/php/jabali-alice/fpm.sock", IsEnabled: true, EnablePathInfo: true,
			PHPValueParam: "display_errors=Off", PHPAdminValueParam: admin,
		})
		if err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	admin := buildPHPAdminValueParam(true, []string{"open_basedir=" + alicePoolOpenBasedir, "allow_url_fopen=0"})
	out := render(admin)
	if got := strings.Count(out, `fastcgi_param PHP_ADMIN_VALUE "open_basedir=`+alicePoolOpenBasedir+"\nallow_url_fopen=0\";"); got != 3 {
		t.Fatalf("PHP_ADMIN_VALUE count = %d, want 3\n%s", got, out)
	}
	if strings.Contains(render(""), "PHP_ADMIN_VALUE") {
		t.Fatal("an empty admin value must render no PHP_ADMIN_VALUE line")
	}
}

// domain.create's pin decision carries the admin pins to the vhost.
func TestPHPFlagPinsForParams_CarriesAdminPins(t *testing.T) {
	stubPHPIniBaseline(t, map[string]string{"allow_url_fopen": "1"}, nil)
	stubPoolConfs(t, map[string]string{"jabali-alice.conf": alicePoolConf})
	got := phpFlagPinsForParams(context.Background(), aliceParams())
	if !reflect.DeepEqual(got.Admin, []string{"open_basedir=" + alicePoolOpenBasedir, "allow_url_fopen=1"}) {
		t.Fatalf("Admin = %q, want the inherited open_basedir and allow_url_fopen", got.Admin)
	}
}
