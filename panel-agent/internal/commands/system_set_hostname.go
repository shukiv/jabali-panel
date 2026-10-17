package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// systemSetHostnameParams is the input shape for system.set_hostname.
type systemSetHostnameParams struct {
	Hostname   string `json:"hostname"`
	PublicIPv4 string `json:"public_ipv4,omitempty"`
}

// systemSetHostnameResponse is the output shape. Returns the hostname that
// was actually applied so the panel can confirm the round-trip succeeded.
type systemSetHostnameResponse struct {
	Hostname      string `json:"hostname"`
	HostsUpdated  bool   `json:"hosts_updated"`
	ConfigUpdated bool   `json:"config_updated"`
}

// hostnameAllowedRE matches a standard DNS hostname. Intentionally loose —
// the panel API enforces stricter validation before we ever get here; this
// is a belt-and-braces check to block shell metacharacters from reaching
// hostnamectl.
// Paths system.set_hostname rewrites; vars so tests can redirect them.
var (
	setHostnameHostsPath  = "/etc/hosts"
	setHostnameConfigPath = "/etc/jabali/config.toml"
)

var hostnameAllowedRE = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$`)

// systemSetHostnameHandler applies a new panel hostname to the OS:
//
//  1. hostnamectl set-hostname;
//  2. /etc/hosts: every `127.0.1.1` line goes, and the name is mapped to the
//     server's public IPv4 unless a line already maps it — the same as
//     install.sh does at install and on every run. Debian's 127.0.1.1 line
//     shadows real DNS for anything that resolves the panel hostname locally.
//     A line the operator wrote for the name (another address, NAT) is kept,
//     and so are the old name's lines: the panel mail hostname doesn't follow
//     a rename (#1546), so mail.<old> must keep resolving here;
//  3. /etc/jabali/config.toml: the [server] hostname follows the rename.
//     It is the install-time seed: install_kratos (under `jabali update`) and
//     the CLI's SSO base URL and placeholder email fall back to it when the
//     server_settings row can't be read, and kratos.yml used to be rendered
//     from it on every update, so it must not keep the old name.
func systemSetHostnameHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p systemSetHostnameParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	p.Hostname = strings.TrimSpace(p.Hostname)
	if p.Hostname == "" || len(p.Hostname) > 253 || !hostnameAllowedRE.MatchString(p.Hostname) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "invalid hostname",
		}
	}
	// Empty is allowed (an unset public IP): /etc/hosts then only loses its
	// 127.0.1.1 lines and DNS answers for the name.
	p.PublicIPv4 = strings.TrimSpace(p.PublicIPv4)
	if p.PublicIPv4 != "" {
		ip := net.ParseIP(p.PublicIPv4)
		if ip == nil || ip.To4() == nil || ip.String() != p.PublicIPv4 {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInvalidArgument,
				Message: "invalid public_ipv4",
			}
		}
	}

	// hostnamectl handles both systemd-hostnamed and the /etc/hostname
	// write. Fall through with a clear error on distros that lack it.
	cmd := execCommandContext(ctx, "hostnamectl", "set-hostname", p.Hostname)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("hostnamectl failed: %s: %w", strings.TrimSpace(string(out)), err)
	}

	resp := systemSetHostnameResponse{Hostname: p.Hostname}
	hosts, err := os.ReadFile(setHostnameHostsPath)
	if err != nil {
		return nil, fmt.Errorf("read /etc/hosts: %w", err)
	}
	if out, changed := rewriteHostsForHostname(hosts, p.Hostname, p.PublicIPv4); changed {
		if err := writeFilePreservingMeta(setHostnameHostsPath, out); err != nil {
			return nil, fmt.Errorf("write /etc/hosts: %w", err)
		}
		resp.HostsUpdated = true
	}

	config, err := os.ReadFile(setHostnameConfigPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// No panel config here (a dev box): nothing to follow.
	case err != nil:
		return nil, fmt.Errorf("read config.toml: %w", err)
	default:
		if out, changed := rewriteConfigServerHostname(config, p.Hostname); changed {
			// Mode and owner are kept (0640 root:jabali): the panel reads it.
			if err := writeFilePreservingMeta(setHostnameConfigPath, out); err != nil {
				return nil, fmt.Errorf("write config.toml: %w", err)
			}
			resp.ConfigUpdated = true
		}
	}
	return resp, nil
}

// hostsLoopbackRE is install.sh's `/^127\.0\.1\.1\([[:space:]]\|$\)/d`.
var hostsLoopbackRE = regexp.MustCompile(`^127\.0\.1\.1([ \t]|$)`)

// rewriteHostsForHostname drops every 127.0.1.1 line and, when publicIPv4 is
// set and no remaining line maps hostname (an exact, case-insensitive name
// field — not a substring, so mail.<host> or <x>.<host> don't count), appends
// `<publicIPv4>\t<hostname>`. Every other line is kept as it was.
func rewriteHostsForHostname(data []byte, hostname, publicIPv4 string) ([]byte, bool) {
	lines := strings.SplitAfter(string(data), "\n")
	var b strings.Builder
	mapped := false
	for _, line := range lines {
		if line == "" {
			continue
		}
		if hostsLoopbackRE.MatchString(line) {
			continue
		}
		b.WriteString(line)
		entry := line
		if i := strings.IndexByte(entry, '#'); i >= 0 {
			entry = entry[:i]
		}
		fields := strings.Fields(entry)
		for _, name := range fields[min(1, len(fields)):] {
			if strings.EqualFold(name, hostname) {
				mapped = true
			}
		}
	}
	out := b.String()
	if !mapped && publicIPv4 != "" {
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += publicIPv4 + "\t" + hostname + "\n"
	}
	return []byte(out), out != string(data)
}

var (
	tomlSectionRE  = regexp.MustCompile(`^\s*\[([^\]]+)\]`)
	tomlHostnameRE = regexp.MustCompile(`^(\s*hostname\s*=\s*)"[^"]*"(.*)$`)
)

// rewriteConfigServerHostname sets the quoted value of the first `hostname`
// key in config.toml's [server] section, keeping the line's spacing and any
// trailing comment. A hostname key in another section is not touched. The
// caller validated hostname (hostnameAllowedRE), so it can't carry a quote.
func rewriteConfigServerHostname(data []byte, hostname string) ([]byte, bool) {
	lines := strings.SplitAfter(string(data), "\n")
	section := ""
	for i, line := range lines {
		if m := tomlSectionRE.FindStringSubmatch(line); m != nil {
			section = strings.TrimSpace(m[1])
			continue
		}
		if section != "server" {
			continue
		}
		body := strings.TrimSuffix(line, "\n")
		m := tomlHostnameRE.FindStringSubmatch(body)
		if m == nil {
			continue
		}
		lines[i] = m[1] + `"` + hostname + `"` + m[2] + line[len(body):]
		out := strings.Join(lines, "")
		return []byte(out), out != string(data)
	}
	return data, false
}

func init() {
	Default.Register("system.set_hostname", systemSetHostnameHandler)
}
