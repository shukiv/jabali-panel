package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"text/template"

	"git.linux-hosting.co.il/shukivaknin/jabali2/agentwire"
)

// domainCreateParams is the input shape for domain.create.
type domainCreateParams struct {
	Username           string `json:"username"`
	Domain             string `json:"domain"`
	DocRoot            string `json:"doc_root"`
	HasPHP             bool   `json:"has_php"`
	PHPVersion         string `json:"php_version"`
	CustomDirectives   string `json:"custom_directives"`
	RedirectDirectives string `json:"redirect_directives"`
	RuleDirectives     string `json:"rule_directives"`
	IndexPriority      string `json:"index_priority"`
	// IsEnabled controls whether the vhost serves the tenant's docroot
	// (true) or a branded "site disabled" placeholder (false). Pointer
	// so omitted fields default to true (backwards compat).
	IsEnabled    *bool  `json:"is_enabled"`
	// CacheEnabled (ADR-0108) — per-domain nginx FastCGI micro-cache
	// opt-in. false ⇒ vhost byte-identical to the pre-0108 shape.
	CacheEnabled bool   `json:"cache_enabled"`
	SSLCertPath  string `json:"ssl_cert_path"`
	SSLKeyPath   string `json:"ssl_key_path"`
	// PHP INI overrides: omitted if not set on the domain.
	PHPMemoryLimit       string `json:"php_memory_limit,omitempty"`
	PHPUploadMaxFilesize string `json:"php_upload_max_filesize,omitempty"`
	PHPPostMaxSize       string `json:"php_post_max_size,omitempty"`
	PHPMaxInputVars      int    `json:"php_max_input_vars,omitempty"`
	PHPMaxExecutionTime  int    `json:"php_max_execution_time,omitempty"`
	PHPMaxInputTime      int    `json:"php_max_input_time,omitempty"`
	// M18 per-domain HTTP limits. DomainID is required when either
	// RateLimitRPS or ConnectionLimit is set so the zone-name tag
	// matches the declaration in 00-jabali-ratelimits.conf. All three
	// default to zero/empty which produces no directives (backwards
	// compatible with existing domain.create callers).
	DomainID        string `json:"domain_id,omitempty"`
	RateLimitRPS    uint32 `json:"rate_limit_rps,omitempty"`
	ConnectionLimit uint32 `json:"connection_limit,omitempty"`
	// M24 per-domain listen IPs. Empty string ⇒ "all interfaces" (the
	// pre-M24 listen 80 / listen [::]:80 behaviour). When set, the
	// renderer emits explicit listen <ipv4>:80; / listen [<ipv6>]:80;
	// directives. The vhost template guards each line with an explicit
	// if/else so an unset IPv6 still renders `listen [::]:80;` rather
	// than the invalid `listen []:80;`.
	ListenIPv4 string `json:"listen_ipv4,omitempty"`
	ListenIPv6 string `json:"listen_ipv6,omitempty"`

	// M28 — operator-editable default index body. Empty string falls
	// back to defaultIndexTemplate below. Go text/template syntax with
	// {{.Domain}}, {{.Username}}, {{.DocRoot}} placeholders.
	DefaultIndexTemplate string `json:"default_index_template,omitempty"`

	// M36 per-domain IP allow/deny rules. Already sorted by the panel
	// (priority ASC, created_at ASC). Each rule renders as one nginx
	// `allow <cidr>;` / `deny <cidr>;` directive in priority order
	// inside the server block.
	IPACLs []domainIPACLRule `json:"ip_acls,omitempty"`

	// M50 per-directory password protection. Each rule produces one
	// htpasswd file under /etc/jabali-panel/dir-privacy/<rule_id>.htpasswd
	// and one `location ^~ <path>/ { auth_basic ...; }` block inside the
	// vhost. DirectoryPrivacyRuleIDs is the full set of rule IDs panel
	// knows about for this domain — the agent uses it to detect orphaned
	// htpasswd files (rule deleted on panel, file still on disk).
	DirectoryPrivacyRules   []directoryPrivacyRule `json:"directory_privacy_rules,omitempty"`
	DirectoryPrivacyRuleIDs []string               `json:"directory_privacy_rule_ids,omitempty"`
}

