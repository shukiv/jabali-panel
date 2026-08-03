package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/filesafe"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/skelpath"
)

// domainCreateParams is the input shape for domain.create.
type domainCreateParams struct {
	Username   string `json:"username"`
	Domain     string `json:"domain"`
	DocRoot    string `json:"doc_root"`
	HasPHP     bool   `json:"has_php"`
	PHPVersion string `json:"php_version"`
	// FPMSocket is the FPM Unix socket the vhost's fastcgi_pass targets
	// (GH #329 per-domain PHP version). Empty ⇒ the legacy per-user socket
	// /run/php/jabali-<user>/fpm.sock, so older callers stay byte-identical.
	FPMSocket          string `json:"fpm_socket,omitempty"`
	CustomDirectives   string `json:"custom_directives"`
	RedirectDirectives string `json:"redirect_directives"`
	RuleDirectives     string `json:"rule_directives"`
	IndexPriority      string `json:"index_priority"`
	// IsEnabled controls whether the vhost serves the tenant's docroot
	// (true) or a branded "site disabled" placeholder (false). Pointer
	// so omitted fields default to true (backwards compat).
	IsEnabled *bool `json:"is_enabled"`
	// InterceptErrors (GH #879) — show the branded 500 page for PHP
	// application 5xx responses. A PHP fatal returns 500 with an EMPTY body
	// (display_errors off), so without interception visitors get the
	// browser's raw error screen. false ⇒ vhost byte-identical.
	InterceptErrors bool `json:"intercept_errors"`
	// CacheEnabled (ADR-0108) — per-domain nginx FastCGI micro-cache
	// opt-in. false ⇒ vhost byte-identical to the pre-0108 shape.
	CacheEnabled bool   `json:"cache_enabled"`
	CachePath    string `json:"cache_path,omitempty"`
	// CachePaths (GH #601): union of every cache-enabled install's path prefix
	// on this domain. When set, drives a multi-path gate; empty falls back to
	// the single CachePath so older callers stay byte-identical.
	CachePaths []string `json:"cache_paths,omitempty"`
	// CacheBypassPaths (GH #616): per-install URL-exclusion prefixes merged across
	// the domain (profile defaults + user list), resolved by the panel. The agent
	// RE-SANITIZES before rendering (config-injection trust boundary). A cookie
	// allowlist was intentionally NOT added — it would re-open the #416/#419
	// fail-open class on the shared microcache.
	CacheBypassPaths []string `json:"cache_bypass_paths,omitempty"`
	// CacheTTLSeconds (Gitea #596) — per-domain page-cache validity. 0 = use
	// the default. Drives fastcgi_cache_valid; background_update keeps it fresh.
	CacheTTLSeconds int `json:"cache_ttl_seconds,omitempty"`
	// CacheQueryAllowlist (migration 000230): query-param names that get their
	// own page-cache entry (e.g. "paged"). Empty ⇒ vhost byte-identical (feature
	// off). The agent RE-sanitizes before rendering (config-injection boundary,
	// like CacheBypassPaths).
	CacheQueryAllowlist []string `json:"cache_query_allowlist,omitempty"`

	// Preview URL (temp URL): when set, the vhost gains an extra server
	// block serving the same docroot under <slug>.preview.<hostname>.
	// PreviewCertPath/KeyPath point at the shared *.preview.<hostname>
	// wildcard pair; empty (or missing on disk) renders the preview block
	// HTTP-only — same graceful degradation as the main vhost.
	PreviewHost     string `json:"preview_host,omitempty"`
	PreviewCertPath string `json:"preview_cert_path,omitempty"`
	PreviewKeyPath  string `json:"preview_key_path,omitempty"`
	SSLCertPath         string   `json:"ssl_cert_path"`
	SSLKeyPath          string   `json:"ssl_key_path"`
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

	// GH #465 — the account docroot skeleton: operator-defined starter files
	// laid into a fresh docroot on create. Each file's RelPath is validated
	// again here (defense in depth) and never overwrites an existing path.
	Skeleton []skeletonFileParam `json:"skeleton,omitempty"`

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
        # M50: if Directory Privacy with path "/" applied auth_basic at
        # server scope, override here so the ACME validator can reach
        # the challenge token without a 401.
        auth_basic off;
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
{{ if .ListenIPv4 }}    listen {{.ListenIPv4}}:443 ssl{{.HTTP2Param}};
{{ else }}    listen 443 ssl{{.HTTP2Param}};
{{ end }}{{ if .ListenIPv6 }}    listen [{{.ListenIPv6}}]:443 ssl{{.HTTP2Param}};
{{ else }}    listen [::]:443 ssl{{.HTTP2Param}};
{{ end }}{{ if .HTTP2Directive }}    {{.HTTP2Directive}}
{{ end }}    # HTTP/2 is version-aware (GH #292): the listen ... http2
    # parameter on nginx <1.25.1 (where the standalone directive is an
    # unknown-directive error), the http2 on directive on >=1.25.1
    # (where the listen parameter is deprecated and warns every reload).
    server_name {{.Domain}} www.{{.Domain}};
    ssl_certificate {{.SSLCertPath}};
    ssl_certificate_key {{.SSLKeyPath}};
    # JAB-69: modern TLS + Mozilla-intermediate ciphers (no 3DES/RC4/CBC-weak).
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384:ECDHE-ECDSA-CHACHA20-POLY1305:ECDHE-RSA-CHACHA20-POLY1305:DHE-RSA-AES128-GCM-SHA256:DHE-RSA-AES256-GCM-SHA384;
    ssl_prefer_server_ciphers off;
    # JAB-70: baseline security headers (location / inherits these; cache-status
    # locations that set their own add_header re-declare them below).
    add_header Strict-Transport-Security "max-age=31536000" always;
    add_header X-Frame-Options "SAMEORIGIN" always;
    add_header X-Content-Type-Options "nosniff" always;
{{ end }}
{{ if .IsEnabled }}
    root {{.DocRoot}};
    # JAB-183: refuse to follow a symlink whose owner differs from the owner of
    # the thing it points at. Docroots are 2750 <user>:www-data and homes are
    # 0750 <user>:www-data, so the nginx worker (www-data) can traverse and read
    # EVERY tenant's tree. Without this a tenant could symlink from their own
    # docroot into another tenant's, name it .txt so the URI never matches the
    # PHP location, and have nginx serve the target verbatim -- wp-config.php
    # included. The PHP path is already confined by open_basedir; this is the
    # matching confinement for nginx's own static serving.
    #
    # if_not_owner (rather than a blanket 'on') is deliberate: a tenant symlinking
    # inside their own tree -- Laravel's public/storage, release-directory
    # deploys -- has matching owner on both ends and keeps working. Everything the
    # agent writes into a docroot is chowned to the tenant, so same-owner is the
    # norm for legitimate content. from=$document_root skips checks above the
    # docroot.
    #
    # Operator note: a rejection surfaces in the domain error log as
    #   openat() "..." failed (40: Too many levels of symbolic links)
    # at [crit]. That message is ELOOP from the kernel and does NOT mean a
    # symlink loop -- it is this directive refusing a cross-owner link. If a
    # legitimate app trips it, chown the link and its target to the same user
    # rather than removing this line.
    disable_symlinks if_not_owner from=$document_root;
    {{.IndexDirective}}

    {{.IPACLDirectives}}
    {{.DirectoryPrivacyDirectives}}

{{ if not .RootOverridden }}
    location / {
{{ if .HasPHP }}
        try_files $uri $uri/ /index.php?$query_string;
{{ else }}
        try_files $uri $uri/ =404;
{{ end }}
    }

    # Branded error pages (M28 page templates). error_page fires ONLY on
    # NATIVE nginx errors: a missing static file (try_files =404), a denied
    # directory (403), or an upstream 50x. fastcgi_intercept_errors is
    # OFF by default, so a real app (WordPress, etc.) keeps its own 404
    # — its index.php runs and owns the response. A no-app PHP domain hits
    # the index.php existence guard below, which yields a
    # native 404 that lands here. Files are converged from the editable
    # page_template rows into /var/www/jabali-errors by the reconciler.
    # The per-domain intercept_errors toggle (GH #879) turns interception
    # on inside the PHP locations for the 5xx codes only.
    error_page 404 /jabali-err-404.html;
    error_page 403 /jabali-err-403.html;
    error_page 500 502 503 504 /jabali-err-500.html;
    location = /jabali-err-404.html { internal; root /var/www/jabali-errors; }
    location = /jabali-err-403.html { internal; root /var/www/jabali-errors; }
    location = /jabali-err-500.html { internal; root /var/www/jabali-errors; }
{{ end }}
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
{{ if and .HasPHP (not .RootOverridden) }}
    # Front-controller with an existence guard: when index.php is absent
    # (domain has no app installed) try_files yields a native 404 instead
    # of PHP-FPM's bare "File not found", so the branded error_page shows.
    # When it exists the app runs and owns its own error pages.
    location = /index.php {
        try_files $uri =404;
        fastcgi_pass unix:{{.FPMSocket}};
        fastcgi_param SCRIPT_FILENAME $realpath_root$fastcgi_script_name;
        include fastcgi_params;
{{ if .InterceptErrors }}
        # GH #879: branded 500 for application errors. A PHP fatal returns
        # 500 with an empty body, so visitors otherwise get the browser's
        # raw error screen. error_page is redefined location-scoped with
        # ONLY the 5xx codes — the inherited 404/403 set is replaced here,
        # so the app keeps its own 404/403 pages (interception only fires
        # for codes that have a matching error_page in this location).
        fastcgi_intercept_errors on;
        error_page 500 502 503 504 /jabali-err-500.html;
{{ end }}
{{ if .PHPValueParam }}
        fastcgi_param PHP_VALUE "{{.PHPValueParam}}";
{{ end }}
{{ if .CacheEnabled }}
        # Fail-CLOSED page-cache gate (Gitea #416/#419): cache ONLY genuinely
        # anonymous GETs. Default to skip; allow only when there is NO cookie or
        # the cookie is exclusively known-benign analytics/consent. Auth, session,
        # language, currency, membership, A/B, and any unknown cookie therefore
        # bypass — so a member/paywall page or the wrong i18n/device variant is
        # never stored and served cross-visitor (the old denylist was fail-open).
        set $jabali_skip 1;
        if ($http_cookie = "") { set $jabali_skip 0; }
        if ($http_cookie ~* "^(?:\s*(?:_ga[\w-]*|_gid|_gat[\w-]*|_gcl[\w-]*|__utm[\w-]*|__gads|__gpi|_fbp|_fbc|_pk_[\w-]*|_hj[\w-]*|_clck|_clsk|_dc_gtm[\w-]*|cookie_consent[\w-]*|cookie_?notice[\w-]*|cookielawinfo[\w-]*|euconsent[\w-]*|wordpress_test_cookie)=[^;]*;?\s*)+$") { set $jabali_skip 0; }
        if ($request_method = POST) { set $jabali_skip 1; }
        # Gitea #610: bypass only when the query has a non-tracking (content-
        # affecting) param. Tracking-only (utm_*/gclid/fbclid/…) and empty query
        # strings are cacheable; $jabali_qs_kind is set by the strict map in
        # jabali-fastcgi-cache.conf. The path-only cache_key below collapses all
        # tracking variants onto the clean-URL entry.
{{if .CacheQAllowRegex}}        # cache-query-allowlist (migration 000230): only queries made ENTIRELY of
        # allowlisted params (non-empty values) cache; any other query param
        # bypasses. Replaces the shared qs_kind=other line for this domain. Only
        # ever SETS skip=1 — an allowlisted page relies on the earlier no-cookie
        # rule for skip=0, so logged-in users stay bypassed.
        set $jabali_qs_dirty 1;
        if ($query_string ~ "{{.CacheQAllowRegex}}") { set $jabali_qs_dirty 0; }
        if ($query_string = "") { set $jabali_qs_dirty 0; }
        if ($jabali_qs_dirty) { set $jabali_skip 1; }{{else}}        if ($jabali_qs_kind = other) { set $jabali_skip 1; }{{end}}
        if ($request_uri ~* "/wp-admin/|/wp-login|/xmlrpc\.php|/wp-cron\.php|/wp-json/|/cart|/checkout|/my-account|/wc-api/|/edd-api/{{.CacheExtraBypass}}") { set $jabali_skip 1; }
{{ if ne .CacheGate "" }}        # Gitea #420/#601: cache only within the cache-enabled install path(s) on
        # this domain (union of every cached install's prefix); other (non-WP,
        # differently-authed) apps on the same domain are never cached.
        # Gate on $jabali_cache_path — the ORIGINAL request path with the query
        # stripped (Codeberg #5). $uri would be /index.php after the try_files
        # rewrite (collapsing every permalink); $request_uri keeps the query which
        # broke the subdir homepage anchor (GH #629). $jabali_cache_path fixes both
        # and mirrors the cache_key below.
        if ($jabali_cache_path !~ "^{{.CacheGate}}(/|$)") { set $jabali_skip 1; }
{{ end }}        fastcgi_cache {{.CacheKeyZone}};
        # Gitea #610: path-only key (no query) so tracking-param variants
        # collapse onto one entry. Safe because only empty/tracking-only queries
        # reach caching (others bypass via $jabali_qs_kind above).
{{if .CacheQAllowNames}}        # cache-query-allowlist: fold allowlisted args into the key in a fixed
        # (panel-sorted) order so ?a=1&b=2 and ?b=2&a=1 collapse; non-allowlisted
        # args never enter the key.
        set $jabali_qkey "";
{{range .CacheQAllowNames}}        if ($arg_{{.}}) { set $jabali_qkey "${jabali_qkey}{{.}}=$arg_{{.}}&"; }
{{end}}        # Only add the "?" separator when an allowlisted arg is actually present,
        # so a query-less page keys as plain $jabali_cache_path (byte-identical to
        # the non-allowlist key AND targeted-purge-safe — post-edit purge computes
        # md5(scheme+method+host+path) with no trailing "?"). Query variants are
        # cleared by whole-domain purge (host-match) or TTL, not targeted purge.
        set $jabali_qsep "";
        if ($jabali_qkey != "") { set $jabali_qsep "?"; }
        fastcgi_cache_key "$scheme$request_method$host$jabali_cache_path$jabali_qsep$jabali_qkey";{{else}}        fastcgi_cache_key "$scheme$request_method$host$jabali_cache_path";{{end}}
        fastcgi_cache_valid 200 301 {{.CacheTTL}};
        fastcgi_cache_bypass $jabali_skip $http_authorization;
        # GH #637: also skip STORING when the backend opted out via Cache-Control
        # (no-cache/no-store/private). $jabali_upstream_nocache is set by the map
        # in jabali-fastcgi-cache.conf from $upstream_http_cache_control.
        fastcgi_no_cache $jabali_skip $jabali_upstream_nocache $http_authorization $jabali_vary_nocache;
        fastcgi_cache_use_stale error timeout updating http_500 http_503;
        fastcgi_cache_lock on;
        # Gitea #596: serve the stale copy instantly (~100ms) on expiry and
        # refresh in the background, instead of blocking the visitor on the
        # full synchronous PHP regen. Pairs with use_stale ... updating above.
        fastcgi_cache_background_update on;
        fastcgi_cache_revalidate on;
        access_log /var/log/nginx/jcache-{{.Domain}}.log jcache buffer=8k flush=5s;
        add_header X-Jabali-Cache $upstream_cache_status always;
        # JAB-70: re-declare security headers (a location's add_header suppresses
        # server-scope ones).
        add_header Strict-Transport-Security "max-age=31536000" always;
        add_header X-Frame-Options "SAMEORIGIN" always;
        add_header X-Content-Type-Options "nosniff" always;
        # Do NOT advertise public/CDN caching for dynamic PHP responses (Gitea
        # #417): the origin micro-cache is purgeable but browser/CDN copies are
        # not, and a skip-miss must never be amplified to Cloudflare scale. The
        # speedup is the server-side micro-cache only; clients always revalidate.
        fastcgi_hide_header Cache-Control;
        add_header Cache-Control "private, no-cache, max-age=0" always;
{{ end }}
    }
    location ~ \.php$ {
        # Existence guard (JAB-187). Without it a request for
        # /upload.jpg/x.php matches this location on the URI suffix, and PHP's
        # cgi.fix_pathinfo walk-back would execute the uploaded .jpg. php-fpm's
        # security.limit_extensions = .php (install/php/jabali-php-pool.conf.tmpl)
        # already rejects that, so this is the second lock on the same door --
        # it must not be removed on the assumption the pool setting is enough.
        try_files $uri =404;
        fastcgi_pass unix:{{.FPMSocket}};
        fastcgi_param SCRIPT_FILENAME $realpath_root$fastcgi_script_name;
        include fastcgi_params;
{{ if .InterceptErrors }}
        # GH #879: branded 500 for application errors (see the identical
        # block in location = /index.php).
        fastcgi_intercept_errors on;
        error_page 500 502 503 504 /jabali-err-500.html;
{{ end }}
{{ if .PHPValueParam }}
        fastcgi_param PHP_VALUE "{{.PHPValueParam}}";
{{ end }}
{{ if .CacheEnabled }}
        # Fail-CLOSED page-cache gate (Gitea #416/#419): cache ONLY genuinely
        # anonymous GETs. Default to skip; allow only when there is NO cookie or
        # the cookie is exclusively known-benign analytics/consent. Auth, session,
        # language, currency, membership, A/B, and any unknown cookie therefore
        # bypass — so a member/paywall page or the wrong i18n/device variant is
        # never stored and served cross-visitor (the old denylist was fail-open).
        set $jabali_skip 1;
        if ($http_cookie = "") { set $jabali_skip 0; }
        if ($http_cookie ~* "^(?:\s*(?:_ga[\w-]*|_gid|_gat[\w-]*|_gcl[\w-]*|__utm[\w-]*|__gads|__gpi|_fbp|_fbc|_pk_[\w-]*|_hj[\w-]*|_clck|_clsk|_dc_gtm[\w-]*|cookie_consent[\w-]*|cookie_?notice[\w-]*|cookielawinfo[\w-]*|euconsent[\w-]*|wordpress_test_cookie)=[^;]*;?\s*)+$") { set $jabali_skip 0; }
        if ($request_method = POST) { set $jabali_skip 1; }
        # Gitea #610: bypass only when the query has a non-tracking (content-
        # affecting) param. Tracking-only (utm_*/gclid/fbclid/…) and empty query
        # strings are cacheable; $jabali_qs_kind is set by the strict map in
        # jabali-fastcgi-cache.conf. The path-only cache_key below collapses all
        # tracking variants onto the clean-URL entry.
{{if .CacheQAllowRegex}}        # cache-query-allowlist (migration 000230): only queries made ENTIRELY of
        # allowlisted params (non-empty values) cache; any other query param
        # bypasses. Replaces the shared qs_kind=other line for this domain. Only
        # ever SETS skip=1 — an allowlisted page relies on the earlier no-cookie
        # rule for skip=0, so logged-in users stay bypassed.
        set $jabali_qs_dirty 1;
        if ($query_string ~ "{{.CacheQAllowRegex}}") { set $jabali_qs_dirty 0; }
        if ($query_string = "") { set $jabali_qs_dirty 0; }
        if ($jabali_qs_dirty) { set $jabali_skip 1; }{{else}}        if ($jabali_qs_kind = other) { set $jabali_skip 1; }{{end}}
        if ($request_uri ~* "/wp-admin/|/wp-login|/xmlrpc\.php|/wp-cron\.php|/wp-json/|/cart|/checkout|/my-account|/wc-api/|/edd-api/{{.CacheExtraBypass}}") { set $jabali_skip 1; }
{{ if ne .CacheGate "" }}        # Gitea #420/#601: cache only within the cache-enabled install path(s) on
        # this domain (union of every cached install's prefix); other (non-WP,
        # differently-authed) apps on the same domain are never cached.
        # Gate on $jabali_cache_path — the ORIGINAL request path with the query
        # stripped (Codeberg #5). $uri would be /index.php after the try_files
        # rewrite (collapsing every permalink); $request_uri keeps the query which
        # broke the subdir homepage anchor (GH #629). $jabali_cache_path fixes both
        # and mirrors the cache_key below.
        if ($jabali_cache_path !~ "^{{.CacheGate}}(/|$)") { set $jabali_skip 1; }
{{ end }}        fastcgi_cache {{.CacheKeyZone}};
        # Gitea #610: path-only key (no query) so tracking-param variants
        # collapse onto one entry. Safe because only empty/tracking-only queries
        # reach caching (others bypass via $jabali_qs_kind above).
{{if .CacheQAllowNames}}        # cache-query-allowlist: fold allowlisted args into the key in a fixed
        # (panel-sorted) order so ?a=1&b=2 and ?b=2&a=1 collapse; non-allowlisted
        # args never enter the key.
        set $jabali_qkey "";
{{range .CacheQAllowNames}}        if ($arg_{{.}}) { set $jabali_qkey "${jabali_qkey}{{.}}=$arg_{{.}}&"; }
{{end}}        # Only add the "?" separator when an allowlisted arg is actually present,
        # so a query-less page keys as plain $jabali_cache_path (byte-identical to
        # the non-allowlist key AND targeted-purge-safe — post-edit purge computes
        # md5(scheme+method+host+path) with no trailing "?"). Query variants are
        # cleared by whole-domain purge (host-match) or TTL, not targeted purge.
        set $jabali_qsep "";
        if ($jabali_qkey != "") { set $jabali_qsep "?"; }
        fastcgi_cache_key "$scheme$request_method$host$jabali_cache_path$jabali_qsep$jabali_qkey";{{else}}        fastcgi_cache_key "$scheme$request_method$host$jabali_cache_path";{{end}}
        fastcgi_cache_valid 200 301 {{.CacheTTL}};
        fastcgi_cache_bypass $jabali_skip $http_authorization;
        # GH #637: also skip STORING when the backend opted out via Cache-Control
        # (no-cache/no-store/private). $jabali_upstream_nocache is set by the map
        # in jabali-fastcgi-cache.conf from $upstream_http_cache_control.
        fastcgi_no_cache $jabali_skip $jabali_upstream_nocache $http_authorization $jabali_vary_nocache;
        fastcgi_cache_use_stale error timeout updating http_500 http_503;
        fastcgi_cache_lock on;
        # Gitea #596: serve the stale copy instantly (~100ms) on expiry and
        # refresh in the background, instead of blocking the visitor on the
        # full synchronous PHP regen. Pairs with use_stale ... updating above.
        fastcgi_cache_background_update on;
        fastcgi_cache_revalidate on;
        access_log /var/log/nginx/jcache-{{.Domain}}.log jcache buffer=8k flush=5s;
        add_header X-Jabali-Cache $upstream_cache_status always;
        # JAB-70: re-declare security headers (a location's add_header suppresses
        # server-scope ones).
        add_header Strict-Transport-Security "max-age=31536000" always;
        add_header X-Frame-Options "SAMEORIGIN" always;
        add_header X-Content-Type-Options "nosniff" always;
        # Do NOT advertise public/CDN caching for dynamic PHP responses (Gitea
        # #417): the origin micro-cache is purgeable but browser/CDN copies are
        # not, and a skip-miss must never be amplified to Cloudflare scale. The
        # speedup is the server-side micro-cache only; clients always revalidate.
        fastcgi_hide_header Cache-Control;
        add_header Cache-Control "private, no-cache, max-age=0" always;
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
}
{{- if .PreviewHost }}

# Preview URL — reverse-proxies this domain's OWN vhost with the real
# Host header, so canonical-URL apps (WordPress etc.) never see a host
# mismatch and never redirect away. Location headers, cookie domains and
# absolute URLs in bodies (including the JSON-escaped form Elementor
# stores) are rewritten to the preview host. Pattern proven in
# production on the hostsclick preview fleet; adapted here for the
# local hairpin (SNI to our own vhost, self-signed backend tolerated —
# proxy_ssl_verify defaults off). X-Robots-Tag keeps previews out of
# search indexes.
server {
{{ if .ListenIPv4 }}    listen {{.ListenIPv4}}:80;
{{ else }}    listen 80;
{{ end }}{{ if .ListenIPv6 }}    listen [{{.ListenIPv6}}]:80;
{{ else }}    listen [::]:80;
{{ end }}    server_name {{.PreviewHost}};
{{ if .PreviewCertPath }}
    location / {
        return 301 https://$host$request_uri;
    }
}

server {
{{ if .ListenIPv4 }}    listen {{.ListenIPv4}}:443 ssl{{.HTTP2Param}};
{{ else }}    listen 443 ssl{{.HTTP2Param}};
{{ end }}{{ if .ListenIPv6 }}    listen [{{.ListenIPv6}}]:443 ssl{{.HTTP2Param}};
{{ else }}    listen [::]:443 ssl{{.HTTP2Param}};
{{ end }}{{ if .HTTP2Directive }}    {{.HTTP2Directive}}
{{ end }}    server_name {{.PreviewHost}};
    ssl_certificate {{.PreviewCertPath}};
    ssl_certificate_key {{.PreviewKeyPath}};
    ssl_protocols TLSv1.2 TLSv1.3;
{{ end }}
    add_header X-Robots-Tag "noindex, nofollow" always;
    client_max_body_size 64m;
    access_log /var/log/nginx/{{.Domain}}-preview-access.log;
    error_log /var/log/nginx/{{.Domain}}-preview-error.log;

    location / {
        proxy_pass {{.PreviewUpstream}};
        proxy_set_header Host {{.Domain}};
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
{{ if .SSLCertPath }}        proxy_ssl_server_name on;
        proxy_ssl_name {{.Domain}};
{{ end }}        proxy_redirect http://www.{{.Domain}} http://$http_host;
        proxy_redirect https://www.{{.Domain}} https://$http_host;
        proxy_redirect http://{{.Domain}} http://$http_host;
        proxy_redirect https://{{.Domain}} https://$http_host;
        proxy_cookie_domain www.{{.Domain}} $host;
        proxy_cookie_domain {{.Domain}} $host;

        # Body rewrite: plain and JSON-escaped absolute URLs, apex + www.
        # Upstream compression must be off for sub_filter to see the
        # bytes; gunzip re-inflates anything that slips through.
        sub_filter_types text/plain text/css text/xml application/javascript application/x-javascript text/javascript application/json application/ld+json image/svg+xml;
        sub_filter 'http://www.{{.Domain}}' 'http://$http_host';
        sub_filter 'https://www.{{.Domain}}' 'https://$http_host';
        sub_filter 'http://{{.Domain}}' 'http://$http_host';
        sub_filter 'https://{{.Domain}}' 'https://$http_host';
        sub_filter 'http:\/\/www.{{.Domain}}' 'http:\/\/$http_host';
        sub_filter 'https:\/\/www.{{.Domain}}' 'https:\/\/$http_host';
        sub_filter 'http:\/\/{{.Domain}}' 'http:\/\/$http_host';
        sub_filter 'https:\/\/{{.Domain}}' 'https:\/\/$http_host';
        sub_filter_once off;
        gunzip on;
        proxy_set_header Accept-Encoding "";
    }
}
{{ end }}`

type vhostData struct {
	Domain               string
	PreviewHost          string
	PreviewCertPath      string
	PreviewKeyPath       string
	PreviewUpstream      string
	DocRoot              string
	HasPHP               bool
	PHPVersion           string
	Username             string
	FPMSocket            string
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
	RateLimitDirectives string
	// M24 listen IPs. Empty string = all-interfaces fallback handled by
	// the template's explicit if/else. Plain string (validated by
	// panel-api before reaching the agent) so the template stays simple.
	ListenIPv4 string
	ListenIPv6 string
	// HTTP/2 form selected per host nginx version (GH #292).
	HTTP2Param     string // " http2" on nginx <1.25.1, "" on >=1.25.1
	HTTP2Directive string // "" on nginx <1.25.1, "http2 on;" on >=1.25.1
	// M36 IP allow/deny — pre-rendered nginx directives. One line per
	// rule, "allow <cidr>;" or "deny <cidr>;", in priority order. Empty
	// string when the domain has no ACLs (zero overhead). Sits inside
	// the server block so it applies to every location.
	IPACLDirectives string
	// M50 directory privacy — pre-rendered `location ^~ <path>/ {
	// auth_basic ...; auth_basic_user_file ...; }` blocks, one per
	// rule. Empty string when the domain has no privacy rules.
	DirectoryPrivacyDirectives string
	// InterceptErrors (GH #879) — branded 500 for PHP app 5xx. Renders
	// fastcgi_intercept_errors + a 50x-only location-scoped error_page in
	// the PHP locations. false ⇒ byte-identical.
	InterceptErrors bool
	// ADR-0108 FastCGI micro-cache. All three are panel/agent-controlled
	// (never user data). When CacheEnabled is false the template emits
	// none of the cache/static directives → byte-identical to pre-0108.
	CacheEnabled     bool
	CachePath        string // Gitea #420: page-cache path prefix ("/" = whole domain)
	CacheGate        string // Gitea #601: regex body for the multi-path gate ("" = whole domain, no gate)
	CacheExtraBypass string // Gitea #616: "|/path|..." suffix appended to the built-in bypass regex ("" = none)
	// CacheQAllowRegex ("" = feature off ⇒ byte-identical): anchored $query_string
	// match for allowlist-only queries. CacheQAllowNames: sorted allowlisted
	// param names driving the canonical $arg_<name> key builder (migration 000230).
	CacheQAllowRegex  string
	CacheQAllowNames  []string
	CacheKeyZone      string
	CacheTTL          string
	CacheTTLSeconds   int // numeric form of CacheTTL for Cache-Control max-age
	CacheClientMaxAge int // client Cache-Control max-age (browser/CDN); decoupled from server TTL
	// RootOverridden is true when CustomDirectives contain a
	// `location /` block (any modifier — exact, prefix, regex). The
	// template omits its own default `location /` + `location ~ \.php$`
	// blocks in that case so the user's custom directives become the
	// authoritative routing for the whole domain. Use case: panel UI
	// "reverse proxy domain to upstream X" without hand-rolling a
	// /etc/nginx/conf.d/* override that lives outside the reconciler.
	RootOverridden bool
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
// cachePathSegmentRE bounds the page-cache path prefix to a single safe segment
// under root (Gitea #420 / nginx-template-injection guard).
var cachePathSegmentRE = regexp.MustCompile(`^/[a-z0-9][a-z0-9_-]{0,63}$`)

// sanitizeCachePath re-validates the page-cache path prefix at the agent (the
// nginx config-generation trust boundary): it is rendered verbatim into the
// regex `^<path>(/|$)`, so anything that is not "/" or a single safe segment
// falls back to "/" (whole domain, no scoping) and can never inject nginx
// directives or break the regex — regardless of what the panel sent.
// clampCacheTTL bounds the per-domain page-cache validity (Gitea #596). 0 (or
// unset) → the 600s default; otherwise clamped to [10s, 24h] so a bad panel
// value can never render a degenerate fastcgi_cache_valid.
func clampCacheTTL(s int) int {
	switch {
	case s <= 0:
		return 600
	case s < 10:
		return 10
	case s > 86400:
		return 86400
	default:
		return s
	}
}

func sanitizeCachePath(p string) string {
	if p != "/" && !cachePathSegmentRE.MatchString(p) {
		return "/"
	}
	return p
}

// cachePathMultiSegRE bounds a page-cache path to one or more safe segments
// (GH #601 allows nested installs like /shop/eu, which the single-segment
// cachePathSegmentRE rejects). Same char class per segment.
var cachePathMultiSegRE = regexp.MustCompile(`^/[a-z0-9][a-z0-9_-]{0,63}(?:/[a-z0-9][a-z0-9_-]{0,63})*$`)

// bypassPathRE / bypassCookieRE bound user-provided page-cache rules (GH #616)
// to characters that can never break out of the nginx `~* "..."` string or
// inject a regex operator. The agent RE-VALIDATES even though the panel already
// checked (defense in depth — the config-generation trust boundary). Anything
// with a quote, backslash, space, brace, paren, pipe, dollar, or control char
// is rejected outright; the survivors are additionally QuoteMeta'd so a literal
// "." matches "." rather than any char.
var (
	bypassPathRE = regexp.MustCompile(`^/[A-Za-z0-9._/-]{0,127}$`)
)

// sanitizeBypassPaths turns user URL-exclusion prefixes into a safe regex
// alternation SUFFIX ("|/private|/account") appended to the built-in bypass set,
// or "" if none survive. Invalid entries are dropped (fail-safe), never escaped
// into the config raw.
func sanitizeBypassPaths(paths []string) string {
	seen := map[string]bool{}
	parts := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if !bypassPathRE.MatchString(p) || seen[p] {
			continue
		}
		seen[p] = true
		parts = append(parts, regexp.QuoteMeta(p))
	}
	if len(parts) == 0 {
		return ""
	}
	return "|" + strings.Join(parts, "|")
}

// cacheQueryParamRE bounds an allowlist entry at the agent trust boundary:
// lower-case alnum + underscore, 1-32 chars — the same guard the panel applies,
// re-checked here so a name can never carry regex metacharacters into the
// rendered config. Mirrors the sanitizeBypassPaths fail-safe.
var cacheQueryParamRE = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

// sanitizeCacheQueryAllowlist re-validates, lower-cases, de-dups, and sorts the
// panel-supplied allowlist. Invalid entries are dropped fail-safe. Returns the
// surviving sorted names (nil = feature off).
func sanitizeCacheQueryAllowlist(names []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(names))
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if !cacheQueryParamRE.MatchString(n) || seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

// cacheQueryAllowRegex builds the anchored nginx regex that matches a query
// string composed ONLY of allowlisted params with NON-EMPTY values, e.g.
// `^(?:(?:paged|s)=[^&]+)(?:&(?:paged|s)=[^&]+)*$`. The value class is `[^&]+`
// (non-empty) so it agrees with `if ($arg_<name>)` in the key builder — an
// empty value (`?paged=`) is treated as dirty and bypasses, never colliding
// with the base path. "" when no names (feature off). names must already be
// sanitized (no regex metachars).
func cacheQueryAllowRegex(names []string) string {
	if len(names) == 0 {
		return ""
	}
	alt := strings.Join(names, "|")
	return `^(?:(?:` + alt + `)=[^&]+)(?:&(?:` + alt + `)=[^&]+)*$`
}

// buildCacheGate returns the regex BODY for the page-cache path gate
// `^<body>(/|$)`, or "" meaning "no gate → cache the whole domain".
//
// GH #601: a domain can host several cache-enabled installs; paths is the union
// of their path prefixes. Rules, in order:
//   - any path is "/" (a root install) ⇒ whole domain cached ⇒ "" (no gate).
//   - otherwise build an alternation of the VALID paths: each is sanitized
//     INDIVIDUALLY (advisor); a path that doesn't match is SKIPPED (fail-safe
//     under-cache), never collapsed to "/" (which would over-cache the domain).
//   - if paths is empty or yields nothing usable, fall back to the single
//     cache_path's legacy behaviour so callers stay byte-identical.
func buildCacheGate(paths []string, fallback string) string {
	singleGate := func() string {
		fb := sanitizeCachePath(fallback)
		if fb == "/" || fb == "" {
			return ""
		}
		return "(" + regexp.QuoteMeta(fb) + ")"
	}
	if len(paths) == 0 {
		return singleGate()
	}
	seen := map[string]bool{}
	valid := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "/" {
			return "" // a root install caches everything
		}
		if !cachePathMultiSegRE.MatchString(p) || seen[p] {
			continue // skip invalid/dupe — fail safe (under-cache), never "/"
		}
		seen[p] = true
		valid = append(valid, regexp.QuoteMeta(p))
	}
	if len(valid) == 0 {
		return singleGate()
	}
	return "(" + strings.Join(valid, "|") + ")"
}

