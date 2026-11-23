package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// defaultVhostListenFixture is jabali-default.conf as install.sh writes it for
// a server on ip: the wildcard listeners, a comment that mentions `listen
// ${IPv4}:443`, and the three address-specific lines.
func defaultVhostListenFixture(ip string) string {
	return `server {
    listen 80 default_server;
    listen [::]:80 default_server;
    # Per-domain vhosts bind explicitly with ` + "`listen ${IPv4}:80`" + `
    listen ` + ip + `:80 default_server;
    server_name _;
}

server {
    listen 443 ssl default_server;
    listen [::]:443 ssl default_server;
    # Per-domain vhosts bind ` + "`listen ${IPv4}:443 ssl`" + ` (M24)
    listen ` + ip + `:443 ssl default_server;
    server_name _;
}

server {
    listen 443 ssl;
    listen [::]:443 ssl;
    listen ` + ip + `:443 ssl;
    server_name panel.example.com;
}
`
}

// withServerAddrs makes ips this server's interface addresses for one test.
func withServerAddrs(t *testing.T, ips ...string) {
	t.Helper()
	prev := listInterfaceAddrs
	listInterfaceAddrs = func() ([]net.Addr, error) {
		var out []net.Addr
		for _, s := range ips {
			out = append(out, &net.IPNet{IP: net.ParseIP(s), Mask: net.CIDRMask(24, 32)})
		}
		return out, nil
	}
	t.Cleanup(func() { listInterfaceAddrs = prev })
}

