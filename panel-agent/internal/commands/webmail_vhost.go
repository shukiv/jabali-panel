package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"text/template"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// webmail_vhost.go provides the two agent commands the reconciler uses
// to toggle the per-domain mail.<domain> nginx vhost:
//
//   - webmail.vhost_apply — render + write + nginx reload
//   - webmail.vhost_remove — rm + nginx reload
//
// The vhost proxies browser traffic to Bulwark (127.0.0.1:3000) for the
// webmail UI and to Stalwart's loopback HTTP listener (127.0.0.1:8446)
// for JMAP + admin API calls. The canonical template lives at
// install/nginx/jabali-mail-vhost.conf.tmpl (committed so operators can
// diff it); this file keeps its own copy for build independence. If the
// two drift, the source-of-truth is the committed template — align by
// re-copying here.

// mailVhostTemplate mirrors install/nginx/jabali-mail-vhost.conf.tmpl.
// Go template variables: .DomainName, .SSLCertPath, .SSLKeyPath,
// .DocRoot, .ListenIPv4, .ListenIPv6.
//
// listen-line shape mirrors domain_create.go: when an explicit IPv4 or
// IPv6 binding is set, we emit `listen <ip>:<port>` so the mail vhost
// joins nginx's specific-IP listener pool and beats the wildcard
// listener for traffic on that IP. Without this, on a host where the
// apex domain vhost binds the public IP explicitly (M24 default), the
// mail subdomain falls into the wildcard pool which nginx then
// IGNORES for that IP — and SNI lands on whichever apex vhost
// happens to be alphabetically first. Result: mail.<domain> serves
// the wrong tenant's cert. Incident 2026-04-26.
const mailVhostTemplate = `# Rendered by panel-agent webmail.vhost_apply (M6 Step 8).
# DO NOT EDIT — changes belong in install/nginx/jabali-mail-vhost.conf.tmpl.

server {
{{ if .ListenIPv4 }}  listen {{.ListenIPv4}}:443 ssl{{.HTTP2Param}};
{{ else }}  listen 443 ssl{{.HTTP2Param}};
{{ end }}{{ if .ListenIPv6 }}  listen [{{.ListenIPv6}}]:443 ssl{{.HTTP2Param}};
{{ else }}  listen [::]:443 ssl{{.HTTP2Param}};
{{ end }}{{ if .HTTP2Directive }}  {{.HTTP2Directive}}
{{ end }}  server_name mail.{{.DomainName}} autoconfig.{{.DomainName}} autodiscover.{{.DomainName}} mta-sts.{{.DomainName}};

  ssl_certificate {{.SSLCertPath}};
  ssl_certificate_key {{.SSLKeyPath}};

{{ if .PanelHostname }}  # Same-origin JMAP rewrite. Bulwark's /api/config returns
  # jmapServerUrl=https://{{.PanelHostname}} (the panel-wide setting in
  # /etc/jabali-panel/bulwark.env), and Stalwart's /.well-known/jmap
  # Session response advertises absolute URLs at the same host. The
  # SPA then issues cross-origin preflights from mail.<tenant>, which
  # fail because the panel hostname's cert / DNS isn't trusted from
  # the user's browser. sub_filter rewrites the hostname to the
  # requested $host so the SPA stays same-origin against the
  # per-domain mail vhost it was loaded from.
  #
  # sub_filter_* inherit into every location below; Accept-Encoding=""
  # is set per-location (nginx fully replaces proxy_set_header when a
  # lower level defines any) so neither Bulwark nor Stalwart can gzip
  # the body — sub_filter silently no-ops on gzipped payloads.
  sub_filter_types application/json;
  sub_filter_once off;
  sub_filter "mail.{{.PanelHostname}}" $host;

{{ end }}  # Intentionally no X-Forwarded-Proto on this location — Next.js
  # middleware-rewrite uses it to build internal proxy URLs, and with
  # "https" would try to TLS-connect to Bulwark's plain-HTTP upstream.
  # See the source-of-truth template in install/nginx/jabali-mail-vhost.conf.tmpl.
  #
  # M25 Step 5: Bulwark on Unix socket via the jabali_bulwark upstream
  # declared in /etc/nginx/sites-available/jabali-bulwark.conf.
  location / {
    proxy_pass http://jabali_bulwark/;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header Accept-Encoding "";
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_read_timeout 3600s;
    proxy_send_timeout 3600s;
  }

  location /jmap {
    proxy_pass http://127.0.0.1:8446;
    proxy_set_header Host $host;
    proxy_set_header Accept-Encoding "";
    proxy_http_version 1.1;
    proxy_read_timeout 3600s;
  }

  location /.well-known/jmap {
    proxy_pass http://127.0.0.1:8446;
    proxy_set_header Host $host;
    proxy_set_header Accept-Encoding "";
  }

  # /api/* intentionally routes to Bulwark (catch-all /) — Bulwark owns
  # its own API endpoints (/api/auth/session, etc.) and proxies
  # Stalwart admin internally via STALWART_API_URL. Routing /api → 8446
  # here would hijack Bulwark's login route.

  # Webmail SSO landing — proxy to panel-api (Unix socket as of M25
  # Step 4). Panel itself serves GET /sso/webmail?token=… and sets the
  # Bulwark session cookie on its response so the 303 lands the user
  # logged in. Reuses the jabali_panel_api upstream declared in
  # /etc/nginx/sites-available/jabali-panel.conf.
  location = /sso/webmail {
    proxy_pass http://jabali_panel_api/sso/webmail;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto https;
    proxy_http_version 1.1;
  }

  # Mail-client autoconfiguration (GH #1039). panel-api renders the config
  # documents from the domain's mail settings (mail.<domain>, IMAPS 993 /
  # SMTPS 465). Host is forwarded so panel-api derives the domain from
  # autoconfig.<domain> / autodiscover.<domain>. These paths used to 404
  # (autodiscover) or fall through location / to Bulwark webmail (autoconfig).
  #
  #   Thunderbird / Mozilla: GET  /mail/config-v1.1.xml
  #                          GET  /.well-known/autoconfig/mail/config-v1.1.xml
  #   Outlook / Exchange:    POST /autodiscover/autodiscover.xml (+ capitalised)
  location = /mail/config-v1.1.xml {
    proxy_pass http://jabali_panel_api;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto https;
    proxy_http_version 1.1;
  }
  location = /.well-known/autoconfig/mail/config-v1.1.xml {
    proxy_pass http://jabali_panel_api/mail/config-v1.1.xml;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto https;
    proxy_http_version 1.1;
  }
  location = /autodiscover/autodiscover.xml {
    proxy_pass http://jabali_panel_api;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto https;
    proxy_http_version 1.1;
  }
  location = /Autodiscover/Autodiscover.xml {
    proxy_pass http://jabali_panel_api;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto https;
    proxy_http_version 1.1;
  }
}

server {
{{ if .ListenIPv4 }}  listen {{.ListenIPv4}}:80;
{{ else }}  listen 80;
{{ end }}{{ if .ListenIPv6 }}  listen [{{.ListenIPv6}}]:80;
{{ else }}  listen [::]:80;
{{ end }}  server_name mail.{{.DomainName}} autoconfig.{{.DomainName}} autodiscover.{{.DomainName}} mta-sts.{{.DomainName}};

  # ACME HTTP-01 webroot. Must be a location block — a server-level
  # redirect fires in nginx SERVER_REWRITE phase BEFORE FIND_CONFIG,
  # so a server-scoped redirect bypasses ^~ matching for ACME paths.
  # Webroot mirrors what panel-api/internal/reconciler.IssueDomainCert
  # passes as -w (the apex domain DocRoot). Incident 2026-04-26:
  # jabali.site cert failed because mail.<domain>:80 redirected to
  # https where the mail vhost served 404 for /.well-known/acme.
  #
  # Panel-primary rows (M6.4 / ADR-0048) are mail-only — no docroot.
  # The panel-cert reconciler (ssl.panel.issue kind=mail) owns
  # mail.<panel-hostname> cert renewal and validates HTTP-01 with
  # -w /var/www/jabali-panel-acme, so the empty-DocRoot branch below
  # serves THAT webroot (it must not fall through to the 301, or the
  # challenge redirects to https and 404s — GH #268). It omits the
  # {{.DocRoot}} fallback (no docroot) to avoid a bare root directive.
  #
  # When DocRoot IS set (tenant domain) the location serves TWO
  # webroots in priority order:
  #   1. /var/www/jabali-acme — the shared panel ACME webroot used by
  #      the M6.6 per-domain mail cert (ssl.mail.issue passes -w
  #      /var/www/jabali-acme) and the M32 panel cert.
  #   2. {{.DocRoot}} — the apex domain docroot used by the regular
  #      per-domain cert (reconciler IssueDomainCert passes -w
  #      DocRoot). Reached via the @acme_docroot fallback.
  # Both certs cover mail.<domain> as a SAN and both validate over
  # mail.<domain>:80, but they drop their challenge token into
  # DIFFERENT directories — without serving both, whichever path
  # didn't write here 404s. Incident GH #132: the M6.6 mail cert
  # 404'd because this location only served the docroot, leaving
  # Stalwart on its self-signed default on :465.
{{ if .DocRoot }}  location ^~ /.well-known/acme-challenge/ {
    default_type "text/plain";
    root /var/www/jabali-acme;
    try_files $uri @acme_docroot;
  }

  location @acme_docroot {
    default_type "text/plain";
    root {{.DocRoot}};
    try_files $uri =404;
  }

  location / {
    return 301 https://$host$request_uri;
  }
{{ else }}  # Panel-primary mail vhost (M6.4 / ADR-0048): mail-only, no per-domain
  # docroot. The panel mail cert (ssl.panel.issue kind=mail) validates
  # mail.<panel-hostname> over HTTP-01 with -w /var/www/jabali-panel-acme,
  # so this :80 block MUST serve that webroot — otherwise the challenge
  # falls through to the 301 below, redirects to https, and 404s on the
  # mail vhost, parking the panel mail cert in failed/attempt-N (GH #268).
  location ^~ /.well-known/acme-challenge/ {
    default_type "text/plain";
    root /var/www/jabali-panel-acme;
    try_files $uri =404;
  }

  location / {
    return 301 https://$host$request_uri;
  }
{{ end }}}
`