// domainIPACLRule is one allow/deny entry. CIDR is canonicalised by
// the panel (bare IP → /32 or /128). Action is "allow" or "deny" —
// validated at the API boundary; agent rejects anything else as a
// defensive measure.
type domainIPACLRule struct {
	CIDR   string `json:"cidr"`
	Action string `json:"action"`
}

// domainCreateResponse is the output shape for domain.create.
type domainCreateResponse struct {
	Domain     string `json:"domain"`
	DocRoot    string `json:"doc_root"`
	ConfigPath string `json:"config_path"`
}

// domainRegex validates domain name format (lowercase hostname).
// Pattern: ^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$
var domainRegex = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// vhostTemplate is the nginx vhost configuration template.
//
// Listen-IP rendering note (M24): each `listen` line uses an explicit
// `{{if .X}}...{{else}}...{{end}}`. The naive `listen {{if .X}}{{.X}}:{{end}}80;`
// shape renders the invalid `listen :80;` when X is empty, so we keep
// the bracketed [::] / bare 80 fallbacks separately. See plans/m24-ip-manager.md
// review F-H-3.
const vhostTemplate = `server {
{{ if .ListenIPv4 }}    listen {{.ListenIPv4}}:80;
{{ else }}    listen 80;
{{ end }}{{ if .ListenIPv6 }}    listen [{{.ListenIPv6}}]:80;
{{ else }}    listen [::]:80;
{{ end }}    server_name {{.Domain}} www.{{.Domain}};
{{ if .IsEnabled }}
    # ACME HTTP-01 webroot. Must be a location block — a server-level
    # redirect fires in nginx SERVER_REWRITE phase BEFORE FIND_CONFIG,
    # so a server-scoped redirect short-circuits every request
    # including /.well-known/acme-challenge/, and Let's Encrypt's
    # challenge fetch bounces to https where LE refuses to follow.
    # Scoping the redirect to a location block instead pushes it into
    # the per-location REWRITE phase, after FIND_CONFIG has had a
    # chance to pick the ^~ ACME match. Webroot mirrors
    # panel-api/internal/reconciler.IssueDomainCert (-w domain.DocRoot).
    # Incident 2026-04-26: jabali.site stuck in pending_acme_retry on
    # first VPS install — first under the no-ACME-location vhost, then
    # again after we added the location but kept the server-scoped
    # redirect that won the rewrite race.
    location ^~ /.well-known/acme-challenge/ {
        default_type "text/plain";
        root {{.DocRoot}};
        try_files $uri =404;
    }
{{ end }}
{{ if .SSLCertPath }}
    # Redirect HTTP to HTTPS when SSL is configured. Scoped to the
    # default location so the ^~ ACME location above wins for
    # challenge paths — see the rationale comment there.
    location / {
        return 301 https://$host$request_uri;
    }
}

server {
{{ if .ListenIPv4 }}    listen {{.ListenIPv4}}:443 ssl http2;
{{ else }}    listen 443 ssl http2;
{{ end }}{{ if .ListenIPv6 }}    listen [{{.ListenIPv6}}]:443 ssl http2;
{{ else }}    listen [::]:443 ssl http2;
{{ end }}    # http2 folded into the listen directive — legacy form
    # works on nginx >= 1.9.5. The separate http2-on directive (1.25+)
    # caused transient emerg + cascade during the in-place vhost
    # migrate on mixed hosts; reverted 2026-05-16.
    server_name {{.Domain}} www.{{.Domain}};
    ssl_certificate {{.SSLCertPath}};
    ssl_certificate_key {{.SSLKeyPath}};
{{ end }}
{{ if .IsEnabled }}
    root {{.DocRoot}};
    {{.IndexDirective}}

    {{.IPACLDirectives}}
    {{.DirectoryPrivacyDirectives}}

    location / {
{{ if .HasPHP }}
        try_files $uri $uri/ /index.php?$query_string;
{{ else }}
        try_files $uri $uri/ =404;
{{ end }}
    }
{{ if .CacheEnabled }}
    # Long-cache static assets (immutable content; 30 days).
    location ~* \.(?:css|js|jpe?g|png|gif|ico|svg|webp|woff2?|ttf|eot)$ {
        expires 30d;
        add_header Cache-Control "public, immutable";
        access_log off;
    }
    # Short-cache static HTML (300s / 5 min). Long enough to satisfy
    # third-party freshness audits, short enough that edits surface
    # within minutes; ETag + Last-Modified still give instant 304
    # revalidation on unchanged content. PHP responses use the
    # separate FastCGI micro-cache (60s) below, not this block.
    # Without this, a domain with cache_enabled=true but a static
    # site (no PHP) responds with no Cache-Control header at all —
    # third-party caching audits (e.g. requestmetrics.com) flag
    # "no Cache-Control" + "heuristic freshness" warnings even
    # though caching IS enabled on the domain. PHP responses still
    # get the server-side FastCGI cache via the location ~ \.php$
    # block below (with X-Jabali-Cache header). Plain HTML hits
    # only this; it does not enable server-side caching.
    location ~* \.html?$ {
        expires 5m;
        add_header Cache-Control "public, max-age=300" always;
    }
{{ end }}
{{ if .HasPHP }}
    location ~ \.php$ {
        fastcgi_pass unix:/run/php/jabali-{{.Username}}/fpm.sock;
        fastcgi_param SCRIPT_FILENAME $realpath_root$fastcgi_script_name;
        include fastcgi_params;
{{ if .PHPValueParam }}
        fastcgi_param PHP_VALUE "{{.PHPValueParam}}";
{{ end }}
{{ if .CacheEnabled }}
        set $jabali_skip 0;
        if ($request_method = POST) { set $jabali_skip 1; }
        if ($query_string != "") { set $jabali_skip 1; }
        if ($request_uri ~* "/wp-admin/|/wp-login|/xmlrpc\.php|/wp-cron\.php|/cart|/checkout|/my-account|/wc-api/|/edd-api/") { set $jabali_skip 1; }
        if ($http_cookie ~* "comment_author|wordpress_[a-f0-9]+|wp-postpass|wordpress_logged_in|woocommerce_items_in_cart|woocommerce_cart_hash|edd_items_in_cart|PHPSESSID") { set $jabali_skip 1; }
        fastcgi_cache {{.CacheKeyZone}};
        fastcgi_cache_key "$scheme$request_method$host$request_uri";
        fastcgi_cache_valid 200 301 302 {{.CacheTTL}};
        fastcgi_cache_bypass $jabali_skip;
        fastcgi_no_cache $jabali_skip;
        fastcgi_cache_use_stale error timeout updating http_500 http_503;
        fastcgi_cache_lock on;
        add_header X-Jabali-Cache $upstream_cache_status always;
{{ end }}
    }
{{ end }}

    access_log /var/log/nginx/{{.Domain}}-access.log;
    error_log /var/log/nginx/{{.Domain}}-error.log;

    # Per-install rewrites (e.g. Drupal/Joomla subdir pretty-URL routing).
    # Written by the per-CMS install handler; reloaded on install/delete.
    # nginx tolerates a missing directory here — no file = no include.
    include /etc/nginx/jabali/{{.Domain}}/*.conf;

    {{.RedirectDirectives}}
    {{.RuleDirectives}}
    {{.CustomDirectives}}
    {{.RateLimitDirectives}}
{{ else }}
    # Domain is administratively disabled. Serve the branded
    # disabled page instead of the tenant's docroot. Keep access
    # logs under the normal filename so ops can still see hits.
    root /var/www/jabali-disabled;
    index index.html;
    access_log /var/log/nginx/{{.Domain}}-access.log;
    error_log /var/log/nginx/{{.Domain}}-error.log;
    location / { try_files /index.html =503; }
{{ end }}
}`

