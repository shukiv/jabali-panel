package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// preview.fallback_apply — catch-all vhost for `*.preview.<hostname>`.
//
// Wildcard DNS resolves EVERY <anything>.preview.<hostname> to this box,
// but only enabled domains render a preview server block. Without a
// fallback, a mistyped slug or a preview whose domain was deleted falls
// through to nginx's default vhost (connection drop / bare 444) — which
// reads as "the server is broken" rather than "this preview doesn't
// exist". This block catches the remainder and serves the branded 404
// from /var/www/jabali-errors (converged by the reconciler's error-page
// pass). Pattern borrowed from the operator's hostsclick preview fleet
// (_preview-fallback.conf).
//
// TLS: served with the shared *.preview.<hostname> pair when it exists;
// HTTP-only until then (same degradation as the per-domain preview
// blocks). Specific server_name entries always win over the wildcard,
// so this can never shadow a real preview.

const previewFallbackConfName = "jabali-preview-fallback.conf"

type previewFallbackApplyParams struct {
	// Base is "preview.<hostname>" — the wildcard serves *.<Base>.
	Base           string `json:"base"`
	SSLCertPath    string `json:"ssl_cert_path,omitempty"`
	SSLKeyPath     string `json:"ssl_key_path,omitempty"`
	HTTP2Param     string `json:"-"`
	HTTP2Directive string `json:"-"`
}

type previewFallbackResponse struct {
	Ok      bool   `json:"ok"`
	Path    string `json:"path"`
	Changed bool   `json:"changed"`
}

const previewFallbackTemplate = `# Managed by jabali-agent (preview.fallback_apply) — do not edit.
# Catch-all for *.{{.Base}}: previews that don't exist (typo, disabled
# or deleted domain) get the branded 404 instead of the default-vhost drop.
server {
    listen 80;
    listen [::]:80;
    server_name *.{{.Base}};
{{ if .SSLCertPath }}
    location / {
        return 301 https://$host$request_uri;
    }
}

server {
    listen 443 ssl{{.HTTP2Param}};
    listen [::]:443 ssl{{.HTTP2Param}};
{{ if .HTTP2Directive }}    {{.HTTP2Directive}}
{{ end }}    server_name *.{{.Base}};
    ssl_certificate {{.SSLCertPath}};
    ssl_certificate_key {{.SSLKeyPath}};
    ssl_protocols TLSv1.2 TLSv1.3;
{{ end }}
    add_header X-Robots-Tag "noindex, nofollow" always;
    root /var/www/jabali-errors;
    access_log /var/log/nginx/preview-fallback-access.log;

    location / {
        try_files /jabali-err-404.html =404;
    }
}
`

func previewFallbackApplyHandler(ctx context.Context, params json.RawMessage) (any, error) {
	if len(params) == 0 {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "params required"}
	}
	var p previewFallbackApplyParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	if !domainRegex.MatchString(p.Base) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid base %q", p.Base),
		}
	}
	// Cert paths travel together; a missing file downgrades to HTTP-only
	// exactly like the per-domain preview blocks.
	if p.SSLCertPath != "" {
		if _, err := os.Stat(p.SSLCertPath); err != nil {
			p.SSLCertPath, p.SSLKeyPath = "", ""
		}
	}

	h2 := nginxHTTP2()
	p.HTTP2Param, p.HTTP2Directive = h2.Param, h2.Directive
	tmpl, err := template.New("previewfallback").Parse(previewFallbackTemplate)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("template parse: %v", err)}
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("template execute: %v", err)}
	}

	configPath := filepath.Join("/etc/nginx/sites-available", previewFallbackConfName)
	enabledPath := filepath.Join("/etc/nginx/sites-enabled", previewFallbackConfName)

	nginxOpMu.Lock()
	defer nginxOpMu.Unlock()

	if existing, err := os.ReadFile(configPath); err == nil && bytes.Equal(existing, rendered.Bytes()) {
		if _, err := os.Lstat(enabledPath); os.IsNotExist(err) {
			if err := os.Symlink(configPath, enabledPath); err != nil {
				return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("re-link enabled path: %v", err)}
			}
			if err := nginxTestAndReload(ctx); err != nil {
				return nil, err
			}
			return previewFallbackResponse{Ok: true, Path: configPath, Changed: true}, nil
		}
		return previewFallbackResponse{Ok: true, Path: configPath, Changed: false}, nil
	}

	tmp := configPath + ".tmp"
	if err := os.WriteFile(tmp, rendered.Bytes(), 0o644); err != nil {
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
		os.Remove(enabledPath)
		os.Remove(configPath)
		return nil, err
	}
	return previewFallbackResponse{Ok: true, Path: configPath, Changed: true}, nil
}

func init() {
	Default.Register("preview.fallback_apply", previewFallbackApplyHandler)
}