// mailVhostSitesAvailable + mailVhostSitesEnabled are overridable for
// tests. The naming `<domain>-mail.conf` avoids colliding with the main
// `<domain>.conf` that domain.create writes.
var (
	mailVhostSitesAvailable = "/etc/nginx/sites-available"
	mailVhostSitesEnabled   = "/etc/nginx/sites-enabled"
)

type webmailVhostApplyParams struct {
	DomainName  string `json:"domain_name"`
	SSLCertPath string `json:"ssl_cert_path"`
	SSLKeyPath  string `json:"ssl_key_path"`
	// DocRoot is the apex domain document root, used as the ACME
	// HTTP-01 webroot in the :80 server block. Mirrors what panel-api
	// passes to ssl.issue (-w) so the renewal challenge file lands at
	// the path nginx will actually serve.
	DocRoot string `json:"doc_root"`
	// ListenIPv4 / ListenIPv6 mirror the apex vhost's listener
	// binding (M24). Empty falls back to wildcard. Required to keep
	// SNI deterministic when the apex vhost has a specific-IP listen.
	ListenIPv4 string `json:"listen_ipv4,omitempty"`
	ListenIPv6 string `json:"listen_ipv6,omitempty"`
	// HTTP/2 form per host nginx version (GH #292); set before render.
	HTTP2Param     string `json:"-"`
	HTTP2Directive string `json:"-"`
	// PanelHostname is server_settings.hostname (e.g. mx.jabali-panel.com).
	// When set, the rendered vhost installs an nginx sub_filter that
	// rewrites the panel hostname to the requested $host in Bulwark
	// /api/config and Stalwart /.well-known/jmap response bodies, so
	// the SPA stays same-origin on mail.<tenant-domain>. Empty value
	// disables the rewrite (fresh install / pre-bootstrap).
	PanelHostname string `json:"panel_hostname,omitempty"`
	// IsPanelPrimary marks the panel-hostname mail vhost (M6.4 /
	// ADR-0048 / DomainName == PanelHostname). For that one row the
	// server_name list ALSO includes the bare panel hostname — Bulwark's
	// /api/auth/impersonate proxies upstream JMAP fetches to
	// `https://<panel-hostname>/jmap`, and without bare hostname in
	// server_name those requests fall to nginx default + return 500.
	// M6.6 / 2026-06-01 live-smoke regression.
	IsPanelPrimary bool `json:"is_panel_primary,omitempty"`
}

