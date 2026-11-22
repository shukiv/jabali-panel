package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// After an IP change Let's Encrypt checked many names at once and pdns ran
// out of its default 20 TCP connections ("Limit of simultaneous TCP
// connections reached"), so certificates failed. Fresh installs get the
// higher limits from the install_powerdns template; existing boxes get them
// from ensure_pdns_tcp_limits on update, which must replace an old value,
// append a missing one, and leave a current file untouched.
func TestEnsurePDNSTCPLimits(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	if tmpl := installShFunc(t, "install_powerdns"); !strings.Contains(tmpl, "\nmax-tcp-connections=200\n") ||
		!strings.Contains(tmpl, "\nmax-tcp-connections-per-client=50\n") {
		t.Fatal("install_powerdns template does not set the TCP limits")
	}

	fn := installShFunc(t, "ensure_pdns_tcp_limits")
	dir := t.TempDir()
	conf := filepath.Join(dir, "01-jabali-mysql.conf")
	fn = strings.Replace(fn, "/etc/powerdns/pdns.d/01-jabali-mysql.conf", conf, 1)
	run := func() string {
		t.Helper()
		script := "set -Eeuo pipefail\n" +
			"_ok() { echo \"OK $*\"; }; _log() { echo \"LOG $*\"; }; _warn() { echo \"WARN $*\"; }\n" +
			"systemctl() { echo \"systemctl $*\"; }\n" +
			fn + "\nensure_pdns_tcp_limits\n"
		out, err := exec.Command("bash", "-c", script).CombinedOutput()
		if err != nil {
			t.Fatalf("ensure_pdns_tcp_limits: %v\n%s", err, out)
		}
		return string(out)
	}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(conf, []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	read := func() string {
		t.Helper()
		b, err := os.ReadFile(conf)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	// An old value is replaced and the missing one appended; pdns restarts.
	write("launch=gmysql\nmax-tcp-connections=20\nzone-cache-refresh-interval=0\n")
	out := run()
	got := read()
	if strings.Count(got, "max-tcp-connections=") != 1 || !strings.Contains(got, "\nmax-tcp-connections=200\n") {
		t.Errorf("max-tcp-connections not replaced in place:\n%s", got)
	}
	if !strings.Contains(got, "\nmax-tcp-connections-per-client=50\n") {
		t.Errorf("max-tcp-connections-per-client not appended:\n%s", got)
	}
	if !strings.Contains(out, "systemctl restart pdns") {
		t.Errorf("pdns not restarted after a change:\n%s", out)
	}

	// A current file is left byte-identical and pdns is not restarted.
	before := read()
	out = run()
	if read() != before {
		t.Errorf("a current file was rewritten:\n%s", read())
	}
	if strings.Contains(out, "restart") {
		t.Errorf("pdns restarted with nothing changed:\n%s", out)
	}

	// No pdns config (DNS module not installed): nothing happens.
	if err := os.Remove(conf); err != nil {
		t.Fatal(err)
	}
	if out := run(); out != "" {
		t.Errorf("acted without a pdns config:\n%s", out)
	}
	if _, err := os.Stat(conf); !os.IsNotExist(err) {
		t.Error("created a pdns config that did not exist")
	}
}
