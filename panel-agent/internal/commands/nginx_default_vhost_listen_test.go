package commands

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
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

func callDefaultVhostListen(t *testing.T, ip string) (nginxDefaultVhostListenResponse, error) {
	t.Helper()
	params, _ := json.Marshal(map[string]string{"ipv4": ip})
	raw, err := nginxDefaultVhostListenHandler(context.Background(), params)
	if err != nil {
		return nginxDefaultVhostListenResponse{}, err
	}
	return raw.(nginxDefaultVhostListenResponse), nil
}

// The default IPv4 moved from .121 to .9 and both are on the interface: the
// three address-specific lines follow it and nothing else in the file moves.
func TestNginxDefaultVhostListen_FollowsTheDefaultIP(t *testing.T) {
	path := withPanelLandingVhost(t, defaultVhostListenFixture("203.0.113.121"))
	withServerAddrs(t, "203.0.113.121", "203.0.113.9")

	resp, err := callDefaultVhostListen(t, "203.0.113.9")
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !resp.Rewritten || resp.Replacements != 3 || resp.Reason != "203.0.113.121 -> 203.0.113.9" {
		t.Fatalf("resp = %+v, want 3 lines rewritten from .121", resp)
	}
	got, _ := os.ReadFile(path)
	if want := defaultVhostListenFixture("203.0.113.9"); string(got) != want {
		t.Fatalf("file =\n%s\nwant\n%s", got, want)
	}

	// Second call: nothing to do, nothing written.
	resp, err = callDefaultVhostListen(t, "203.0.113.9")
	if err != nil || resp.Rewritten || resp.Reason != "already current" {
		t.Fatalf("second call = %+v, %v; want already current", resp, err)
	}
}

// An address that is not on any interface would make nginx fail to start;
// the file is left alone.
func TestNginxDefaultVhostListen_RefusesAnAddressNotOnTheServer(t *testing.T) {
	path := withPanelLandingVhost(t, defaultVhostListenFixture("203.0.113.121"))
	withServerAddrs(t, "203.0.113.121")

	_, err := callDefaultVhostListen(t, "203.0.113.9")
	var ae *agentwire.AgentError
	if !errors.As(err, &ae) || ae.Code != agentwire.CodeFailedPrecondition {
		t.Fatalf("err = %v, want failed_precondition", err)
	}
	if got, _ := os.ReadFile(path); string(got) != defaultVhostListenFixture("203.0.113.121") {
		t.Fatalf("file changed:\n%s", got)
	}
}

func TestNginxDefaultVhostListen_RejectsBadInput(t *testing.T) {
	path := withPanelLandingVhost(t, defaultVhostListenFixture("203.0.113.121"))
	withServerAddrs(t, "203.0.113.121", "127.0.0.1", "::1")
	for _, in := range []string{"", "not-an-ip", "127.0.0.1", "0.0.0.0", "2001:db8::1", "203.0.113.009", "203.0.113.9;"} {
		var ae *agentwire.AgentError
		if _, err := callDefaultVhostListen(t, in); !errors.As(err, &ae) || ae.Code != agentwire.CodeInvalidArgument {
			t.Errorf("%q: err = %v, want invalid_argument", in, err)
		}
	}
	if got, _ := os.ReadFile(path); string(got) != defaultVhostListenFixture("203.0.113.121") {
		t.Fatalf("file changed:\n%s", got)
	}
}

func TestNginxDefaultVhostListen_NginxTestFailureRollsBack(t *testing.T) {
	path := withPanelLandingVhost(t, defaultVhostListenFixture("203.0.113.121"))
	withServerAddrs(t, "203.0.113.121", "203.0.113.9")
	prev := execCommandContext
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name == "nginx" && len(args) > 0 && args[0] == "-t" {
			return exec.CommandContext(ctx, "false")
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { execCommandContext = prev })

	if _, err := callDefaultVhostListen(t, "203.0.113.9"); err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("err = %v, want a rolled-back nginx -t failure", err)
	}
	if got, _ := os.ReadFile(path); string(got) != defaultVhostListenFixture("203.0.113.121") {
		t.Fatalf("file not rolled back:\n%s", got)
	}
}

// A file without address-specific lines (a server installed without a public
// IPv4) is not an error and is not written.
func TestNginxDefaultVhostListen_NoSpecificLines(t *testing.T) {
	in := "server {\n    listen 80 default_server;\n    server_name _;\n}\n"
	path := withPanelLandingVhost(t, in)
	withServerAddrs(t, "203.0.113.9")

	resp, err := callDefaultVhostListen(t, "203.0.113.9")
	if err != nil || resp.Rewritten {
		t.Fatalf("resp = %+v, %v; want no rewrite", resp, err)
	}
	if got, _ := os.ReadFile(path); string(got) != in {
		t.Fatalf("file changed:\n%s", got)
	}
}

// The panel's side of the contract: the request it sends decodes into the
// handler's params with nothing dropped.
func TestNginxDefaultVhostListen_PanelContract(t *testing.T) {
	raw, err := os.ReadFile("../../../panel-api/internal/agent/testdata/nginx_default_vhost_listen_request.json")
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var p nginxDefaultVhostListenParams
	if err := dec.Decode(&p); err != nil || p.IPv4 != "203.0.113.9" {
		t.Fatalf("decode = %+v, %v", p, err)
	}
}
