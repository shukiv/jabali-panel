package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"text/template"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// mail_webadmin.go — GH #243 (ADR-0142). Opt-in nginx reverse-proxy exposing
// Stalwart's WebAdmin UI. Stalwart's admin listener stays pinned to
// 127.0.0.1:8446 (M25 invariant); this vhost is the ONLY door, behind:
//   1. TLS,
//   2. nginx basic-auth (a DEDICATED gateway credential, never the Stalwart
//      admin token),
//   3. an optional source-IP allowlist,
//   4. and Stalwart's own admin login underneath.
//
//	mail.webadmin.apply  {enabled, server_name, ssl_cert_path, ssl_key_path,
//	                      allow_cidrs[]}  -> render/remove vhost; on first
//	                      enable, generate + return the gateway credential once.

const (
	stalwartAdminVhostName = "jabali-stalwart-admin.conf"
)

// stalwartAdminHtpasswd is the nginx basic-auth gateway credential file.
// A var (not const) so tests can redirect it off /etc/nginx.
var stalwartAdminHtpasswd = "/etc/nginx/.jabali-stalwart-admin.htpasswd"

var stalwartAdminVhostTemplate = `# Rendered by panel-agent mail.webadmin.apply (GH #243, ADR-0142).
# Stalwart WebAdmin reverse-proxy on a dedicated port of the PANEL hostname, so
# it reuses the panel cert (name matches; already trusted) and Stalwart owns
# the origin root (its admin SPA uses absolute /api, /logo, /webadmin paths).
# Stalwart stays on 127.0.0.1:8446; this is the only externally reachable door,
# behind TLS{{if .AllowCIDRs}} + IP allowlist{{end}} + Stalwart's own admin login.
server {
    listen {{.Port}} ssl{{.HTTP2Param}};
    listen [::]:{{.Port}} ssl{{.HTTP2Param}};
{{ if .HTTP2Directive }}    {{.HTTP2Directive}}
{{ end }}    server_name {{.ServerName}};

    ssl_certificate {{.SSLCertPath}};
    ssl_certificate_key {{.SSLKeyPath}};
{{range .AllowCIDRs}}    allow {{.}};
{{end}}{{if .AllowCIDRs}}    deny all;
{{end}}
    client_max_body_size 25m;

    # This door is gated by TLS + IP allowlist + Stalwart's own admin login
    # (basic-auth was dropped — it collided with Stalwart's own API auth on the
    # Authorization header; see ADR-0142). The global CrowdSec
    # nginx bouncer / AppSec WAF false-positives on the WebAdmin's own admin
    # paths (/admin, /login, /webadmin, /api/auth read as admin-panel scans),
    # returning 403. A no-op server-scope access_by_lua override exempts only
    # this vhost from the bouncer; every other server still runs CrowdSec.
    access_by_lua_block { return }

    # Stalwart serves its WebAdmin SPA at /admin/ (base href=/admin/); the bare
    # root returns a JSON 404. Send / there so the panel-surfaced URL just works.
    location = / {
        return 302 /admin/;
    }

    location / {
        proxy_pass http://127.0.0.1:8446;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_read_timeout 300s;
    }
}
`

type webadminApplyParams struct {
	Enabled     bool     `json:"enabled"`
	ServerName  string   `json:"server_name"`
	Port        int      `json:"port"`
	SSLCertPath string   `json:"ssl_cert_path"`
	SSLKeyPath  string   `json:"ssl_key_path"`
	AllowCIDRs  []string `json:"allow_cidrs,omitempty"`
}

type webadminVhostTemplateData struct {
	ServerName  string
	Port        int
	SSLCertPath string
	SSLKeyPath  string
	AllowCIDRs  []string
	// HTTP/2 form per host nginx version (GH #292).
	HTTP2Param     string
	HTTP2Directive string
}

type webadminApplyResponse struct {
	Ok      bool `json:"ok"`
	Changed bool `json:"changed"`
}

func mailWebadminApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p webadminApplyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}

	confPath := filepath.Join(mailVhostSitesAvailable, stalwartAdminVhostName)
	enabledPath := filepath.Join(mailVhostSitesEnabled, stalwartAdminVhostName)

	if !p.Enabled {
		changed := false
		for _, f := range []string{enabledPath, confPath} {
			if _, err := os.Lstat(f); err == nil {
				if err := os.Remove(f); err != nil {
					return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("rm %s: %v", f, err)}
				}
				changed = true
			}
		}
		// Remove any stale htpasswd from the basic-auth era (ADR-0142 dropped
		// it). Reload only when the vhost actually changed.
		_ = os.Remove(stalwartAdminHtpasswd)
		if changed {
			if err := nginxTestAndReload(ctx); err != nil {
				return nil, err
			}
		}
		ufwSetWebadminPort(ctx, 0, false) // best-effort: close the port
		return webadminApplyResponse{Ok: true, Changed: changed}, nil
	}

	// enabled=true
	if err := validateDomainNameForShell(p.ServerName); err != nil {
		return nil, err
	}
	if p.SSLCertPath == "" || p.SSLKeyPath == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "ssl_cert_path and ssl_key_path required"}
	}
	for _, c := range p.AllowCIDRs {
		if !validCIDROrIP(c) {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("invalid allow_cidr %q", c)}
		}
	}

	// Clean up any stale htpasswd from the basic-auth era (ADR-0142 dropped it).
	_ = os.Remove(stalwartAdminHtpasswd)

	if p.Port < 1 || p.Port > 65535 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "port out of range"}
	}
	swh2 := nginxHTTP2()
	data := webadminVhostTemplateData{
		ServerName:     p.ServerName,
		Port:           p.Port,
		SSLCertPath:    p.SSLCertPath,
		SSLKeyPath:     p.SSLKeyPath,
		AllowCIDRs:     p.AllowCIDRs,
		HTTP2Param:     swh2.Param,
		HTTP2Directive: swh2.Directive,
	}
	tmpl, err := template.New("swadmin").Parse(stalwartAdminVhostTemplate)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("template: %v", err)}
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, data); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("render: %v", err)}
	}

	changed := false
	if cur, _ := os.ReadFile(confPath); !bytes.Equal(cur, rendered.Bytes()) {
		if err := os.WriteFile(confPath, rendered.Bytes(), 0o644); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write vhost: %v", err)}
		}
		changed = true
	}
	if _, err := os.Lstat(enabledPath); os.IsNotExist(err) {
		if err := os.Symlink(confPath, enabledPath); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("symlink: %v", err)}
		}
		changed = true
	}
	// Open the WebAdmin port in UFW so it is reachable (best-effort; a no-op
	// when UFW is inactive). The IP allowlist + Stalwart's own login are the gate.
	ufwSetWebadminPort(ctx, p.Port, true)
	if changed {
		if err := nginxTestAndReload(ctx); err != nil {
			return nil, err
		}
	}
	return webadminApplyResponse{Ok: true, Changed: changed}, nil
}

// validCIDROrIP accepts a bare IP or a CIDR (the two shapes nginx allow/deny
// take). Rejects anything else so a malformed value can't break nginx -t.
func validCIDROrIP(s string) bool {
	if net.ParseIP(s) != nil {
		return true
	}
	_, _, err := net.ParseCIDR(s)
	return err == nil
}

// ufwSetWebadminPort opens (open=true) or closes the WebAdmin port in UFW.
// Best-effort: errors (UFW inactive, not installed) are ignored — the IP
// allowlist + Stalwart's own login are the security gate, not the firewall.
func ufwSetWebadminPort(ctx context.Context, port int, open bool) {
	if _, err := exec.LookPath("ufw"); err != nil {
		return
	}
	rule := stalwartAdminUFWPort
	if port > 0 {
		rule = fmt.Sprintf("%d/tcp", port)
	}
	args := []string{"--force", "delete", "allow", rule}
	if open {
		args = []string{"allow", rule}
	}
	_ = execCommandContext(ctx, "ufw", args...).Run()
}

// stalwartAdminUFWPort is used by the close path when no port is supplied.
var stalwartAdminUFWPort = "8449/tcp"

func init() {
	Default.Register("mail.webadmin.apply", mailWebadminApplyHandler)
}