type vhostData struct {
	Domain               string
	DocRoot              string
	HasPHP               bool
	PHPVersion           string
	Username             string
	IndexDirective       string
	RedirectDirectives   string
	RuleDirectives       string
	CustomDirectives     string
	IsEnabled            bool
	SSLCertPath          string
	SSLKeyPath           string
	PHPMemoryLimit       string
	PHPUploadMaxFilesize string
	PHPPostMaxSize       string
	PHPMaxInputVars      int
	PHPMaxExecutionTime  int
	PHPMaxInputTime      int
	PHPValueParam        string // fastcgi_param PHP_VALUE directive content
	// RateLimitDirectives is the fully-rendered per-vhost rate/conn
	// limit block (may span 0–2 lines). Computed by the caller via
	// BuildRateLimitDirectives; interpolated verbatim. Never contains
	// user-controllable data — both the rps value and the zone name
	// (which embeds the ULID) are panel-controlled.
	RateLimitDirectives  string
	// M24 listen IPs. Empty string = all-interfaces fallback handled by
	// the template's explicit if/else. Plain string (validated by
	// panel-api before reaching the agent) so the template stays simple.
	ListenIPv4 string
	ListenIPv6 string
	// M36 IP allow/deny — pre-rendered nginx directives. One line per
	// rule, "allow <cidr>;" or "deny <cidr>;", in priority order. Empty
	// string when the domain has no ACLs (zero overhead). Sits inside
	// the server block so it applies to every location.
	IPACLDirectives string
	// M50 directory privacy — pre-rendered `location ^~ <path>/ {
	// auth_basic ...; auth_basic_user_file ...; }` blocks, one per
	// rule. Empty string when the domain has no privacy rules.
	DirectoryPrivacyDirectives string
	// ADR-0108 FastCGI micro-cache. All three are panel/agent-controlled
	// (never user data). When CacheEnabled is false the template emits
	// none of the cache/static directives → byte-identical to pre-0108.
	CacheEnabled bool
	CacheKeyZone string
	CacheTTL     string
}

