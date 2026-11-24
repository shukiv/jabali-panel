package commands

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSSHAcceptedRe(t *testing.T) {
	pos := []struct {
		line, user, ip string
	}{
		{"Accepted publickey for alice from 203.0.113.7 port 51234 ssh2: RSA SHA256:abc", "alice", "203.0.113.7"},
		{"Accepted password for bob from 198.51.100.9 port 22 ssh2", "bob", "198.51.100.9"},
		{"Accepted keyboard-interactive/pam for carol from 2606:4700:4700::1111 port 40000 ssh2", "carol", "2606:4700:4700::1111"},
	}
	for _, c := range pos {
		m := sshAcceptedRe.FindStringSubmatch(c.line)
		if m == nil {
			t.Fatalf("expected match for %q", c.line)
		}
		if m[1] != c.user || m[2] != c.ip {
			t.Errorf("parse %q => user=%q ip=%q, want %q/%q", c.line, m[1], m[2], c.user, c.ip)
		}
	}
	neg := []string{
		"Failed password for root from 203.0.113.7 port 51234 ssh2",
		"Disconnected from 203.0.113.7 port 51234",
		"Connection closed by 203.0.113.7 port 51234",
		"Accepted publickey for alice",           // no "from ... port"
		"pam_unix(sshd:session): session opened", // session line, not Accepted
	}
	for _, l := range neg {
		if sshAcceptedRe.FindStringSubmatch(l) != nil {
			t.Errorf("did NOT expect match for %q", l)
		}
	}
}

func TestLoginDedup(t *testing.T) {
	d := newLoginDedup()
	base := time.Unix(1_700_000_000, 0)
	if !d.allow("203.0.113.7", base) {
		t.Fatal("first sight should allow")
	}
	if d.allow("203.0.113.7", base.Add(time.Hour)) {
		t.Fatal("within window should be deduped")
	}
	// Different IP is independent.
	if !d.allow("198.51.100.9", base.Add(time.Hour)) {
		t.Fatal("distinct IP should allow")
	}
	// After the window the same IP is allowed again (and pruned).
	if !d.allow("203.0.113.7", base.Add(loginAllowlistDedupWindow+time.Minute)) {
		t.Fatal("past window should allow again")
	}
}

func TestLoginWhitelistableIP(t *testing.T) {
	cases := map[string]bool{
		"203.0.113.7":          true,
		"2606:4700:4700::1111": true,
		"127.0.0.1":            false,
		"10.0.3.14":            false,
		"192.168.1.1":          false,
		"169.254.0.1":          false,
		"":                     false,
		"garbage":              false,
	}
	for ip, want := range cases {
		if got := loginWhitelistableIP(ip); got != want {
			t.Errorf("loginWhitelistableIP(%q)=%v want %v", ip, got, want)
		}
	}
}