func writeVhost(ctx context.Context, username, domain, docRoot, phpVersion, redirectDirectives, ruleDirectives, customDirectives, rateLimitDirectives, ipACLDirectives, dirPrivacyDirectives, indexPriority string, isEnabled, hasPHP bool, sslCertPath, sslKeyPath, phpMemLimit, phpUploadMax, phpPostMax string, phpMaxInputVars, phpMaxExecTime, phpMaxInputTime int, listenIPv4, listenIPv6 string, cacheEnabled bool, cachePath string, cachePaths []string, cacheBypassPaths []string, cacheTTLSeconds int, cacheQueryAllowlist []string, fpmSocket string, previewHost, previewCertPath, previewKeyPath string, interceptErrors bool) (string, error) {
	cacheGate := buildCacheGate(cachePaths, cachePath)        // GH #601: multi-path gate body ("" = whole domain)
	cacheExtraBypass := sanitizeBypassPaths(cacheBypassPaths) // GH #616: safe "|/path" suffix
	cacheQAllowNames := sanitizeCacheQueryAllowlist(cacheQueryAllowlist)
	cacheQAllowRegex := cacheQueryAllowRegex(cacheQAllowNames)
	cachePath = sanitizeCachePath(cachePath)
	cacheTTLSeconds = clampCacheTTL(cacheTTLSeconds)
	// GH #329: default to the legacy per-user socket when the caller didn't
	// specify one, so the vhost is byte-identical for default-pool domains.
	if fpmSocket == "" {
		fpmSocket = fmt.Sprintf("/run/php/jabali-%s/fpm.sock", username)
	}
	// SSL cert may be referenced by the DB (ssl_cert_path set) but MISSING
	// on disk — most commonly after a reinstall wiped /etc/letsencrypt while
	// the panel DB kept the cert row. Emitting the 443 block with
	// `ssl_certificate <missing-file>` makes `nginx -t` fail, and the
	// rollback below then removes the WHOLE vhost — including the port-80
	// ACME location — so the cert can never be re-obtained (GH #213: a full
	// reinstall left the apex vhost absent, the HTTP-01 challenge 404'd, and
	// the cert was stuck pending forever). Fall back to an HTTP-only vhost
	// when the cert is absent; the reconciler's port-80 ACME location then
	// re-issues the cert and the next reconcile pass restores the 443 block.
	if sslCertPath != "" {
		if _, statErr := os.Stat(sslCertPath); statErr != nil {
			sslCertPath = ""
			sslKeyPath = ""
		}
	}
	// Same missing-file fallback for the preview wildcard pair: the shared
	// *.preview.<hostname> cert may not be issued yet when the first
	// preview-enabled domain converges — serve the preview HTTP-only until
	// the reconciler's ACME sweep lands it.
	if previewCertPath != "" {
		if _, statErr := os.Stat(previewCertPath); statErr != nil {
			previewCertPath = ""
			previewKeyPath = ""
		}
	}
	// The preview block only renders for enabled domains — a disabled
	// domain's preview must go dark with it.
	if !isEnabled {
		previewHost = ""
	}
	// Preview upstream: hairpin into this domain's own vhost. Prefer the
	// domain's bound listen IP (an IP-scoped listen won't answer on
	// loopback); https whenever the main vhost serves TLS (post
	// stat-guard, so a self-signed fallback pair counts) — the backend
	// then sees is_ssl() true and never protocol-redirects into a loop.
	previewUpstream := ""
	if previewHost != "" {
		upstreamAddr := "127.0.0.1"
		if listenIPv4 != "" {
			upstreamAddr = listenIPv4
		}
		if sslCertPath != "" {
			previewUpstream = "https://" + upstreamAddr + ":443"
		} else {
			previewUpstream = "http://" + upstreamAddr + ":80"
		}
	}

	// Generate vhost configuration
	tmpl, err := template.New("vhost").Parse(vhostTemplate)
	if err != nil {
		return "", fmt.Errorf("template parse failed: %w", err)
	}

	h2 := nginxHTTP2()
	vhostData := vhostData{
		Domain:                     domain,
		PreviewHost:                previewHost,
		PreviewCertPath:            previewCertPath,
		PreviewKeyPath:             previewKeyPath,
		PreviewUpstream:            previewUpstream,
		HTTP2Param:                 h2.Param,
		HTTP2Directive:             h2.Directive,
		DocRoot:                    docRoot,
		HasPHP:                     hasPHP,
		PHPVersion:                 phpVersion,
		Username:                   username,
		FPMSocket:                  fpmSocket,
		IndexDirective:             indexDirectiveFor(indexPriority),
		RedirectDirectives:         redirectDirectives,
		RuleDirectives:             ruleDirectives,
		CustomDirectives:           customDirectives,
		RateLimitDirectives:        rateLimitDirectives,
		IPACLDirectives:            ipACLDirectives,
		DirectoryPrivacyDirectives: dirPrivacyDirectives,
		IsEnabled:                  isEnabled,
		SSLCertPath:                sslCertPath,
		SSLKeyPath:                 sslKeyPath,
		PHPMemoryLimit:             phpMemLimit,
		PHPUploadMaxFilesize:       phpUploadMax,
		PHPPostMaxSize:             phpPostMax,
		PHPMaxInputVars:            phpMaxInputVars,
		PHPMaxExecutionTime:        phpMaxExecTime,
		PHPMaxInputTime:            phpMaxInputTime,
		PHPValueParam:              buildPHPValueParam(phpMemLimit, phpUploadMax, phpPostMax, phpMaxInputVars, phpMaxExecTime, phpMaxInputTime),
		ListenIPv4:                 listenIPv4,
		ListenIPv6:                 listenIPv6,
		InterceptErrors:            interceptErrors,
		CacheEnabled:               cacheEnabled,
		CachePath:                  cachePath,
		CacheGate:                  cacheGate,
		CacheExtraBypass:           cacheExtraBypass,
		CacheQAllowRegex:           cacheQAllowRegex,
		CacheQAllowNames:           cacheQAllowNames,
		CacheKeyZone:               "jabali_fcgi",
		CacheTTL:                   fmt.Sprintf("%ds", cacheTTLSeconds),
		CacheTTLSeconds:            cacheTTLSeconds,
		CacheClientMaxAge:          300,
		// RootOverridden tracks whether ANY user-controlled directive
		// block declares `location /`. Both raw `customDirectives`
		// (admin Raw Directives tab) and `ruleDirectives` (compiled
		// from nginx_rules; Rule Builder proxy_pass with path=`/` is
		// the common case) can collide with the template's default
		// `location /`. Without checking both, a Rule Builder
		// proxy_pass to `/` renders TWO `location /` blocks ->
		// nginx -t emerg "duplicate location /" -> vhost rollback ->
		// tenant domain falls through to a sibling vhost (the first
		// specific-IP listener, usually a mail vhost). Caught
		// 2026-06-04 on vpsjournal.com/yacht.vpsjournal.com.
		RootOverridden: directivesOverrideRoot(customDirectives, ruleDirectives),
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
	// JAB-71: hold the global nginx mutex across the whole decide→write→test→
	// reload below, so a concurrent handler can't write a broken vhost between
	// our `nginx -t` passing and our reload.
	nginxOpMu.Lock()
	defer nginxOpMu.Unlock()

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
		return "", nginxTestFailure("domain.vhost", testOutput.String())
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

	// Preview host is panel-derived (<slug>.preview.<hostname>) but
	// validated like any hostname as defense in depth — it lands in a
	// server_name directive.
	if p.PreviewHost != "" && !domainRegex.MatchString(p.PreviewHost) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid preview_host %q", p.PreviewHost),
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

	// Provision the docroot through a filesafe scope rooted at the tenant's own
	// home (Gitea #476). doc_root components (domains/<domain>/public_html) are
	// tenant-controlled; a pre-planted symlink would let a plain root mkdir -p /
	// chown / chmod be redirected outside the home subtree (or into another
	// tenant). openat2 RESOLVE_BENEATH refuses any symlinked component and binds
	// the path to /home/<user>.
	homeDir := "/home/" + p.Username
	docScope, scErr := filesafe.NewScope(p.Username, p.Username, []string{homeDir})
	if scErr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "scope init: " + scErr.Error()}
	}
	docrootCreated, err := docScope.MkdirInScope(p.DocRoot, true, 0o755)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("mkdir failed: %v", err),
		}
	}

	// Resolve uid (tenant) + gid (www-data) for the fd-based chown.
	docUser, duErr := user.Lookup(p.Username)
	if duErr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeFailedPrecondition, Message: fmt.Sprintf("user %q not found: %v", p.Username, duErr)}
	}
	docUID, _ := parseUID(docUser)
	wwwGID, wgErr := lookupGroup("www-data")
	if wgErr != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "lookup www-data: " + wgErr.Error()}
	}

	// Chown + chmod every directory we created under /home/<user>/, not just
	// the leaf — via directory fds (fchown/fchmod, symlink-safe). 2750: the
	// setgid bit makes files the app (PHP-FPM, group=<user>) later creates
	// inherit the docroot's www-data group, so nginx can read uploads (GH
	// #194-adjacent). os.ModeSetgid is the portable spelling of the 02000 bit.
	for _, dir := range pathsUnderHome(p.Username, p.DocRoot) {
		df, derr := docScope.OpenDirInScope(dir)
		if derr != nil {
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("open %s: %v", dir, derr)}
		}
		if err := df.Chown(docUID, wwwGID); err != nil {
			_ = df.Close()
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chown %s: %v", dir, err)}
		}
		if err := df.Chmod(os.ModeSetgid | 0o750); err != nil {
			_ = df.Close()
			return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf("chmod %s: %v", dir, err)}
		}
		_ = df.Close()
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
	// GH #465: lay the operator's account skeleton into the fresh docroot
	// BEFORE the default index, so a skeleton-provided index.html wins (the
	// default-index block below skips when index.html already exists). Never
	// clobbers an existing path; a bad file warns but doesn't fail create.
	for _, w := range layAccountSkeleton(docScope, docrootCreated, p.DocRoot, p.Username, docUID, wwwGID, p.Skeleton) {
		log.Printf("domain.create: skeleton %s: %s", p.Domain, w)
	}

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
	configPath, err := writeVhost(ctx, p.Username, p.Domain, p.DocRoot, p.PHPVersion, p.RedirectDirectives, p.RuleDirectives, p.CustomDirectives, rateLimitDirectives, ipACLDirectives, dirPrivacyDirectives, p.IndexPriority, isEnabled, p.HasPHP, p.SSLCertPath, p.SSLKeyPath, p.PHPMemoryLimit, p.PHPUploadMaxFilesize, p.PHPPostMaxSize, p.PHPMaxInputVars, p.PHPMaxExecutionTime, p.PHPMaxInputTime, p.ListenIPv4, p.ListenIPv6, p.CacheEnabled, p.CachePath, p.CachePaths, p.CacheBypassPaths, p.CacheTTLSeconds, p.CacheQueryAllowlist, p.FPMSocket, p.PreviewHost, p.PreviewCertPath, p.PreviewKeyPath, p.InterceptErrors)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: err.Error(),
		}
	}

	// #430 interim: self-healing re-tighten of docroot secrets (wp-config.php /
	// .env) to 0600 every reconcile — backfills existing installs + re-tightens
	// anything widened via the file manager.
	hardenSensitiveFilesInScope(p.Username, p.DocRoot)

	return domainCreateResponse{
		Domain:     p.Domain,
		DocRoot:    p.DocRoot,
		ConfigPath: configPath,
	}, nil
}