// indexDirectiveFor maps the panel's index_priority enum to the concrete
// nginx `index ...;` directive. Unknown values fall back to the prior
// hardcoded behaviour so a mis-plumbed reconciler can't silently break
// a tenant's site.
func indexDirectiveFor(priority string) string {
	switch priority {
	case "php_first":
		return "index index.php index.html;"
	case "html_only":
		return "index index.html;"
	case "php_only":
		return "index index.php;"
	case "full":
		return "index index.php index.html index.htm;"
	case "html_first", "":
		fallthrough
	default:
		return "index index.html index.php;"
	}
}

// pathsUnderHome returns each directory from docRoot up to but NOT
// including /home/<user>, ordered from deepest to shallowest. Used to
// chown every intermediate directory the panel creates so tenant data
// never gets stranded under root ownership.
//
// Returns nil if docRoot doesn't live under the expected home dir —
// the caller should already have validated this upstream.
func pathsUnderHome(username, docRoot string) []string {
	homeDir := "/home/" + username
	if !strings.HasPrefix(docRoot, homeDir+"/") {
		return nil
	}
	var paths []string
	cur := docRoot
	for cur != homeDir && cur != "/" && cur != "." {
		paths = append(paths, cur)
		cur = filepath.Dir(cur)
	}
	return paths
}

