package commands

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// setHostnameRun runs system.set_hostname against temp copies of /etc/hosts
// and config.toml (config "" = no config file), with hostnamectl stubbed.
// It returns the files afterwards and the hostnamectl argv.
func setHostnameRun(t *testing.T, hosts, config, params string) (gotHosts, gotConfig string, argv []string, err error) {
	t.Helper()
	dir := t.TempDir()
	hostsPath := filepath.Join(dir, "hosts")
	configPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(hostsPath, []byte(hosts), 0o644); err != nil {
		t.Fatal(err)
	}
	if config != "" {
		if err := os.WriteFile(configPath, []byte(config), 0o640); err != nil {
			t.Fatal(err)
		}
	}

	origHosts, origConfig, origExec := setHostnameHostsPath, setHostnameConfigPath, execCommandContext
	t.Cleanup(func() {
		setHostnameHostsPath, setHostnameConfigPath, execCommandContext = origHosts, origConfig, origExec
	})
	setHostnameHostsPath, setHostnameConfigPath = hostsPath, configPath
	execCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		argv = append([]string{name}, args...)
		return exec.CommandContext(ctx, "true")
	}

	_, err = systemSetHostnameHandler(context.Background(), json.RawMessage(params))
	h, rerr := os.ReadFile(hostsPath)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if config != "" {
		c, rerr := os.ReadFile(configPath)
		if rerr != nil {
			t.Fatal(rerr)
		}
		gotConfig = string(c)
		fi, serr := os.Stat(configPath)
		if serr != nil {
			t.Fatal(serr)
		}
		if fi.Mode().Perm() != 0o640 {
			t.Errorf("config.toml mode %o after the rename, want 640 (the panel reads it as group jabali)", fi.Mode().Perm())
		}
	}
	return string(h), gotConfig, argv, err
}

const debianHosts = "127.0.0.1\tlocalhost\n::1\tlocalhost ip6-localhost ip6-loopback\n"

