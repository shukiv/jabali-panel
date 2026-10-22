package commands

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
)

func TestPHPErrorLogPath(t *testing.T) {
	cases := []struct {
		user, domain, want string
	}{
		{"alice", "example.com", "/home/alice/logs/example.com.php-error.log"},
		{"alice", "xn--bcher-kva.example", "/home/alice/logs/xn--bcher-kva.example.php-error.log"},
		{"alice", "../etc", ""},
		{"alice", "a..b.com", ""},
		{"alice", "ex ample.com", ""},
		{"alice", "example.com\nauto_prepend_file=/tmp/x", ""},
		{"alice", "example.com\"", ""},
		{"alice", "", ""},
		{"Alice", "example.com", ""},
		{"alice/../root", "example.com", ""},
		{"", "example.com", ""},
	}
	for _, c := range cases {
		if got := phpErrorLogPath(c.user, c.domain); got != c.want {
			t.Errorf("phpErrorLogPath(%q, %q) = %q, want %q", c.user, c.domain, got, c.want)
		}
	}
}

func TestWithPHPErrorLog(t *testing.T) {
	if got := withPHPErrorLog("", "/home/alice/logs/a.php-error.log"); got != "" {
		t.Errorf("a non-PHP vhost (empty PHP_VALUE) must stay empty, got %q", got)
	}
	if got := withPHPErrorLog("display_errors=Off", ""); got != "display_errors=Off" {
		t.Errorf("an unsafe path adds nothing, got %q", got)
	}
	want := "display_errors=Off\nerror_log=/home/alice/logs/a.php-error.log"
	if got := withPHPErrorLog("display_errors=Off", "/home/alice/logs/a.php-error.log"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// The vhost and the app snippets' php-pins file both carry the pin: a value
// sent once sticks on the reused worker, so a location without it would log
// to whichever domain the worker served last.
func TestPHPErrorLogPinnedInVhostAndPinsFile(t *testing.T) {
	value := withPHPErrorLog(withPHPFlagPins(buildPHPValueParam(true, "", "", "", 0, 0, 0, false, nil, ""), phpFlagPins{}), phpErrorLogPath("alice", "example.com"))
	params := renderPHPPinParams(vhostData{HasPHP: true, PHPValueParam: value})
	want := "fastcgi_param PHP_VALUE \"display_errors=Off\nerror_log=/home/alice/logs/example.com.php-error.log\";\n"
	if params != want {
		t.Errorf("php-pins params:\n%q\nwant\n%q", params, want)
	}

	var sb strings.Builder
	tmpl := template.Must(template.New("vhost").Parse(vhostTemplate))
	if err := tmpl.Execute(&sb, vhostData{Domain: "example.com", Username: "alice", HasPHP: true, IsEnabled: true, DocRoot: "/home/alice/domains/example.com/public_html", FPMSocket: "/run/php/jabali-alice/fpm.sock", PHPValueParam: value}); err != nil {
		t.Fatalf("render vhost: %v", err)
	}
	if !strings.Contains(sb.String(), "error_log=/home/alice/logs/example.com.php-error.log\";") {
		t.Errorf("no PHP location of the vhost pins the error log:\n%s", sb.String())
	}
}

// TestPoolTemplateErrorLogFallback: the fallback is php_value (a tenant's
// .user.ini and ini_set still win) and points into the tenant's own ~/logs.
func TestPoolTemplateErrorLogFallback(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "install", "php", "jabali-php-pool.conf.tmpl"))
	if err != nil {
		t.Fatalf("read repo pool template: %v", err)
	}
	var sb strings.Builder
	spec := phpPoolSpecTemplate{PoolName: "jabali-alice", User: "alice", Group: "alice", SocketPath: "/run/php/jabali-alice/fpm.sock", PmMode: "ondemand", PmMaxChildren: 5, ProcessIdleTimeoutSeconds: 60}
	if err := template.Must(template.New("pool").Parse(string(b))).Execute(&sb, spec); err != nil {
		t.Fatalf("render pool: %v", err)
	}
	got := sb.String()
	if !strings.Contains(got, "\nphp_value[error_log] = /home/alice/logs/php-error.log\n") {
		t.Errorf("pool conf has no php_value error_log fallback:\n%s", got)
	}
	if strings.Contains(got, "php_admin_value[error_log]") {
		t.Error("error_log must not be php_admin_value: .user.ini and ini_set (WP_DEBUG_LOG) could no longer choose the file")
	}
}