// withRehostFiles points /etc/hosts and the CrowdSec self-whitelist at temp
// files for one test ("" = file absent) and returns their paths.
func withRehostFiles(t *testing.T, hosts, whitelist string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	hp, wp := filepath.Join(dir, "hosts"), filepath.Join(dir, "jabali-self-whitelist.yaml")
	for path, body := range map[string]string{hp: hosts, wp: whitelist} {
		if body != "" {
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	prevH, prevW := setHostnameHostsPath, selfWhitelistPath
	setHostnameHostsPath, selfWhitelistPath = hp, wp
	t.Cleanup(func() { setHostnameHostsPath, selfWhitelistPath = prevH, prevW })
	return hp, wp
}

// selfWhitelistFixture is jabali-self-whitelist.yaml as install.sh writes it.
func selfWhitelistFixture(ips ...string) string {
	s := "# Managed by jabali install.sh (JAB-250). Do NOT hand-edit.\n" +
		"name: jabali/self-whitelist\n" +
		"description: \"Whitelist the host's own public IPs (JAB-250)\"\n" +
		"whitelist:\n" +
		"  reason: \"jabali host self-calls (public IP in tenant logs)\"\n" +
		"  ip:\n"
	for _, ip := range ips {
		s += "    - \"" + ip + "\"\n"
	}
	return s
}

func callDefaultIPv4Rehost(t *testing.T, ip string) (systemDefaultIPv4RehostResponse, error) {
	t.Helper()
	params, _ := json.Marshal(map[string]string{"ipv4": ip})
	raw, err := systemDefaultIPv4RehostHandler(context.Background(), params)
	if err != nil {
		return systemDefaultIPv4RehostResponse{}, err
	}
	return raw.(systemDefaultIPv4RehostResponse), nil
}

// The default IPv4 moved from .121 to .9, and .121 is still on the
// interface: the three listen lines and the /etc/hosts lines on .121 follow,
// the whitelist gains .9 and keeps .121, and nothing else changes.
func TestDefaultIPv4Rehost_FollowsTheDefaultIP(t *testing.T) {
	path := withPanelLandingVhost(t, defaultVhostListenFixture("203.0.113.121"))
	hosts := "127.0.0.1\tlocalhost\n203.0.113.121\tpanel.example.com\n203.0.113.121\tmail.panel.example.com\tautoconfig.panel.example.com # install\n198.51.100.4\tbackup.example.net\n"
	hp, wp := withRehostFiles(t, hosts, selfWhitelistFixture("203.0.113.121"))
	withServerAddrs(t, "203.0.113.121", "203.0.113.9")

	resp, err := callDefaultIPv4Rehost(t, "203.0.113.9")
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	want := systemDefaultIPv4RehostResponse{From: []string{"203.0.113.121"}, ListenRewritten: 3, HostsUpdated: true, WhitelistUpdated: true}
	if fmt.Sprint(resp) != fmt.Sprint(want) {
		t.Fatalf("resp = %+v, want %+v", resp, want)
	}
	if got, _ := os.ReadFile(path); string(got) != defaultVhostListenFixture("203.0.113.9") {
		t.Fatalf("vhost =\n%s", got)
	}
	if got, _ := os.ReadFile(hp); string(got) != strings.ReplaceAll(hosts, "203.0.113.121", "203.0.113.9") {
		t.Fatalf("hosts =\n%s", got)
	}
	if got, _ := os.ReadFile(wp); string(got) != selfWhitelistFixture("203.0.113.121", "203.0.113.9") {
		t.Fatalf("whitelist =\n%s", got)
	}

	// Second call: nothing to do, nothing written.
	resp, err = callDefaultIPv4Rehost(t, "203.0.113.9")
	if err != nil || fmt.Sprint(resp) != fmt.Sprint(systemDefaultIPv4RehostResponse{}) {
		t.Fatalf("second call = %+v, %v; want nothing changed", resp, err)
	}
}

// Once the old address is gone from the server it leaves the whitelist: it
// may be someone else's now.
func TestDefaultIPv4Rehost_DropsAnOldAddressThatLeftTheServer(t *testing.T) {
	withPanelLandingVhost(t, defaultVhostListenFixture("203.0.113.121"))
	_, wp := withRehostFiles(t, "203.0.113.121\tpanel.example.com\n", selfWhitelistFixture("203.0.113.121"))
	withServerAddrs(t, "203.0.113.9")

	if _, err := callDefaultIPv4Rehost(t, "203.0.113.9"); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(wp); string(got) != selfWhitelistFixture("203.0.113.9") {
		t.Fatalf("whitelist =\n%s", got)
	}
}

// Without the security module there is no whitelist; that is not an error.
func TestDefaultIPv4Rehost_NoWhitelistFile(t *testing.T) {
	withPanelLandingVhost(t, defaultVhostListenFixture("203.0.113.121"))
	_, wp := withRehostFiles(t, "203.0.113.121\tpanel.example.com\n", "")
	withServerAddrs(t, "203.0.113.121", "203.0.113.9")

	resp, err := callDefaultIPv4Rehost(t, "203.0.113.9")
	if err != nil || !resp.HostsUpdated || resp.WhitelistUpdated {
		t.Fatalf("resp = %+v, %v", resp, err)
	}
	if _, err := os.Stat(wp); !os.IsNotExist(err) {
		t.Fatalf("whitelist was created: %v", err)
	}
}

func TestRewriteSelfWhitelist(t *testing.T) {
	for _, tc := range []struct {
		name, in, target string
		drop             []string
		want             string
	}{
		{"already listed", selfWhitelistFixture("192.0.2.1", "192.0.2.2"), "192.0.2.2", nil, selfWhitelistFixture("192.0.2.1", "192.0.2.2")},
		{"added after the last entry", selfWhitelistFixture("192.0.2.1", "2001:db8::1"), "192.0.2.2", nil, selfWhitelistFixture("192.0.2.1", "2001:db8::1", "192.0.2.2")},
		{"only entry dropped", selfWhitelistFixture("192.0.2.1"), "192.0.2.2", []string{"192.0.2.1"}, selfWhitelistFixture("192.0.2.2")},
		{"no ip entries", "name: other\n", "192.0.2.2", nil, "name: other\n"},
		{"no trailing newline", strings.TrimSuffix(selfWhitelistFixture("192.0.2.1"), "\n"), "192.0.2.2", nil, selfWhitelistFixture("192.0.2.1", "192.0.2.2")},
	} {
		got, _ := rewriteSelfWhitelist([]byte(tc.in), tc.target, tc.drop)
		if string(got) != tc.want {
			t.Errorf("%s:\n%s\nwant\n%s", tc.name, got, tc.want)
		}
	}
}

// An address that is not on any interface would make nginx fail to start;
// the file is left alone.
func TestDefaultIPv4Rehost_RefusesAnAddressNotOnTheServer(t *testing.T) {
	path := withPanelLandingVhost(t, defaultVhostListenFixture("203.0.113.121"))
	withRehostFiles(t, "203.0.113.121\tpanel.example.com\n", selfWhitelistFixture("203.0.113.121"))
	withServerAddrs(t, "203.0.113.121")

	_, err := callDefaultIPv4Rehost(t, "203.0.113.9")
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeFailedPrecondition {
		t.Fatalf("err = %v, want failed_precondition", err)
	}
	if got, _ := os.ReadFile(path); string(got) != defaultVhostListenFixture("203.0.113.121") {
		t.Fatalf("file changed:\n%s", got)
	}
}

func TestDefaultIPv4Rehost_RejectsBadInput(t *testing.T) {
	path := withPanelLandingVhost(t, defaultVhostListenFixture("203.0.113.121"))
	withRehostFiles(t, "", "")
	withServerAddrs(t, "203.0.113.121", "127.0.0.1", "::1")
	for _, in := range []string{"", "not-an-ip", "127.0.0.1", "0.0.0.0", "2001:db8::1", "203.0.113.009", "203.0.113.9;"} {
		var ae *agentwire.AgentError
		if _, err := callDefaultIPv4Rehost(t, in); !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument {
			t.Errorf("%q: err = %v, want invalid_argument", in, err)
		}
	}
	if got, _ := os.ReadFile(path); string(got) != defaultVhostListenFixture("203.0.113.121") {
		t.Fatalf("file changed:\n%s", got)
	}
}

func TestDefaultIPv4Rehost_NginxTestFailureRollsBack(t *testing.T) {
	path := withPanelLandingVhost(t, defaultVhostListenFixture("203.0.113.121"))
	hp, _ := withRehostFiles(t, "203.0.113.121\tpanel.example.com\n", "")
	withServerAddrs(t, "203.0.113.121", "203.0.113.9")
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "nginx" && len(args) > 0 && args[0] == "-t" {
			return exec.CommandContext(ctx, "false")
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })

	if _, err := callDefaultIPv4Rehost(t, "203.0.113.9"); err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("err = %v, want a rolled-back nginx -t failure", err)
	}
	if got, _ := os.ReadFile(path); string(got) != defaultVhostListenFixture("203.0.113.121") {
		t.Fatalf("file not rolled back:\n%s", got)
	}
	if got, _ := os.ReadFile(hp); string(got) != "203.0.113.121\tpanel.example.com\n" {
		t.Fatalf("hosts changed although nginx refused the config:\n%s", got)
	}
}

// A file without address-specific lines (a server installed without a public
// IPv4) is not an error and is not written.
func TestDefaultIPv4Rehost_NoSpecificLines(t *testing.T) {
	in := "server {\n    listen 80 default_server;\n    server_name _;\n}\n"
	path := withPanelLandingVhost(t, in)
	withRehostFiles(t, "", "")
	withServerAddrs(t, "203.0.113.9")

	resp, err := callDefaultIPv4Rehost(t, "203.0.113.9")
	if err != nil || resp.ListenRewritten != 0 {
		t.Fatalf("resp = %+v, %v; want no rewrite", resp, err)
	}
	if got, _ := os.ReadFile(path); string(got) != in {
		t.Fatalf("file changed:\n%s", got)
	}
}

// The panel's side of the contract: the request it sends decodes into the
// handler's params with nothing dropped.
func TestDefaultIPv4Rehost_PanelContract(t *testing.T) {
	raw, err := os.ReadFile("../../../panel-api/internal/agent/testdata/system_default_ipv4_rehost_request.json")
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var p systemDefaultIPv4RehostParams
	if err := dec.Decode(&p); err != nil || p.IPv4 != "203.0.113.9" {
		t.Fatalf("decode = %+v, %v", p, err)
	}
}