// buildPHPValueParam assembles a fastcgi_param PHP_VALUE directive from per-domain INI overrides.
// Returns empty string if all overrides are empty/zero. Format: key1=val1\nkey2=val2\n
func buildPHPValueParam(memLimit, uploadMax, postMax string, maxInputVars, maxExecTime, maxInputTime int) string {
	var parts []string
	if memLimit != "" {
		parts = append(parts, "memory_limit="+memLimit)
	}
	if uploadMax != "" {
		parts = append(parts, "upload_max_filesize="+uploadMax)
	}
	if postMax != "" {
		parts = append(parts, "post_max_size="+postMax)
	}
	if maxInputVars > 0 {
		parts = append(parts, "max_input_vars="+strconv.Itoa(maxInputVars))
	}
	if maxExecTime > 0 {
		parts = append(parts, "max_execution_time="+strconv.Itoa(maxExecTime))
	}
	if maxInputTime > 0 {
		parts = append(parts, "max_input_time="+strconv.Itoa(maxInputTime))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n")
}

// writeVhost generates and writes the nginx vhost configuration, then tests and reloads nginx.
// This is the core logic shared by domain.create and domain.enable/disable.
// If the config content is unchanged, nginx reload is skipped for efficiency.
func writeVhost(ctx context.Context, username, domain, docRoot, phpVersion, redirectDirectives, ruleDirectives, customDirectives, rateLimitDirectives, ipACLDirectives, dirPrivacyDirectives, indexPriority string, isEnabled, hasPHP bool, sslCertPath, sslKeyPath, phpMemLimit, phpUploadMax, phpPostMax string, phpMaxInputVars, phpMaxExecTime, phpMaxInputTime int, listenIPv4, listenIPv6 string, cacheEnabled bool) (string, error) {
	// Generate vhost configuration
	tmpl, err := template.New("vhost").Parse(vhostTemplate)
	if err != nil {
		return "", fmt.Errorf("template parse failed: %w", err)
	}

	vhostData := vhostData{
		Domain:             domain,
		DocRoot:            docRoot,
		HasPHP:             hasPHP,
		PHPVersion:         phpVersion,
		Username:           username,
		IndexDirective:     indexDirectiveFor(indexPriority),
		RedirectDirectives: redirectDirectives,
		RuleDirectives:     ruleDirectives,
		CustomDirectives:   customDirectives,
		RateLimitDirectives: rateLimitDirectives,
		IPACLDirectives:    ipACLDirectives,
		DirectoryPrivacyDirectives: dirPrivacyDirectives,
		IsEnabled:          isEnabled,
		SSLCertPath:        sslCertPath,
		SSLKeyPath:         sslKeyPath,
		PHPMemoryLimit:     phpMemLimit,
		PHPUploadMaxFilesize: phpUploadMax,
		PHPPostMaxSize:     phpPostMax,
		PHPMaxInputVars:    phpMaxInputVars,
		PHPMaxExecutionTime: phpMaxExecTime,
		PHPMaxInputTime:    phpMaxInputTime,
		PHPValueParam:      buildPHPValueParam(phpMemLimit, phpUploadMax, phpPostMax, phpMaxInputVars, phpMaxExecTime, phpMaxInputTime),
		ListenIPv4:         listenIPv4,
		ListenIPv6:         listenIPv6,
		CacheEnabled:       cacheEnabled,
		CacheKeyZone:       "jabali_fcgi",
		CacheTTL:           "60s",
	}

	var vhostConfig bytes.Buffer
	if err := tmpl.Execute(&vhostConfig, vhostData); err != nil {
		return "", fmt.Errorf("template execute failed: %w", err)
	}

	configPath := filepath.Join("/etc/nginx/sites-available", domain+".conf")
	enabledPath := filepath.Join("/etc/nginx/sites-enabled", domain+".conf")
	wantBytes := vhostConfig.Bytes()

	// Content-hash gate. The reconciler calls writeVhost on EVERY
	// per-domain pass (~60s ticks). Without this gate, every tick
	// rewrites every vhost + reloads nginx — puzzle's diagnostic
	// saw 181 reloads/hr = constant Lua reinit = ~340 MB PSS bloat.
	// Check before write: if the live file already matches AND the
	// symlink already points at it, no write + no reload needed.
	existingBytes, readErr := os.ReadFile(configPath)
	linkOK := false
	if target, lerr := os.Readlink(enabledPath); lerr == nil && target == configPath {
		linkOK = true
	}
	if readErr == nil && bytes.Equal(existingBytes, wantBytes) && linkOK {
		return configPath, nil
	}

	// Write vhost configuration atomically (temp file + rename).
	tmpFile := configPath + ".tmp"
	if err := os.WriteFile(tmpFile, wantBytes, 0644); err != nil {
		return "", fmt.Errorf("write config failed: %w", err)
	}
	if err := os.Rename(tmpFile, configPath); err != nil {
		os.Remove(tmpFile)
		return "", fmt.Errorf("rename config failed: %w", err)
	}

	// Create symlink to sites-enabled (always present for enabled or disabled domains).
	if !linkOK {
		os.Remove(enabledPath) // Remove if already exists
		if err := os.Symlink(configPath, enabledPath); err != nil {
			return "", fmt.Errorf("symlink failed: %w", err)
		}
	}

	// Test nginx configuration.
	testCmd := exec.CommandContext(ctx, "nginx", "-t")
	var testOutput bytes.Buffer
	testCmd.Stdout = &testOutput
	testCmd.Stderr = &testOutput
	if err := testCmd.Run(); err != nil {
		// Clean up on test failure.
		os.Remove(enabledPath)
		os.Remove(configPath)
		return "", fmt.Errorf("nginx test failed: %s", testOutput.String())
	}

	// Reload nginx.
	reloadCmd := exec.CommandContext(ctx, "systemctl", "reload", "nginx")
	var reloadOutput bytes.Buffer
	reloadCmd.Stdout = &reloadOutput
	reloadCmd.Stderr = &reloadOutput
	if err := reloadCmd.Run(); err != nil {
		return "", fmt.Errorf("systemctl reload nginx failed: %s", reloadOutput.String())
	}

	return configPath, nil
}

func domainCreateHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p domainCreateParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// Validate domain format
	if !domainRegex.MatchString(p.Domain) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid domain %q: must match ^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$", p.Domain),
		}
	}

	// Validate username format
	if !usernameRegex.MatchString(p.Username) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid username %q: must match ^[a-z_][a-z0-9_-]{0,31}$", p.Username),
		}
	}

	// Validate doc_root starts with /home/
	if !bytes.HasPrefix([]byte(p.DocRoot), []byte("/home/")) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid doc_root %q: must start with /home/", p.DocRoot),
		}
	}

	// Create doc_root directory
	mkdirCmd := exec.CommandContext(ctx, "mkdir", "-p", p.DocRoot)
	if err := mkdirCmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("mkdir failed: %v", err),
		}
	}

	// Chown + chmod every directory we created under /home/<user>/,
	// not just the leaf. mkdir -p would have left intermediates as
	// root:root; fix them so tenant data stays owned by the tenant.
	for _, dir := range pathsUnderHome(p.Username, p.DocRoot) {
		if err := exec.CommandContext(ctx, "chown", p.Username+":www-data", dir).Run(); err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("chown %s: %v", dir, err),
			}
		}
		if err := exec.CommandContext(ctx, "chmod", "0750", dir).Run(); err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("chmod %s: %v", dir, err),
			}
		}
	}

	// Write a default index.html so a fresh domain serves a welcome
	// page instead of nginx's 403 on empty directory. Idempotent: if
	// the file already exists (e.g. the user uploaded real content, or
	// the reconciler re-ran domain.create), leave it alone.
	//
	// Also skip when index.php exists — that's the unambiguous "real
	// content present" signal (WordPress install, hand-uploaded app).
	// Without this check, the reconciler periodically re-creates the
	// placeholder ~30s after a WP install, and nginx (with `index
	// index.html index.php`) serves the placeholder instead of WP.
	indexPath := filepath.Join(p.DocRoot, "index.html")
	indexPHPPath := filepath.Join(p.DocRoot, "index.php")
	_, htmlErr := os.Stat(indexPath)
	_, phpErr := os.Stat(indexPHPPath)
	if os.IsNotExist(htmlErr) && os.IsNotExist(phpErr) {
		if werr := writeDefaultIndex(ctx, indexPath, p.Username, p.Domain, p.DocRoot, p.DefaultIndexTemplate); werr != nil {
			// non-fatal — the vhost still works, it'll just 403 until
			// the user uploads content.
			log.Printf("domain.create: failed to write default index.html for %s: %v", p.Domain, werr)
		}
	}

	// Default IsEnabled to true if not provided (backwards compatibility)
	isEnabled := true
	if p.IsEnabled != nil {
		isEnabled = *p.IsEnabled
	}

	rateLimitDirectives := BuildRateLimitDirectives(p.DomainID, p.RateLimitRPS, p.ConnectionLimit)
	ipACLDirectives := buildIPACLDirectives(p.IPACLs)
	// M50: write htpasswd files first; on failure, do NOT write the vhost
	// (leaves nginx pointing at non-existent files = nginx -t fails).
	if _, err := syncDirectoryPrivacyFiles(p.DirectoryPrivacyRules, p.DirectoryPrivacyRuleIDs); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: err.Error(),
		}
	}
	dirPrivacyDirectives := buildDirectoryPrivacyDirectives(p.DirectoryPrivacyRules)
	configPath, err := writeVhost(ctx, p.Username, p.Domain, p.DocRoot, p.PHPVersion, p.RedirectDirectives, p.RuleDirectives, p.CustomDirectives, rateLimitDirectives, ipACLDirectives, dirPrivacyDirectives, p.IndexPriority, isEnabled, p.HasPHP, p.SSLCertPath, p.SSLKeyPath, p.PHPMemoryLimit, p.PHPUploadMaxFilesize, p.PHPPostMaxSize, p.PHPMaxInputVars, p.PHPMaxExecutionTime, p.PHPMaxInputTime, p.ListenIPv4, p.ListenIPv6, p.CacheEnabled)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: err.Error(),
		}
	}

	return domainCreateResponse{
		Domain:     p.Domain,
		DocRoot:    p.DocRoot,
		ConfigPath: configPath,
	}, nil
}

