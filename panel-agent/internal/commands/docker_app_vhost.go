package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"text/template"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// docker_app_vhost.go renders the standalone nginx vhost for a docker
// marketplace app that opted into reverse_proxy=true on a domain
// (managed_by='docker_app'). It is the docker-app analogue of
// webmail_vhost.go: a self-contained server block that proxies all
// traffic to the app's loopback host port — NO tenant docroot, Linux
// username, or PHP-FPM socket.
//
// Why a dedicated verb instead of the tenant domain.create path:
// docker-app domains are admin-owned (installs are admin-only) and have
// an empty docroot, so the reconciler's createDomainOnAgent rejects them
// outright (`user has no username for domain`, and the agent's
// docroot-must-start-with-/home validator). Routing them here keeps the
// tenant vhost template (and its blast radius) completely untouched.
//
//   - docker_app.vhost_apply  — render + write + nginx reload
//   - docker_app.vhost_remove — rm + nginx reload
//
// File naming is `<domain>-dockerapp.conf` so it never collides with the
// tenant `<domain>.conf` (domain.create) or `<domain>-mail.conf`
// (webmail.vhost_apply).

// dockerAppUpstreamRE constrains the proxy upstream to a loopback host
// port. Docker apps bind their reverse-proxied port to 127.0.0.1
// (bind_interface=loopback), so the panel only ever sends
// http://127.0.0.1:<port>. Pinning the shape here keeps untrusted-looking
// input out of the rendered nginx config even though the caller (the
// reconciler) is trusted.
var dockerAppUpstreamRE = regexp.MustCompile(`^http://127\.0\.0\.1:[1-9][0-9]{1,4}$`)

// dockerAppVhostTemplate renders a proxy-only vhost. listen-line shape
// mirrors webmail_vhost.go / domain_create.go so the app's vhost joins
// nginx's specific-IP listener pool (M24) and SNI stays deterministic
// when the host binds public IPs explicitly.
const dockerAppVhostTemplate = `# Rendered by panel-agent docker_app.vhost_apply.
# DO NOT EDIT — managed by the jabali docker-app marketplace.
# Proxy-only vhost for a managed_by='docker_app' domain.

server {
{{ if .ListenIPv4 }}    listen {{.ListenIPv4}}:443 ssl{{.HTTP2Param}};
{{ else }}    listen 443 ssl{{.HTTP2Param}};
{{ end }}{{ if .ListenIPv6 }}    listen [{{.ListenIPv6}}]:443 ssl{{.HTTP2Param}};
{{ else }}    listen [::]:443 ssl{{.HTTP2Param}};
{{ end }}{{ if .HTTP2Directive }}    {{.HTTP2Directive}}
{{ end }}    server_name {{.DomainName}};

    ssl_certificate {{.SSLCertPath}};
    ssl_certificate_key {{.SSLKeyPath}};

    # JAB-406: the app's assets (JS/CSS/JSON/SVG) are served by the upstream
    # via the proxied location / below, so nginx must compress PROXIED
    # responses — gzip_proxied defaults to off and would skip them entirely,
    # shipping every text asset raw (a typical SPA main bundle is ~2 MB, ~3.5x
    # larger uncompressed). Apps that already compress their own responses are
    # unaffected (nginx never double-gzips). Mirrors the panel vhost (JAB-142).
    gzip on;
    gzip_proxied any;
    gzip_vary on;
    gzip_min_length 1024;
    gzip_types application/javascript text/javascript text/css application/json image/svg+xml application/manifest+json;

    location / {
        proxy_pass {{.Upstream}};
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto https;
        proxy_http_version 1.1;
{{ if .Websocket }}        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
{{ end }}        proxy_read_timeout 3600s;
        proxy_send_timeout 3600s;
    }
}

server {
{{ if .ListenIPv4 }}    listen {{.ListenIPv4}}:80;
{{ else }}    listen 80;
{{ end }}{{ if .ListenIPv6 }}    listen [{{.ListenIPv6}}]:80;
{{ else }}    listen [::]:80;
{{ end }}    server_name {{.DomainName}};

    # ACME HTTP-01 webroot — served from the shared panel ACME root so a
    # public LE issuance (once the operator points DNS at this host)
    # validates. Harmless when DNS isn't public yet (self-signed
    # fallback keeps HTTPS working in the meantime).
    location ^~ /.well-known/acme-challenge/ {
        default_type "text/plain";
        root /var/www/jabali-acme;
        try_files $uri =404;
    }

    location / {
        return 301 https://$host$request_uri;
    }
}
`