func TestReadLoginAllowlistConf_DefaultsWhenAbsent(t *testing.T) {
	// Point at a non-existent path via the package const's dir is fixed, so we
	// instead exercise the JSON/default logic through a temp file swapped in.
	orig := loginAllowlistConfPath
	defer func() { loginAllowlistConfPath = orig }()

	// Absent file => enabled + default TTL.
	loginAllowlistConfPath = filepath.Join(t.TempDir(), "nope.conf")
	c := readLoginAllowlistConf()
	if !c.Enabled || c.TTL != loginAllowlistDefaultTTL {
		t.Fatalf("absent => want enabled+%s, got %+v", loginAllowlistDefaultTTL, c)
	}

	// Present, disabled, custom TTL.
	p := filepath.Join(t.TempDir(), "ok.conf")
	if err := os.WriteFile(p, []byte(`{"enabled":false,"ttl":"24h"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	loginAllowlistConfPath = p
	c = readLoginAllowlistConf()
	if c.Enabled || c.TTL != "24h" {
		t.Fatalf("present => want disabled+24h, got %+v", c)
	}

	// Present but bad TTL => coerced to default.
	if err := os.WriteFile(p, []byte(`{"enabled":true,"ttl":"nonsense"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c = readLoginAllowlistConf()
	if c.TTL != loginAllowlistDefaultTTL {
		t.Fatalf("bad ttl => want default, got %q", c.TTL)
	}
}

func TestCSLoginAllowlistApplyHandler_WritesConf(t *testing.T) {
	orig := loginAllowlistConfPath
	defer func() { loginAllowlistConfPath = orig }()
	p := filepath.Join(t.TempDir(), "login-allowlist.conf")
	loginAllowlistConfPath = p

	out, err := csLoginAllowlistApplyHandler(nil, json.RawMessage(`{"enabled":true,"ttl_hours":48}`))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	_ = out
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var c loginAllowlistConf
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if !c.Enabled || c.TTL != "48h" {
		t.Fatalf("want enabled+48h, got %+v", c)
	}

	// ttl_hours <= 0 coerces to 168.
	if _, err := csLoginAllowlistApplyHandler(nil, json.RawMessage(`{"enabled":false,"ttl_hours":0}`)); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(p)
	_ = json.Unmarshal(b, &c)
	if c.Enabled || c.TTL != "168h" {
		t.Fatalf("want disabled+168h default, got %+v", c)
	}
}

// sshd logs a client-chosen username verbatim ("Invalid user <name> from
// ..."). A username that spells out an Accepted line must never parse as one,
// in either log form — before the anchoring, one failed pre-auth connection
// put an arbitrary IP on the allowlist.
func TestParseSSHAccepted_NoUsernameInjection(t *testing.T) {
	ok := []struct{ line, user, ip string }{
		{"Accepted publickey for root from 203.0.113.7 port 51234 ssh2: ED25519 SHA256:abc", "root", "203.0.113.7"},
		{"Accepted password for root from 2606:4700:4700::1111 port 22 ssh2", "root", "2606:4700:4700::1111"},
	}
	for _, c := range ok {
		u, ip, got := parseSSHAccepted(c.line)
		if !got || u != c.user || ip != c.ip {
			t.Errorf("%q => %q %q %v, want %q %q", c.line, u, ip, got, c.user, c.ip)
		}
	}
	bad := []string{
		// What sshd 10.0 logged on .13 for one failed connection.
		"Invalid user Accepted password for root from 198.51.100.9 port 22 x from 127.0.0.1 port 55850",
		"Connection closed by invalid user Accepted password for root from 198.51.100.9 port 22 x 127.0.0.1 port 55850 [preauth]",
		"Invalid user Accepted publickey for root from 198.51.100.9 port 22 ssh2: RSA from 127.0.0.1 port 1",
		"Invalid user sshd[1]: Accepted publickey for root from 198.51.100.9 port 22 ssh2: RSA from 127.0.0.1 port 1",
		// A syslog-style line is not a journal message.
		"Nov 24 20:29:40 host1 sshd[123]: Accepted publickey for root from 198.51.100.9 port 22 ssh2",
	}
	for _, l := range bad {
		if u, ip, got := parseSSHAccepted(l); got {
			t.Errorf("%q must not parse, got %q %q", l, u, ip)
		}
	}
}

// The one-shot cleanup drops only the entries the SSH watcher added.
func TestDropSSHAllowlistEntries(t *testing.T) {
	inspect := `{"items":[` +
		`{"value":"198.51.100.9","description":"auto-whitelist: ssh root"},` +
		`{"value":"203.0.113.5","description":"auto-whitelist: login admin@example.com"},` +
		`{"value":"192.0.2.1","description":"office"}]}`
	var removed []string
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "cscli" && len(args) > 1 && args[1] == "inspect" {
			return exec.CommandContext(ctx, "printf", "%s", inspect)
		}
		if name == "cscli" && len(args) > 1 && args[1] == "remove" {
			removed = append(removed, strings.Join(args[2:], " "))
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })

	n, done := dropSSHAllowlistEntries(context.Background())
	if !done || n != 1 || len(removed) != 1 || removed[0] != jabaliAllowlistName+" 198.51.100.9" {
		t.Fatalf("done=%v n=%d removed=%v, want only the ssh entry", done, n, removed)
	}
}
