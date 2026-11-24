package commands

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type banExemptFake struct {
	calls          []string
	rejectCrowdsec bool
}

func (f *banExemptFake) count(prefix string) int {
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// withLoginBanExemptEnv points every path at a temp dir and records the
// commands run: cscli, crowdsec -t and systemctl succeed unless told not to.
func withLoginBanExemptEnv(t *testing.T) *banExemptFake {
	t.Helper()
	dir := t.TempDir()
	cs := filepath.Join(dir, "crowdsec")
	if err := os.MkdirAll(cs, 0o755); err != nil {
		t.Fatal(err)
	}
	prevPath, prevState, prevDir, prevConf, prevExec := loginBanExemptPath, loginBanExemptStatePath, crowdsecConfigDir, loginAllowlistConfPath, execCommandContext
	loginBanExemptPath = filepath.Join(cs, "postoverflows", "s01-whitelist", "zz-jabali-login-ips.yaml")
	loginBanExemptStatePath = filepath.Join(dir, "state", "login-ban-exempt.json")
	crowdsecConfigDir = cs
	loginAllowlistConfPath = filepath.Join(dir, "login-allowlist.conf")
	f := &banExemptFake{}
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		f.calls = append(f.calls, strings.TrimSpace(name+" "+strings.Join(args, " ")))
		if name == "crowdsec" && f.rejectCrowdsec {
			return exec.CommandContext(ctx, "false")
		}
		if name == "cscli" && len(args) > 1 && args[1] == "inspect" {
			return exec.CommandContext(ctx, "printf", "%s", `{"items":[]}`)
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() {
		loginBanExemptPath, loginBanExemptStatePath, crowdsecConfigDir, loginAllowlistConfPath, execCommandContext = prevPath, prevState, prevDir, prevConf, prevExec
	})
	return f
}

func whitelistIPs(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(loginBanExemptPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Name      string `yaml:"name"`
		Whitelist struct {
			Reason string   `yaml:"reason"`
			IP     []string `yaml:"ip"`
		} `yaml:"whitelist"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("whitelist is not YAML: %v\n%s", err, b)
	}
	if doc.Name != "jabali/login-ips" || doc.Whitelist.Reason == "" {
		t.Fatalf("whitelist shape: %+v", doc)
	}
	return doc.Whitelist.IP
}

func TestLoginBanExemptIP(t *testing.T) {
	cases := map[string]string{
		"203.0.113.7":          "203.0.113.7",
		" 203.0.113.7 ":        "203.0.113.7",
		"::ffff:203.0.113.7":   "203.0.113.7",
		"2606:4700:4700::1111": "2606:4700:4700::1111",
		"10.0.3.14":            "10.0.3.14", // a LAN admin, as the panel sends it
		"100.64.0.1":           "",          // CGNAT / tailnet
		"100.127.255.254":      "",
		"::ffff:100.64.0.1":    "",
		"127.0.0.1":            "",
		"::1":                  "",
		"0.0.0.0":              "",
		"169.254.0.1":          "",
		"fe80::1%eth0":         "",
		"2001:db8::1%eth0":     "",
		"224.0.0.1":            "",
		"203.0.113.0/24":       "",
		"garbage":              "",
		"":                     "",
	}
	for in, want := range cases {
		got, ok := loginBanExemptIP(in)
		if got != want || ok != (want != "") {
			t.Errorf("loginBanExemptIP(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
}

func TestRenderLoginBanExempt_IsYAML(t *testing.T) {
	withLoginBanExemptEnv(t)
	ips := []string{"203.0.113.7", "2606:4700:4700::1111"}
	if err := os.MkdirAll(filepath.Dir(loginBanExemptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(loginBanExemptPath, []byte(renderLoginBanExempt(ips)), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := whitelistIPs(t); strings.Join(got, ",") != strings.Join(ips, ",") {
		t.Fatalf("ips = %v", got)
	}
}

// A root SSH login used to put its IP on the CrowdSec allowlist, which skips
// the WAF as well as bans. It now only goes on the ban-only whitelist.
func TestHandleSSHLogLine_RootLoginIsBanOnly(t *testing.T) {
	f := withLoginBanExemptEnv(t)
	handleSSHLogLine(context.Background(), "Accepted publickey for root from 203.0.113.7 port 51234 ssh2: RSA SHA256:x",
		newLoginDedup(), slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now())
	if n := f.count("cscli allowlists"); n != 0 {
		t.Fatalf("cscli allowlists called %d times: %v", n, f.calls)
	}
	if got := whitelistIPs(t); len(got) != 1 || got[0] != "203.0.113.7" {
		t.Fatalf("whitelist = %v", got)
	}
	if f.count("crowdsec -c") != 1 || f.count("systemctl reload crowdsec") != 1 {
		t.Fatalf("want one check and one reload: %v", f.calls)
	}
}

// A panel older than the agent still calls allowlists.add for admin logins.
func TestAllowlistsAdd_LoginReasonGoesBanOnly(t *testing.T) {
	f := withLoginBanExemptEnv(t)
	raw, _ := json.Marshal(map[string]any{"value": "203.0.113.9", "reason": "auto-whitelist: login admin@example.com", "expiration": "168h"})
	out, err := csAllowlistsAddHandler(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if m, _ := out.(map[string]any); m["ban_only"] != true {
		t.Fatalf("out = %v", out)
	}
	if n := f.count("cscli allowlists"); n != 0 {
		t.Fatalf("cscli allowlists called: %v", f.calls)
	}
	if got := whitelistIPs(t); len(got) != 1 || got[0] != "203.0.113.9" {
		t.Fatalf("whitelist = %v", got)
	}
}

func TestAllowlistsAdd_ManualEntryStaysOnAllowlist(t *testing.T) {
	f := withLoginBanExemptEnv(t)
	raw, _ := json.Marshal(map[string]any{"value": "192.0.2.10", "reason": "office"})
	if _, err := csAllowlistsAddHandler(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if f.count("cscli allowlists add "+jabaliAllowlistName+" 192.0.2.10") != 1 {
		t.Fatalf("calls = %v", f.calls)
	}
	if got := whitelistIPs(t); got != nil {
		t.Fatalf("whitelist = %v", got)
	}
}

func TestLoginIPsAdd_RejectsCGNATAndCIDR(t *testing.T) {
	f := withLoginBanExemptEnv(t)
	for _, v := range []string{"100.64.1.2", "203.0.113.0/24", "127.0.0.1"} {
		raw, _ := json.Marshal(map[string]any{"value": v, "reason": "panel login a@b"})
		_, err := csLoginIPsAddHandler(context.Background(), raw)
		var ae *agentwire.AgentError
		if !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument {
			t.Errorf("%s: err = %v, want invalid_argument", v, err)
		}
	}
	if len(f.calls) != 0 || whitelistIPs(t) != nil {
		t.Fatalf("calls = %v", f.calls)
	}
}

func TestLoginBanExempt_RefreshDoesNotReload(t *testing.T) {
	f := withLoginBanExemptEnv(t)
	now := time.Now()
	for i := 0; i < 3; i++ {
		if err := addLoginBanExempt("203.0.113.7", "panel login a@b", time.Hour, now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.count("systemctl reload crowdsec"); n != 1 {
		t.Fatalf("reloads = %d: %v", n, f.calls)
	}
	m := readLoginBanExemptState()
	if e := m["203.0.113.7"]; !e.Expires.Equal(now.Add(2*time.Minute + time.Hour)) {
		t.Fatalf("expiry not slid: %v", e.Expires)
	}
}

// A file CrowdSec rejects never stays: the reload (and its restart fallback)
// must not meet a config CrowdSec cannot start with.
func TestLoginBanExempt_RejectedFileIsPutBack(t *testing.T) {
	f := withLoginBanExemptEnv(t)
	now := time.Now()
	f.rejectCrowdsec = true
	if err := addLoginBanExempt("203.0.113.7", "first", time.Hour, now); err == nil {
		t.Fatal("want an error")
	}
	if whitelistIPs(t) != nil || f.count("systemctl") != 0 {
		t.Fatalf("first file kept or reloaded: %v", f.calls)
	}
	f.rejectCrowdsec = false
	if err := maintainLoginBanExempt(now); err != nil { // upkeep retries
		t.Fatal(err)
	}
	if got := whitelistIPs(t); len(got) != 1 {
		t.Fatalf("upkeep did not write it: %v", got)
	}
	f.rejectCrowdsec = true
	before, _ := os.ReadFile(loginBanExemptPath)
	if err := addLoginBanExempt("203.0.113.8", "second", time.Hour, now); err == nil {
		t.Fatal("want an error")
	}
	after, _ := os.ReadFile(loginBanExemptPath)
	if string(before) != string(after) || f.count("systemctl reload crowdsec") != 1 {
		t.Fatalf("old file not put back:\n%s\ncalls %v", after, f.calls)
	}
}

func TestLoginBanExempt_ExpiryAndSwitchOff(t *testing.T) {
	withLoginBanExemptEnv(t)
	now := time.Now()
	if err := addLoginBanExempt("203.0.113.7", "a", time.Hour, now); err != nil {
		t.Fatal(err)
	}
	if err := addLoginBanExempt("203.0.113.8", "b", 3*time.Hour, now); err != nil {
		t.Fatal(err)
	}
	if err := maintainLoginBanExempt(now.Add(2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := whitelistIPs(t); len(got) != 1 || got[0] != "203.0.113.8" {
		t.Fatalf("after expiry = %v", got)
	}
	if len(loginBanExemptPrefixes(now.Add(2*time.Hour))) != 1 {
		t.Fatal("blocklist protection should list the one left")
	}
	if _, err := csLoginAllowlistApplyHandler(context.Background(), json.RawMessage(`{"enabled":false,"ttl_hours":168}`)); err != nil {
		t.Fatal(err)
	}
	if got := whitelistIPs(t); got != nil {
		t.Fatalf("setting off left %v", got)
	}
	// Off: the watcher's upkeep keeps it empty even if an add slipped in.
	if err := addLoginBanExempt("203.0.113.9", "c", time.Hour, now); err != nil {
		t.Fatal(err)
	}
	if err := maintainLoginBanExempt(now); err != nil {
		t.Fatal(err)
	}
	if got := whitelistIPs(t); got != nil {
		t.Fatalf("upkeep while off left %v", got)
	}
}

func TestLoginIPsListAndRemove(t *testing.T) {
	withLoginBanExemptEnv(t)
	if err := addLoginBanExempt("203.0.113.7", "panel login a@b", time.Hour, time.Now()); err != nil {
		t.Fatal(err)
	}
	out, err := csLoginIPsListHandler(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	items := out.(map[string]any)["items"].([]csLoginIPWire)
	if len(items) != 1 || items[0].Value != "203.0.113.7" || items[0].Reason != "panel login a@b" || items[0].ExpiresAt == "" {
		t.Fatalf("items = %+v", items)
	}
	if _, err := csLoginIPsRemoveHandler(context.Background(), json.RawMessage(`{"value":"203.0.113.7"}`)); err != nil {
		t.Fatal(err)
	}
	if whitelistIPs(t) != nil {
		t.Fatal("file left after the last remove")
	}
	_, err = csLoginIPsRemoveHandler(context.Background(), json.RawMessage(`{"value":"203.0.113.7"}`))
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeNotFound {
		t.Fatalf("err = %v, want not_found", err)
	}
}

func TestLoginBanExempt_NoCrowdSec(t *testing.T) {
	withLoginBanExemptEnv(t)
	crowdsecConfigDir = filepath.Join(t.TempDir(), "absent")
	raw, _ := json.Marshal(map[string]any{"value": "203.0.113.7", "reason": "panel login a@b"})
	_, err := csLoginIPsAddHandler(context.Background(), raw)
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeFailedPrecondition || ae.Message != agentwire.MsgCrowdSecNotInstalled {
		t.Fatalf("err = %v", err)
	}
	if _, serr := os.Stat(loginBanExemptStatePath); !os.IsNotExist(serr) {
		t.Fatal("state written on a box without CrowdSec")
	}
}
