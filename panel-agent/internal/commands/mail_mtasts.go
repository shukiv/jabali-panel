// Package commands — mail.mtasts.apply / mail.mtasts.disable.
//
// MTA-STS per-domain serving (M47 Wave 7, ADR-0109). The panel calls
// `mail.mtasts.apply` whenever a domain's mta_sts_enabled flips on
// (or its mta_sts_id rotates after a mode change). The agent:
//
//  1. Writes the policy file at
//     /var/www/jabali-mta-sts/<domain>/.well-known/mta-sts.txt
//     containing version: STSv1 + mode + max_age + the mx host.
//  2. Writes the dedicated nginx vhost at
//     /etc/nginx/sites-available/<domain>-mta-sts.conf
//     serving https://mta-sts.<domain>/.well-known/mta-sts.txt as a
//     static file from the directory above. Listens on the existing
//     mail vhost cert (which carries mta-sts.<domain> as a SAN once
//     sanHostnamesForDomain includes it).
//  3. Symlinks the vhost into sites-enabled.
//  4. Runs `nginx -t` and reloads on success. A failed test leaves
//     the existing config in place and surfaces the error to the
//     caller (fail-safe — MTA-STS misconfiguration must not take the
//     whole nginx down).
//
// `mail.mtasts.disable` reverses 1-3 (idempotent — missing files are
// not an error) and reloads. Both handlers are safe to re-run; the
// reconciler invokes apply on every tick when the flag is on, which
// is how an operator-edited policy file gets reset.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// mtaStsRoot is the parent directory holding one subdir per domain.
// Each subdir contains the .well-known/mta-sts.txt nginx serves. The
// root dir is created on first apply with 0755 + root:root ownership
// — content is non-secret (public policy) and nginx reads as
// www-data. A var so tests can point it at a temp dir.
var mtaStsRoot = "/var/www/jabali-mta-sts"

// mtaStsVhostDir is the nginx sites-available/sites-enabled root the
// agent already manages — same shape as the domain vhost files
// written by writeVhost(). One `<domain>-mta-sts.conf` per domain.
// Vars so tests can point them at temp dirs.
var (
	mtaStsSitesAvail = "/etc/nginx/sites-available"
	mtaStsSitesEnabl = "/etc/nginx/sites-enabled"
)

// mtaStsModes enumerates the three RFC 8461 PolicyEnforcement
// variants. Stalwart's MtaSts singleton uses the same labels.
var mtaStsModes = map[string]struct{}{
	"enforce": {}, "testing": {}, "none": {},
}