type webmailVhostResponse struct {
	Ok      bool   `json:"ok"`
	Path    string `json:"path,omitempty"`
	Changed bool   `json:"changed"`
}

func webmailVhostApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	if len(params) == 0 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "params required"}
	}
	var p webmailVhostApplyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if err := validateDomainNameForShell(p.DomainName); err != nil {
		return nil, err
	}
	if p.SSLCertPath == "" || p.SSLKeyPath == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "ssl_cert_path and ssl_key_path are required",
		}
	}

	h2 := nginxHTTP2()
	p.HTTP2Param, p.HTTP2Directive = h2.Param, h2.Directive
	tmpl, err := template.New("mailvhost").Parse(mailVhostTemplate)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("template parse: %v", err)}
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("template execute: %v", err)}
	}

	configPath := filepath.Join(mailVhostSitesAvailable, p.DomainName+"-mail.conf")
	enabledPath := filepath.Join(mailVhostSitesEnabled, p.DomainName+"-mail.conf")

	// JAB-71: serialize the whole write→test→reload (both the fast-path relink
	// branch and the full write) against every other nginx op.
	nginxOpMu.Lock()
	defer nginxOpMu.Unlock()

	// Hash-gated write: if the on-disk content matches what we'd render,
	// skip the write + reload. Matches writeVhost's idempotency contract.
	if existing, err := os.ReadFile(configPath); err == nil && bytes.Equal(existing, rendered.Bytes()) {
		// Ensure the symlink exists even when the content hasn't changed —
		// a prior vhost_remove may have torn it down.
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

	// Atomic write: tmp then rename. If rename fails we leave the tmp file
	// so a subsequent run can diagnose (and the idempotency check above
	// will re-try on next pass).
	tmp := configPath + ".tmp"
	if err := os.WriteFile(tmp, rendered.Bytes(), 0644); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("write tmp vhost: %v", err)}
	}
	if err := os.Rename(tmp, configPath); err != nil {
		os.Remove(tmp)
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("rename vhost: %v", err)}
	}

	// (Re-)link into sites-enabled. Re-link unconditionally so a dangling
	// symlink from an earlier broken state is repaired.
	os.Remove(enabledPath)
	if err := os.Symlink(configPath, enabledPath); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("symlink enabled: %v", err)}
	}

	if err := nginxTestAndReload(ctx); err != nil {
		// Roll back the vhost: nginx is currently happy because the bad
		// file isn't loaded yet (we haven't reloaded after the rename).
		// But leaving a bad file in sites-enabled would break the NEXT
		// reload by anyone else. Safer to remove both and surface.
		os.Remove(enabledPath)
		os.Remove(configPath)
		return nil, err
	}
	return webmailVhostResponse{Ok: true, Path: configPath, Changed: true}, nil
}