type dockerAppVhostApplyParams struct {
	DomainName  string `json:"domain_name"`
	Upstream    string `json:"upstream"`
	SSLCertPath string `json:"ssl_cert_path"`
	SSLKeyPath  string `json:"ssl_key_path"`
	Websocket   bool   `json:"websocket,omitempty"`
	ListenIPv4  string `json:"listen_ipv4,omitempty"`
	ListenIPv6  string `json:"listen_ipv6,omitempty"`
	// HTTP/2 form per host nginx version (GH #292); set before render, not wire.
	HTTP2Param     string `json:"-"`
	HTTP2Directive string `json:"-"`
}

func dockerAppVhostPaths(domain string) (configPath, enabledPath string) {
	name := domain + "-dockerapp.conf"
	return filepath.Join(mailVhostSitesAvailable, name), filepath.Join(mailVhostSitesEnabled, name)
}

func dockerAppVhostApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	if len(params) == 0 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "params required"}
	}
	var p dockerAppVhostApplyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if err := validateDomainNameForShell(p.DomainName); err != nil {
		return nil, err
	}
	if !dockerAppUpstreamRE.MatchString(p.Upstream) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("upstream %q must be http://127.0.0.1:<port>", p.Upstream),
		}
	}
	if p.SSLCertPath == "" || p.SSLKeyPath == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "ssl_cert_path and ssl_key_path are required",
		}
	}

	h2 := nginxHTTP2()
	p.HTTP2Param, p.HTTP2Directive = h2.Param, h2.Directive
	tmpl, err := template.New("dockerappvhost").Parse(dockerAppVhostTemplate)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("template parse: %v", err)}
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("template execute: %v", err)}
	}

	// JAB-71: serialize the whole write→test→reload (both the fast-path symlink
	// branch and the full write) against every other nginx op.
	nginxOpMu.Lock()
	defer nginxOpMu.Unlock()

	configPath, enabledPath := dockerAppVhostPaths(p.DomainName)

	// Hash-gated write — skip write + reload when on-disk content matches.
	if existing, err := os.ReadFile(configPath); err == nil && bytes.Equal(existing, rendered.Bytes()) {
		if _, err := os.Lstat(enabledPath); os.IsNotExist(err) {
			if err := os.Symlink(configPath, enabledPath); err != nil {
				return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("re-link enabled path: %v", err)}
			}
			if err := nginxTestAndReload(ctx); err != nil {
				return nil, err
			}
			return webmailVhostResponse{Ok: true, Path: configPath, Changed: true}, nil
		}
		return webmailVhostResponse{Ok: true, Path: configPath, Changed: false}, nil
	}

	tmp := configPath + ".tmp"
	if err := os.WriteFile(tmp, rendered.Bytes(), 0644); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write tmp vhost: %v", err)}
	}
	if err := os.Rename(tmp, configPath); err != nil {
		os.Remove(tmp)
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("rename vhost: %v", err)}
	}

	os.Remove(enabledPath)
	if err := os.Symlink(configPath, enabledPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("symlink enabled: %v", err)}
	}

	if err := nginxTestAndReload(ctx); err != nil {
		// Roll back: the bad file isn't loaded yet (no reload since
		// rename), but leaving it in sites-enabled would break the next
		// reload by anyone else.
		os.Remove(enabledPath)
		os.Remove(configPath)
		return nil, err
	}
	return webmailVhostResponse{Ok: true, Path: configPath, Changed: true}, nil
}

type dockerAppVhostRemoveParams struct {
	DomainName string `json:"domain_name"`
}

func dockerAppVhostRemoveHandler(ctx context.Context, params json.RawMessage) (any, error) {
	if len(params) == 0 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "params required"}
	}
	var p dockerAppVhostRemoveParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if err := validateDomainNameForShell(p.DomainName); err != nil {
		return nil, err
	}

	// JAB-71: serialize remove→test→reload against every other nginx op.
	nginxOpMu.Lock()
	defer nginxOpMu.Unlock()

	configPath, enabledPath := dockerAppVhostPaths(p.DomainName)

	changed := false
	if _, err := os.Lstat(enabledPath); err == nil {
		if err := os.Remove(enabledPath); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("rm enabled: %v", err)}
		}
		changed = true
	}
	if _, err := os.Stat(configPath); err == nil {
		if err := os.Remove(configPath); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("rm available: %v", err)}
		}
		changed = true
	}

	if changed {
		if err := nginxTestAndReload(ctx); err != nil {
			return nil, err
		}
	}
	return webmailVhostResponse{Ok: true, Changed: changed}, nil
}

func init() {
	Default.Register("docker_app.vhost_apply", dockerAppVhostApplyHandler)
	Default.Register("docker_app.vhost_remove", dockerAppVhostRemoveHandler)
}