func writeDefaultIndex(ctx context.Context, path, username, domain, docRoot, customTmpl string) error {
	const builtin = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width,initial-scale=1">
  <title>{{.Domain}}</title>
  <style>
    body { font-family: system-ui, -apple-system, Segoe UI, Roboto, sans-serif; max-width: 640px; margin: 4rem auto; padding: 0 1.25rem; color: #222; line-height: 1.5; }
    h1 { color: #1976d2; margin-bottom: 0.25em; }
    .muted { color: #666; margin-top: 0; }
    code { background: #f5f5f5; padding: 0.15rem 0.4rem; border-radius: 4px; font-size: 0.92em; font-family: ui-monospace, Menlo, Consolas, monospace; }
    hr { border: none; border-top: 1px solid #eee; margin: 2rem 0; }
    small { color: #888; }
  </style>
</head>
<body>
  <h1>{{.Domain}}</h1>
  <p class="muted">This domain is hosted by Jabali Panel. The site is provisioned and waiting for your content.</p>
  <hr>
  <p>Upload your files to the document root:</p>
  <p><code>{{.DocRoot}}</code></p>
  <p><small>Logged in as <code>{{.Username}}</code>.</small></p>
</body>
</html>
`

	tmplBody := builtin
	if strings.TrimSpace(customTmpl) != "" {
		tmplBody = customTmpl
	}
	t, err := template.New("index").Parse(tmplBody)
	if err != nil {
		// Malformed operator template — don't crash domain.create;
		// fall back to the built-in so the docroot still serves
		// something. Logged at the caller.
		t, err = template.New("index").Parse(builtin)
		if err != nil {
			return fmt.Errorf("parse template: %w", err)
		}
	}

	// Write to a temp file first, then rename — avoids a half-written
	// index.html if we crash mid-write.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".index.*.html.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // cleanup if rename fails

	data := struct {
		Domain, Username, DocRoot string
	}{Domain: domain, Username: username, DocRoot: docRoot}
	if err := t.Execute(tmp, data); err != nil {
		tmp.Close()
		return fmt.Errorf("execute template: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}

	// Set permissions + ownership before rename so the final file
	// is correct from the first ls. 0644 for world-readable by nginx.
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}

	// Chown to <user>:www-data so it matches the docroot's ownership.
	// Using exec.Command since os.Chown needs numeric IDs we don't have.
	if err := exec.CommandContext(ctx, "chown", username+":www-data", tmpName).Run(); err != nil {
		return fmt.Errorf("chown: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

func init() {
	Default.Register("domain.create", domainCreateHandler)
}

// buildIPACLDirectives renders the M36 per-domain ACL list into a
// fragment of nginx directives — one line per rule. Inserted into the
// vhost server block so allow/deny applies to every location.
//
// Sanitises both fields: only rules with action ∈ {allow, deny} and
// CIDR matching a permissive ASCII subset (digits, dots, colons,
// hex, slash) reach the file. Anything else is silently dropped.
//
// Empty input → empty string (zero overhead, no comment line).
func buildIPACLDirectives(rules []domainIPACLRule) string {
	if len(rules) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("# M36 per-domain ACLs (priority order)\n")
	for _, r := range rules {
		action := r.Action
		if action != "allow" && action != "deny" {
			continue
		}
		cidr := strings.TrimSpace(r.CIDR)
		if !validACLCIDR(cidr) {
			continue
		}
		sb.WriteString("    ")
		sb.WriteString(action)
		sb.WriteString(" ")
		sb.WriteString(cidr)
		sb.WriteString(";\n")
	}
	return sb.String()
}

// validACLCIDR is a defensive whitelist of characters allowed in a
// CIDR string. The panel validates with net.ParseCIDR before storage,
// so this is belt-and-braces against a corrupted DB row containing
// shell/nginx metacharacters that could escape the directive.
func validACLCIDR(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		case r == '.' || r == ':' || r == '/':
		default:
			return false
		}
	}
	return true
}