type webmailVhostRemoveParams struct {
	DomainName string `json:"domain_name"`
}

func webmailVhostRemoveHandler(ctx context.Context, params json.RawMessage) (any, error) {
	if len(params) == 0 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "params required"}
	}
	var p webmailVhostRemoveParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if err := validateDomainNameForShell(p.DomainName); err != nil {
		return nil, err
	}

	configPath := filepath.Join(mailVhostSitesAvailable, p.DomainName+"-mail.conf")
	enabledPath := filepath.Join(mailVhostSitesEnabled, p.DomainName+"-mail.conf")

	// JAB-71: serialize remove→test→reload against every other nginx op.
	nginxOpMu.Lock()
	defer nginxOpMu.Unlock()

	// Idempotent remove. If neither file exists we still return ok+changed=false
	// so the reconciler can cheaply ask us on every tick.
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

// nginxTestAndReload is the tiny shim shared by apply + remove: test
// first, reload only on a clean test. Overridable for tests so unit
// coverage doesn't need a real nginx binary on the host.
var nginxTestAndReload = defaultNginxTestAndReload

func defaultNginxTestAndReload(ctx context.Context) error {
	var out bytes.Buffer
	test := exec.CommandContext(ctx, "nginx", "-t")
	test.Stdout = &out
	test.Stderr = &out
	if err := test.Run(); err != nil {
		return nginxTestFailure("webmail.vhost", out.String())
	}
	out.Reset()
	reload := exec.CommandContext(ctx, "systemctl", "reload", "nginx")
	reload.Stdout = &out
	reload.Stderr = &out
	if err := reload.Run(); err != nil {
		return &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("nginx reload failed: %s", out.String()),
		}
	}
	return nil
}

func init() {
	Default.Register("webmail.vhost_apply", webmailVhostApplyHandler)
	Default.Register("webmail.vhost_remove", webmailVhostRemoveHandler)
}