// skeletonFileParam is one account-skeleton file on the domain.create wire.
// Content marshals as base64 (Go's []byte JSON encoding).
type skeletonFileParam struct {
	RelPath string `json:"rel_path"`
	Content []byte `json:"content"`
}

// layAccountSkeleton writes the skeleton tree only when THIS call actually
// created the docroot. domain.create is re-run by the reconciler every tick;
// an unconditional write resurrected every skeleton file the user had deleted
// (the #465 reopen — "I press delete ... when refreshing the file reappears").
// O_EXCL in writeSkeleton protects edited files but not deleted ones;
// docrootCreated is the "this is a new account/domain" signal.
func layAccountSkeleton(scope *filesafe.Scope, docrootCreated bool, docRoot, username string, docUID, wwwGID int, files []skeletonFileParam) []string {
	if !docrootCreated || len(files) == 0 {
		return nil
	}
	return writeSkeleton(scope, docRoot, username, docUID, wwwGID, files)
}

// writeSkeleton lays each skeleton file into the fresh docroot through the
// symlink-safe scope: re-validate the path (defense in depth — a poisoned DB
// row must not escape the docroot), create parent dirs chowned <user>:www-data
// + setgid, and write the file with O_EXCL|O_NOFOLLOW so it NEVER clobbers an
// existing path or follows a planted symlink, then chown <user>:www-data 0644.
// A bad file is collected as a warning; it must not fail the whole
// domain.create.
func writeSkeleton(scope *filesafe.Scope, docRoot, username string, docUID, wwwGID int, files []skeletonFileParam) []string {
	var warns []string
	for _, f := range files {
		if err := skelpath.Validate(f.RelPath); err != nil {
			warns = append(warns, fmt.Sprintf("%q rejected: %v", f.RelPath, err))
			continue
		}
		target := filepath.Join(docRoot, f.RelPath)
		if rel, rerr := filepath.Rel(docRoot, target); rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			warns = append(warns, fmt.Sprintf("%q escapes docroot", f.RelPath))
			continue
		}
		// No clobber: skip a path that already exists (real content, a re-run).
		if _, err := os.Lstat(target); err == nil {
			continue
		}
		// Create parent dirs symlink-safely, then chown/setgid every dir on the
		// chain (idempotent for ones that already existed).
		if _, err := scope.MkdirInScope(filepath.Dir(target), true, 0o755); err != nil {
			warns = append(warns, fmt.Sprintf("mkdir for %q: %v", f.RelPath, err))
			continue
		}
		for _, dir := range pathsUnderHome(username, filepath.Dir(target)) {
			df, derr := scope.OpenDirInScope(dir)
			if derr != nil {
				continue
			}
			_ = df.Chown(docUID, wwwGID)
			_ = df.Chmod(os.ModeSetgid | 0o750)
			_ = df.Close()
		}
		fh, oerr := scope.Open(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
		if oerr != nil {
			warns = append(warns, fmt.Sprintf("create %q: %v", f.RelPath, oerr))
			continue
		}
		if _, werr := fh.Write(f.Content); werr != nil {
			_ = fh.Close()
			warns = append(warns, fmt.Sprintf("write %q: %v", f.RelPath, werr))
			continue
		}
		_ = fh.Chmod(0o644) // O_CREATE perm is umask-masked; force 0644
		_ = fh.Close()
		if cerr := scope.ChownToUser(target, docUID, wwwGID); cerr != nil {
			warns = append(warns, fmt.Sprintf("chown %q: %v", f.RelPath, cerr))
		}
	}
	return warns
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

// rootLocationRE matches a `location /` block opening for any nginx
// modifier where the URI is exactly "/" (a single slash, not "/foo").
// Used by writeVhost to detect when custom directives override the
// default root location so the template can omit its own (duplicate
// `location /` is an nginx parse error). Supports:
//
//	location /             { ... }   prefix match
//	location = /           { ... }   exact match
//	location ^~ /          { ... }   preferential prefix
//	location ~ /           { ... }   regex (rare but legal)
//	location ~* /          { ... }   case-insensitive regex
//
// Anchored to a line boundary so a comment line containing the literal
// text doesn't fire a false positive.
var rootLocationRE = regexp.MustCompile(`(?m)(?:^|\n)\s*location\s+(?:(?:=|\^~|~\*?)\s+)?/\s*\{`)

// customDirectivesOverrideRoot reports whether s declares its own
// `location /` block. When true, writeVhost passes RootOverridden=true
// to the template and the default `location /` + `location ~ \.php$`
// blocks are omitted from the rendered vhost so the operator's custom
// routing wins. Empty string and whitespace-only strings return false.
func customDirectivesOverrideRoot(s string) bool {
	if strings.TrimSpace(s) == "" {
		return false
	}
	return rootLocationRE.MatchString(s)
}

// directivesOverrideRoot reports whether any of the supplied directive
// blocks declares its own `location /`. Used by writeVhost to combine
// the raw custom-directives textarea AND the compiled nginx_rules
// (Rule Builder) into a single override signal. Either source can
// produce a `location /` block (rule type=proxy_pass + path="/" is the
// common Rule Builder case), and missing either source means a silent
// "duplicate location /" emerg the next time nginx -t runs.
func directivesOverrideRoot(blocks ...string) bool {
	for _, b := range blocks {
		if customDirectivesOverrideRoot(b) {
			return true
		}
	}
	return false
}