// A panel hostname change maps the new name to the server's public IP in
// /etc/hosts, the way install.sh does, never to Debian's 127.0.1.1 loopback
// line, and drops the 127.0.1.1 lines install.sh strips on every run.
func TestSystemSetHostname_EtcHosts(t *testing.T) {
	const ip = "203.0.113.5"
	for _, tc := range []struct {
		name  string
		hosts string
		ipv4  string
		want  string
	}{
		{
			name:  "Debian's 127.0.1.1 line is replaced by the public IP",
			hosts: debianHosts + "127.0.1.1\told.example.com old\n",
			ipv4:  ip,
			want:  debianHosts + ip + "\tnew.example.com\n",
		},
		{
			name:  "a name that only contains the new one (mail.<new>) doesn't count",
			hosts: debianHosts + ip + "\tmail.new.example.com\tautoconfig.new.example.com\n",
			ipv4:  ip,
			want:  debianHosts + ip + "\tmail.new.example.com\tautoconfig.new.example.com\n" + ip + "\tnew.example.com\n",
		},
		{
			name:  "an old name that ends with the new one doesn't count",
			hosts: debianHosts + ip + "\tpanel.new.example.com\n",
			ipv4:  ip,
			want:  debianHosts + ip + "\tpanel.new.example.com\n" + ip + "\tnew.example.com\n",
		},
		{
			name:  "the old name's public-IP lines stay",
			hosts: debianHosts + ip + "\told.example.com\n" + ip + "\tmail.old.example.com\tautoconfig.old.example.com\n",
			ipv4:  ip,
			want:  debianHosts + ip + "\told.example.com\n" + ip + "\tmail.old.example.com\tautoconfig.old.example.com\n" + ip + "\tnew.example.com\n",
		},
		{
			name:  "already mapped to the public IP: unchanged",
			hosts: debianHosts + ip + "\tnew.example.com\n",
			ipv4:  ip,
			want:  debianHosts + ip + "\tnew.example.com\n",
		},
		{
			name:  "mapped to another address by the operator (NAT): unchanged",
			hosts: debianHosts + "10.0.0.5 NEW.example.com\n",
			ipv4:  ip,
			want:  debianHosts + "10.0.0.5 NEW.example.com\n",
		},
		{
			name:  "a commented-out mapping doesn't count",
			hosts: debianHosts + "# " + ip + " new.example.com\n" + ip + " other.example.com # new.example.com\n",
			ipv4:  ip,
			want:  debianHosts + "# " + ip + " new.example.com\n" + ip + " other.example.com # new.example.com\n" + ip + "\tnew.example.com\n",
		},
		{
			name:  "no public IP: the loopback line goes, nothing is added",
			hosts: debianHosts + "127.0.1.1\tnew.example.com\n",
			want:  debianHosts,
		},
		{
			name:  "a file without a final newline",
			hosts: "127.0.0.1 localhost",
			ipv4:  ip,
			want:  "127.0.0.1 localhost\n" + ip + "\tnew.example.com\n",
		},
		{
			name:  "only 127.0.1.1 itself is dropped, not 127.0.1.10",
			hosts: debianHosts + "127.0.1.10\tbox.lan\n",
			ipv4:  ip,
			want:  debianHosts + "127.0.1.10\tbox.lan\n" + ip + "\tnew.example.com\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params := `{"hostname":"new.example.com"}`
			if tc.ipv4 != "" {
				params = `{"hostname":"new.example.com","public_ipv4":"` + tc.ipv4 + `"}`
			}
			got, _, argv, err := setHostnameRun(t, tc.hosts, "", params)
			if err != nil {
				t.Fatalf("handler: %v", err)
			}
			if strings.Join(argv, " ") != "hostnamectl set-hostname new.example.com" {
				t.Errorf("hostnamectl argv %q", argv)
			}
			if got != tc.want {
				t.Fatalf("/etc/hosts:\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

// config.toml's [server] hostname follows the rename: `jabali update` renders
// kratos.yml from it (install_kratos), and the CLI builds its SSO base URL and
// the placeholder email of a user created without --email from it.
func TestSystemSetHostname_ConfigToml(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string
		want   string
	}{
		{
			name:   "the [server] hostname, alignment kept",
			config: "[database]\ndsn = \"x\"\n\n[server]\naddr        = \"unix:/run/jabali-panel/api.sock\"\nhostname    = \"old.example.com\"\npublic_ipv4 = \"203.0.113.5\"\nns1_name    = \"ns1.old.example.com\"\n",
			want:   "[database]\ndsn = \"x\"\n\n[server]\naddr        = \"unix:/run/jabali-panel/api.sock\"\nhostname    = \"new.example.com\"\npublic_ipv4 = \"203.0.113.5\"\nns1_name    = \"ns1.old.example.com\"\n",
		},
		{
			name:   "a hostname key in another section is left alone",
			config: "[mail]\nhostname = \"mail.old.example.com\"\n\n[server]\nhostname = \"old.example.com\" # seeded at install\n\n[sso]\nhostname = \"old.example.com\"\n",
			want:   "[mail]\nhostname = \"mail.old.example.com\"\n\n[server]\nhostname = \"new.example.com\" # seeded at install\n\n[sso]\nhostname = \"old.example.com\"\n",
		},
		{
			name:   "no [server] hostname: unchanged",
			config: "[server]\naddr = \"unix:/run/x.sock\"\n[other]\nhostname = \"old.example.com\"\n",
			want:   "[server]\naddr = \"unix:/run/x.sock\"\n[other]\nhostname = \"old.example.com\"\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, got, _, err := setHostnameRun(t, debianHosts, tc.config, `{"hostname":"new.example.com","public_ipv4":"203.0.113.5"}`)
			if err != nil {
				t.Fatalf("handler: %v", err)
			}
			if got != tc.want {
				t.Fatalf("config.toml:\n%s\nwant\n%s", got, tc.want)
			}
		})
	}

	t.Run("no config.toml is not an error", func(t *testing.T) {
		if _, _, _, err := setHostnameRun(t, debianHosts, "", `{"hostname":"new.example.com"}`); err != nil {
			t.Fatalf("handler: %v", err)
		}
	})
}

func TestSystemSetHostname_RejectsBadPublicIPv4(t *testing.T) {
	for _, ip := range []string{"not-an-ip", "2001:db8::1", "203.0.113.5\n127.0.1.1 evil", "203.0.113.5 x"} {
		params, _ := json.Marshal(map[string]string{"hostname": "new.example.com", "public_ipv4": ip})
		hosts, _, argv, err := setHostnameRun(t, debianHosts, "", string(params))
		ae, ok := err.(*agentwire.AgentError)
		if !ok || ae.Code != agentwire.CodeInvalidArgument {
			t.Fatalf("public_ipv4 %q: err %v, want invalid argument", ip, err)
		}
		if argv != nil || hosts != debianHosts {
			t.Fatalf("public_ipv4 %q: hostnamectl ran (%q) or /etc/hosts changed", ip, argv)
		}
	}
}