func TestEnsureUserLogsDir(t *testing.T) {
	setHome := func(t *testing.T) string {
		t.Helper()
		home := t.TempDir()
		prev := userLogsHomeRoot
		userLogsHomeRoot = home
		t.Cleanup(func() { userLogsHomeRoot = prev })
		return home
	}

	t.Run("creates the dir", func(t *testing.T) {
		home := setHome(t)
		if err := os.Mkdir(filepath.Join(home, "alice"), 0o751); err != nil {
			t.Fatal(err)
		}
		dir, err := ensureUserLogsDir("alice")
		if err != nil {
			t.Fatalf("ensureUserLogsDir: %v", err)
		}
		fi, err := os.Lstat(dir)
		if err != nil || !fi.IsDir() {
			t.Fatalf("want a dir at %s, got %v %v", dir, fi, err)
		}
		if _, err := ensureUserLogsDir("alice"); err != nil {
			t.Errorf("an existing dir is fine: %v", err)
		}
	})

	t.Run("refuses a symlink", func(t *testing.T) {
		home := setHome(t)
		target := t.TempDir()
		if err := os.Mkdir(filepath.Join(home, "alice"), 0o751); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(home, "alice", "logs")); err != nil {
			t.Fatal(err)
		}
		if _, err := ensureUserLogsDir("alice"); err == nil {
			t.Fatal("a logs symlink must be refused, not followed (root would chown its target)")
		}
	})

	t.Run("refuses a file", func(t *testing.T) {
		home := setHome(t)
		if err := os.Mkdir(filepath.Join(home, "alice"), 0o751); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "alice", "logs"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ensureUserLogsDir("alice"); err == nil {
			t.Fatal("a logs file must be refused")
		}
	})

	t.Run("never makes a missing home", func(t *testing.T) {
		home := setHome(t)
		if _, err := ensureUserLogsDir("alice"); err == nil {
			t.Fatal("a missing home must be an error")
		}
		if _, err := os.Lstat(filepath.Join(home, "alice")); !os.IsNotExist(err) {
			t.Errorf("the home was created: %v", err)
		}
	})

	t.Run("refuses a bad username", func(t *testing.T) {
		setHome(t)
		if _, err := ensureUserLogsDir("../alice"); err == nil {
			t.Fatal("a username outside the pool regex must be refused")
		}
	})
}

func setPHPErrorLogrotateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := phpErrorLogrotateDir
	phpErrorLogrotateDir = dir
	t.Cleanup(func() { phpErrorLogrotateDir = prev })
	return dir
}

func TestBuildPHPErrorLogrotate(t *testing.T) {
	got := buildPHPErrorLogrotate("alice")
	for _, want := range []string{
		"/home/alice/logs/*php-error.log {",
		"    su alice alice\n",
		"    nocreate\n",
		"    missingok\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stanza lacks %q:\n%s", want, got)
		}
	}
	// As root in a tenant-owned dir a planted link would aim root's work at
	// another file; copytruncate would also truncate through it.
	if strings.Contains(got, "su root") || strings.Contains(got, "copytruncate") {
		t.Errorf("stanza must rotate as the tenant, by rename:\n%s", got)
	}
}

// TestPHPErrorLogrotateParses runs the real logrotate in debug mode (reads
// only) over the stanza, for the user running the test.
func TestPHPErrorLogrotateParses(t *testing.T) {
	bin, err := exec.LookPath("logrotate")
	if err != nil {
		t.Skip("logrotate not installed")
	}
	u, err := user.Current()
	if err != nil || !phpPoolUsernameRegex.MatchString(u.Username) {
		t.Skip("current user does not fit the pool username shape")
	}
	g, err := user.LookupGroupId(u.Gid)
	if err != nil {
		t.Skip("current group unknown")
	}
	dir := t.TempDir()
	conf := filepath.Join(dir, "conf")
	body := strings.Replace(buildPHPErrorLogrotate(u.Username), "su "+u.Username+" "+u.Username, "su "+u.Username+" "+g.Name, 1)
	if err := os.WriteFile(conf, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, "-d", "-s", filepath.Join(dir, "state"), conf).CombinedOutput()
	if err != nil || strings.Contains(string(out), "error:") {
		t.Fatalf("logrotate rejects the stanza: %v\n%s", err, out)
	}
}

func TestWritePHPErrorLogrotate(t *testing.T) {
	dir := setPHPErrorLogrotateDir(t)

	if err := writePHPErrorLogrotate("../alice"); err == nil {
		t.Error("a bad username must be refused")
	}

	// A stanza for a user the system does not know is removed on the next
	// write: logrotate fails on its su line. Only jabali-phperr-* is touched.
	stale := filepath.Join(dir, phpErrorLogrotatePrefix+"nosuchuser9q7z")
	other := filepath.Join(dir, "nginx")
	for _, p := range []string{stale, other} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	name := "nosuchuser8w3k"
	if u, err := user.Current(); err == nil && phpPoolUsernameRegex.MatchString(u.Username) {
		name = u.Username
	}
	if err := writePHPErrorLogrotate(name); err != nil {
		t.Fatalf("writePHPErrorLogrotate: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("the stanza of an unknown user was kept: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("a file that is not jabali-phperr-* was touched: %v", err)
	}
	if name != "nosuchuser8w3k" {
		b, err := os.ReadFile(phpErrorLogrotatePath(name))
		if err != nil || string(b) != buildPHPErrorLogrotate(name) {
			t.Errorf("stanza for %s not written: %v %q", name, err, b)
		}
	}

	removePHPErrorLogrotate(name)
	if _, err := os.Stat(phpErrorLogrotatePath(name)); !os.IsNotExist(err) {
		t.Errorf("removePHPErrorLogrotate left the stanza: %v", err)
	}
}

// user.delete reaps the default pool; the account's stanza goes with it. A
// versioned pool's reap leaves it alone: the account still exists.
func TestReapRemovesPHPErrorLogrotate(t *testing.T) {
	poolRunFixture(t)
	t.Setenv("JABALI_SYSTEMD_ROOT", t.TempDir())
	oldPin := userPhpverDir
	userPhpverDir = t.TempDir()
	t.Cleanup(func() { userPhpverDir = oldPin })
	dir := setPHPErrorLogrotateDir(t)
	p := filepath.Join(dir, phpErrorLogrotatePrefix+"alice")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	reapFPMPoolArtifacts(context.Background(), "alice-php8.2")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("a versioned pool reap removed the account's stanza: %v", err)
	}
	reapFPMPoolArtifacts(context.Background(), "alice")
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("the default pool reap kept the stanza: %v", err)
	}
}