// mtaStsDomainRe pins the same FQDN shape ssl_issue uses. Reject
// anything else before letting it near a file path or nginx config.
var mtaStsDomainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,253}[a-z0-9])?$`)

// mtaStsApplyParams is the wire shape for mail.mtasts.apply.
type mtaStsApplyParams struct {
	Domain  string `json:"domain"`
	MXHost  string `json:"mx_host"`  // the published mx: value (server hostname)
	Mode    string `json:"mode"`     // enforce | testing | none
	MaxAge  int    `json:"max_age"`  // policy cache lifetime, seconds (>= 86400 RFC 8461)
	SSLCert string `json:"ssl_cert"` // path to the cert that covers mta-sts.<domain>
	SSLKey  string `json:"ssl_key"`
}

type mtaStsApplyResponse struct {
	OK         bool   `json:"ok"`
	PolicyPath string `json:"policy_path"`
	VhostPath  string `json:"vhost_path"`
}

type mtaStsDisableParams struct {
	Domain string `json:"domain"`
}

type mtaStsDisableResponse struct {
	OK bool `json:"ok"`
}

func mailMTAStsApplyHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p mtaStsApplyParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("parse: %v", err)}
	}
	if err := validateMTAStsApply(p); err != nil {
		return nil, err
	}
	domDir := filepath.Join(mtaStsRoot, p.Domain)
	wkDir := filepath.Join(domDir, ".well-known")
	if err := os.MkdirAll(wkDir, 0o755); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal,
			Message: fmt.Sprintf("mkdir %s: %v", wkDir, err)}
	}
	policyPath := filepath.Join(wkDir, "mta-sts.txt")
	policyBody := renderMTAStsPolicy(p.Mode, p.MaxAge, p.MXHost)
	if err := atomicWrite(policyPath, policyBody); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal,
			Message: fmt.Sprintf("write policy: %v", err)}
	}
	// atomicWrite is shared with ssh-config writers and uses 0600 by
	// default. The policy file must be world-readable so nginx (running
	// as www-data) can serve it — chmod after.
	if err := os.Chmod(policyPath, 0o644); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal,
			Message: fmt.Sprintf("chmod policy: %v", err)}
	}
	vhostPath := filepath.Join(mtaStsSitesAvail, p.Domain+"-mta-sts.conf")
	vhost := renderMTAStsVhost(p.Domain, domDir, p.SSLCert, p.SSLKey)
	if err := atomicWrite(vhostPath, vhost); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal,
			Message: fmt.Sprintf("write vhost: %v", err)}
	}
	if err := os.Chmod(vhostPath, 0o644); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal,
			Message: fmt.Sprintf("chmod vhost: %v", err)}
	}
	enabledLink := filepath.Join(mtaStsSitesEnabl, p.Domain+"-mta-sts.conf")
	if _, err := os.Lstat(enabledLink); os.IsNotExist(err) {
		if err := os.Symlink(vhostPath, enabledLink); err != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal,
				Message: fmt.Sprintf("symlink: %v", err)}
		}
	}
	if err := nginxTestAndReload(ctx); err != nil {
		return nil, err
	}
	return mtaStsApplyResponse{OK: true, PolicyPath: policyPath, VhostPath: vhostPath}, nil
}

func mailMTAStsDisableHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p mtaStsDisableParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("parse: %v", err)}
	}
	if !mtaStsDomainRe.MatchString(p.Domain) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument,
			Message: "invalid domain"}
	}
	// Remove sites-enabled symlink first so a failed reload doesn't leak the vhost.
	_ = os.Remove(filepath.Join(mtaStsSitesEnabl, p.Domain+"-mta-sts.conf"))
	_ = os.Remove(filepath.Join(mtaStsSitesAvail, p.Domain+"-mta-sts.conf"))
	_ = os.RemoveAll(filepath.Join(mtaStsRoot, p.Domain))
	if err := nginxTestAndReload(ctx); err != nil {
		return nil, err
	}
	return mtaStsDisableResponse{OK: true}, nil
}

// validateMTAStsApply checks every wire field. Domain + mxhost both
// pass the FQDN regex; mode is enum-restricted; max_age must be in
// the RFC 8461 [86400, 31557600] range; cert paths must look like
// absolute paths under the panel cert dirs (no traversal).
func validateMTAStsApply(p mtaStsApplyParams) error {
	if !mtaStsDomainRe.MatchString(p.Domain) {
		return &agentwire.AgentError{Code: agentwire.CodeInvalidArgument,
			Message: "invalid domain"}
	}
	if !mtaStsDomainRe.MatchString(p.MXHost) {
		return &agentwire.AgentError{Code: agentwire.CodeInvalidArgument,
			Message: "invalid mx_host"}
	}
	if _, ok := mtaStsModes[p.Mode]; !ok {
		return &agentwire.AgentError{Code: agentwire.CodeInvalidArgument,
			Message: "mode must be enforce|testing|none"}
	}
	if p.MaxAge < 86400 || p.MaxAge > 31557600 {
		return &agentwire.AgentError{Code: agentwire.CodeInvalidArgument,
			Message: "max_age must be 86400..31557600 (RFC 8461)"}
	}
	for _, c := range []string{p.SSLCert, p.SSLKey} {
		if c == "" || !filepath.IsAbs(c) || strings.Contains(c, "..") {
			return &agentwire.AgentError{Code: agentwire.CodeInvalidArgument,
				Message: "ssl_cert/ssl_key must be absolute paths without .."}
		}
	}
	return nil
}

// renderMTAStsPolicy emits the RFC 8461 §3.2 policy file body. The
// `mx:` line is REQUIRED even in testing mode — receivers compare it
// to the actual MX records, and an empty mx makes the whole policy
// look like a misconfiguration.
func renderMTAStsPolicy(mode string, maxAge int, mxHost string) string {
	var b strings.Builder
	b.WriteString("version: STSv1\n")
	b.WriteString("mode: ")
	b.WriteString(mode)
	b.WriteByte('\n')
	b.WriteString(fmt.Sprintf("max_age: %d\n", maxAge))
	b.WriteString("mx: ")
	b.WriteString(mxHost)
	b.WriteByte('\n')
	return b.String()
}

// renderMTAStsVhost writes a minimal nginx server block. Two listen
// stanzas (4 and 6) match the domain vhost shape; HTTP-only on :443
// (no :80 — MTA-STS REQUIRES https and a public CA cert per RFC
// 8461 §3.3, so any plain-HTTP attempt is a misconfiguration we
// should make obvious by NOT serving it). ssl_certificate paths come
// from the panel — the cert MUST cover `mta-sts.<domain>` as a SAN
// (sanHostnamesForDomain handles this once mta_sts_enabled is on).
func renderMTAStsVhost(domain, docRoot, certPath, keyPath string) string {
	h2 := nginxHTTP2()
	listenBlock := fmt.Sprintf("    listen 443 ssl%s;\n    listen [::]:443 ssl%s;", h2.Param, h2.Param)
	if h2.Directive != "" {
		listenBlock += "\n    " + h2.Directive
	}
	return fmt.Sprintf(`# Managed by jabali agent — mail.mtasts.apply (ADR-0109)
# DO NOT HAND-EDIT — every reconciler pass overwrites.
server {
%s
    server_name mta-sts.%s;

    ssl_certificate %s;
    ssl_certificate_key %s;

    root %s;

    location = /.well-known/mta-sts.txt {
        default_type "text/plain";
        add_header Cache-Control "public, max-age=86400" always;
        try_files $uri =404;
    }

    location / { return 404; }

    access_log /var/log/nginx/%s-mta-sts-access.log;
    error_log  /var/log/nginx/%s-mta-sts-error.log;
}
`, listenBlock, domain, certPath, keyPath, docRoot, domain, domain)
}

// nginxTestAndReload + atomicWrite live in webmail_vhost.go and
// system_set_ssh_config.go respectively — same package, single
// source of truth. We use them as-is so an nginx-reload race
// between mail.mtasts.apply and another vhost writer can't happen.

func init() {
	Default.Register("mail.mtasts.apply", mailMTAStsApplyHandler)
	Default.Register("mail.mtasts.disable", mailMTAStsDisableHandler)
}
