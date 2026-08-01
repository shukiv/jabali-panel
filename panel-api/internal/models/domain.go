package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// PageRedirect is one entry in a domain's page_redirects JSON array.
// Source is a path starting with "/"; Destination is a full URL; Type
// is a redirect HTTP status code as a short string ("301"/"302"/"307"/"308").
// Active is optional; nil means true (backwards compatibility for rows stored without this field).
// Wildcard matches all paths starting with Source (prefix match with captured remainder).
// NOTE: PageRedirects is stored as JSON in a DB column. No schema migration needed —
// GORM serializes the full struct. When rows stored without Active field are Unmarshaled,
// Active becomes nil and must be treated as true for backwards compatibility.
type PageRedirect struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Type        string `json:"type"`
	Active      *bool  `json:"active,omitempty"`
	Wildcard    bool   `json:"wildcard,omitempty"`
}

// PageRedirects implements driver.Valuer / sql.Scanner so GORM can
// persist it to a JSON column. An empty slice round-trips as SQL NULL
// (keeps the column genuinely empty, not a literal "[]").
type PageRedirects []PageRedirect

func (p PageRedirects) Value() (driver.Value, error) {
	if len(p) == 0 {
		return nil, nil
	}
	return json.Marshal(p)
}

func (p *PageRedirects) Scan(src any) error {
	if src == nil {
		*p = nil
		return nil
	}
	var b []byte
	switch v := src.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return fmt.Errorf("PageRedirects.Scan: unsupported type %T", src)
	}
	if len(b) == 0 {
		*p = nil
		return nil
	}
	return json.Unmarshal(b, p)
}

// NginxRule is one entry in a domain's nginx_rules JSON array. Rules
// are discriminated by the Type field; each type consumes a different
// subset of the remaining fields. Keeping a single flat struct (vs
// interface/any) avoids an unmarshalling foot-gun and matches how
// PageRedirect is structured.
type NginxRule struct {
	Type string `json:"type"`

	// custom_header
	Name   string `json:"name,omitempty"`
	Value  string `json:"value,omitempty"`
	Always *bool  `json:"always,omitempty"`

	// rewrite
	Pattern     string `json:"pattern,omitempty"`
	Replacement string `json:"replacement,omitempty"`
	Flag        string `json:"flag,omitempty"` // last|break|redirect|permanent

	// proxy_pass
	Target string `json:"target,omitempty"`
	// proxy_pass: when true, emit the three WebSocket upgrade headers
	// (`proxy_http_version 1.1`, `Upgrade $http_upgrade`,
	// `Connection "upgrade"`). Off by default.
	Websocket *bool `json:"websocket,omitempty"`
	// proxy_pass: read-timeout for the upstream response. Accepts the
	// usual nginx duration suffixes (e.g. "60s", "86400s", "24h").
	// Empty leaves nginx's default (60s).
	ReadTimeout string `json:"read_timeout,omitempty"`

	// proxy_pass, ip_access
	Path string `json:"path,omitempty"`

	// ip_access
	Mode string   `json:"mode,omitempty"` // allow_list | deny_list
	IPs  []string `json:"ips,omitempty"`

	// max_upload_size
	Size string `json:"size,omitempty"`
}

// NginxRules implements driver.Valuer / sql.Scanner so GORM can
// persist it to a JSON column. An empty slice round-trips as SQL NULL
// (keeps the column genuinely empty, not a literal "[]").
type NginxRules []NginxRule

func (n NginxRules) Value() (driver.Value, error) {
	if len(n) == 0 {
		return nil, nil
	}
	return json.Marshal(n)
}

func (n *NginxRules) Scan(src any) error {
	if src == nil {
		*n = nil
		return nil
	}
	var b []byte
	switch v := src.(type) {
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return fmt.Errorf("NginxRules.Scan: unsupported type %T", src)
	}
	if len(b) == 0 {
		*n = nil
		return nil
	}
	return json.Unmarshal(b, n)
}

// Domain represents a hosted domain bound to a user account. Each domain
// gets an nginx vhost config managed by the agent and a document root
// under the user's home directory.
// SSL certificate modes (GH #246).
const (
	SSLModeLE     = "le"
	SSLModeSelf   = "self"
	SSLModeCustom = "custom"
	SSLModeNone   = "none"
	// SSLModeShared: a shared wildcard/multi-SAN cert (JAB-170) attached via
	// domains.shared_certificate_id. Operator-managed like custom (no ACME).
	SSLModeShared = "shared"
)

// ValidSSLMode reports whether s is a recognised TLS cert mode.
func ValidSSLMode(s string) bool {
	switch s {
	case SSLModeLE, SSLModeSelf, SSLModeCustom, SSLModeNone, SSLModeShared:
		return true
	}
	return false
}

// SSLEnabledForMode derives the legacy ssl_enabled shadow from the mode:
// TLS is "on" for every mode except none.
func SSLEnabledForMode(mode string) bool { return mode != SSLModeNone }

type Domain struct {
	ID     string `gorm:"type:char(26);primaryKey" json:"id"`
	UserID string `gorm:"type:char(26);not null;index:ix_domains_user_id" json:"user_id"`

	// Name is the fully qualified domain name (e.g. "example.com").
	// Unique across the entire panel — two users can't host the same domain.
	Name string `gorm:"type:varchar(255);uniqueIndex:ux_domains_name;not null" json:"name"`

	// DocRoot is the filesystem path served by nginx. Defaults to
	// /home/<username>/public_html/<domain> at creation time.
	DocRoot string `gorm:"type:varchar(512);not null;default:''" json:"doc_root"`

	// IsEnabled controls whether the nginx vhost symlink exists in
	// sites-enabled. Disabled domains still have their config on disk.
	IsEnabled bool `gorm:"type:tinyint(1);not null;default:1" json:"is_enabled"`

	// IsQuotaSuspended marks domains the M13.1.1 bandwidth reconciler
	// disabled because their owning user crossed BandwidthQuotaMB.
	// Disambiguates panel-driven disables from manual operator
	// disables — only suspended-by-quota domains get auto-restored
	// when usage drops back below the warn threshold.
	IsQuotaSuspended bool `gorm:"column:is_quota_suspended;type:tinyint(1);not null;default:0;index:idx_domains_quota_suspended" json:"is_quota_suspended"`

	// NginxCustomDirectives holds operator-provided nginx config snippets
	// injected into the server block. Validated before save — dangerous
	// directives (proxy_pass, lua_*) are rejected.
	NginxCustomDirectives *string `gorm:"type:text" json:"nginx_custom_directives,omitempty"`

	// RedirectAllTo, when non-nil, is a URL that every request to this
	// domain is redirected to (whole-domain redirect). Supersedes the
	// docroot and any PageRedirects. Must be an absolute http(s) URL.
	RedirectAllTo *string `gorm:"type:varchar(2048)" json:"redirect_all_to,omitempty"`

	// RedirectAllType is the HTTP status code for RedirectAllTo, as a
	// short string: "301", "302", "307", or "308". Required when
	// RedirectAllTo is set. Defaults to "301" when writing from the UI.
	RedirectAllType *string `gorm:"type:varchar(8)" json:"redirect_all_type,omitempty"`

	// PageRedirects is a JSON array of per-path redirects applied when
	// RedirectAllTo is nil. Each entry has a source path, destination
	// URL, and HTTP status code. Supports Active toggle (nil = true for
	// backwards compat) and Wildcard prefix matching (v2 feature).
	PageRedirects PageRedirects `gorm:"type:json" json:"page_redirects,omitempty"`

	// NginxRules is a JSON array of typed rule-builder entries that
	// compile to nginx directives server-side (see internal/nginxrules).
	// Separate from NginxCustomDirectives (which holds raw user snippets)
	// and from PageRedirects (which has its own dedicated UI).
	NginxRules NginxRules `gorm:"type:json" json:"nginx_rules,omitempty"`

	// NginxSafeOptions is a curated, owner-settable set of vetted nginx options
	// (GH #307) — rendered to fixed safe directives by the panel. Gated by
	// server_settings.tenant_domain_options_enabled for non-admin owners.
	NginxSafeOptions NginxSafeOptions `gorm:"column:nginx_safe_options;type:longtext" json:"nginx_safe_options"`

	// IndexPriority picks which file(s) nginx serves as the default
	// directory index. Enum values (html_first/php_first/html_only/
	// php_only/full) map to nginx `index` directives in the agent.
	IndexPriority string `gorm:"type:varchar(32);not null;default:'html_first'" json:"index_priority"`

	// SSLEnabled tracks whether ACME certificate provisioning is active for this domain.
	// When true, the reconciler attempts to issue or renew a cert; when false,
	// the cert (if any) is not updated but may remain installed.
	SSLEnabled bool `gorm:"type:tinyint(1);not null;default:1" json:"ssl_enabled"`

	// SSLMode (GH #246, migration 000179) is the authoritative per-domain TLS
	// cert mode: 'le' (ACME + self-signed fallback), 'self' (self-signed only),
	// 'custom' (operator-supplied cert/key, no auto-renew), 'none' (HTTP only).
	// SSLEnabled is a derived shadow (mode != 'none') kept in sync on every
	// write; the reconciler switches on SSLMode. Pinned column tag to match
	// the other toggles.
	SSLMode string `gorm:"column:ssl_mode;type:enum('le','self','custom','none','shared');not null;default:'le'" json:"ssl_mode"`

	// SharedCertificateID attaches this domain to a shared wildcard/multi-SAN
	// cert (JAB-170) when ssl_mode='shared'; NULL otherwise. FK →
	// shared_certificates(id) ON DELETE SET NULL.
	SharedCertificateID *string `gorm:"column:shared_certificate_id;type:char(26)" json:"shared_certificate_id,omitempty"`

	// SSLState is a computed field (not persisted) that represents the actual SSL
	// certificate state. Values: "active_le" (valid LE cert), "self_signed", "pending",
	// "failed", "off". Populated by the repository on List/ListByUserID.
	SSLState string `gorm:"-" json:"ssl_state,omitempty"`

	// PHPPoolID optionally binds this domain to a PHP-FPM pool. When set, the
	// domain can execute PHP. When NULL, the domain is static (no PHP block in vhost).
	PHPPoolID *string `gorm:"type:char(26)" json:"php_pool_id,omitempty"`

	// PHP INI overrides: per-domain settings that override the pool default.
	// NULL means use the pool's default; empty string is invalid (disallowed by API validation).
	// These are rendered as fastcgi_param PHP_VALUE at the nginx layer.
	PHPMemoryLimit       *string `gorm:"type:varchar(16)" json:"php_memory_limit,omitempty"`
	PHPUploadMaxFilesize *string `gorm:"type:varchar(16)" json:"php_upload_max_filesize,omitempty"`
	PHPPostMaxSize       *string `gorm:"type:varchar(16)" json:"php_post_max_size,omitempty"`
	PHPMaxInputVars      *int    `gorm:"type:int unsigned" json:"php_max_input_vars,omitempty"`
	PHPMaxExecutionTime  *int    `gorm:"type:int unsigned" json:"php_max_execution_time,omitempty"`
	PHPMaxInputTime      *int    `gorm:"type:int unsigned" json:"php_max_input_time,omitempty"`

	// M18: per-domain HTTP rate/conn limits. Zero = unlimited (no
	// nginx directive emitted). RateLimitRPS is requests-per-SECOND
	// as seen by the reconciler; the vhost renderer converts to
	// nginx's native rate syntax (r/s or r/m) at render time and
	// uses `burst=rate*2 nodelay` to absorb short spikes.
	RateLimitRPS    uint32 `gorm:"type:int unsigned;not null;default:0" json:"rate_limit_rps"`
	ConnectionLimit uint32 `gorm:"type:int unsigned;not null;default:0" json:"connection_limit"`

	// M24: optional binding to specific IPv4/IPv6 in the managed_ips
	// pool. NULL means "use server primary" (managed_ips.is_default for
	// the family). FK enforced at DB level with ON DELETE RESTRICT;
	// the API translates restrict-violations into 409 with the affected
	// domains list. Pointers because nullable; uint64 because the
	// referenced managed_ips.id is BIGINT UNSIGNED auto-increment.
	ListenIPv4ID *uint64 `gorm:"column:listen_ipv4_id;type:bigint unsigned" json:"listen_ipv4_id,omitempty"`
	ListenIPv6ID *uint64 `gorm:"column:listen_ipv6_id;type:bigint unsigned" json:"listen_ipv6_id,omitempty"`

	// M6 email state (migration 000054). EmailEnabled drives whether the
	// reconciler sets up MX/SPF/DMARC zone records and whether the API
	// accepts mailbox creates. DkimSelector + DkimPublicKey mirror the
	// DNS TXT value published by the agent's domain.email_enable; the
	// private key stays on disk at /etc/jabali-panel/dkim/<name>.key
	// (ADR-0043). EmailEnabledAt is the last transition-to-enabled
	// timestamp — useful for operator audit and the reconciler to
	// re-publish DNS after a backup restore.
	//
	// No gorm `default:1` tag: GORM substitutes a defaulted field's
	// DB default for a Go zero value on insert (even under Select), so
	// `default:1` flipped an intentional EmailEnabled=false (mail_provider
	// none/m365/google → DeriveMailFlags returns false) to email_enabled=1
	// on create. The reconciler then read it as mail-on and backfilled the
	// mail.<domain> cert + mail DNS for "No Mail" domains (GH #216; same
	// GORM-zero-value scar as the cron default:1 fix). The DB column keeps
	// its `DEFAULT 1` from migration 000123 for any non-API insert; the
	// API always sets this field explicitly via DeriveMailFlags.
	EmailEnabled bool `gorm:"type:tinyint(1);not null" json:"email_enabled"`
	// WebmailEnabled (GH #316) gates the per-domain Bulwark webmail vhost,
	// AND-ed with server_settings.webmail_enabled. Default ON.
	WebmailEnabled bool `gorm:"column:webmail_enabled;type:tinyint(1);not null;default:1" json:"webmail_enabled"`
	// SkipAutoSAN opts the domain out of ADR-0070 auto-added mail/
	// autoconfig SAN entries on the LE cert. Set when the tenant runs
	// mail elsewhere (or has no mail subdomain DNS) — without this the
	// LE challenge fails for the missing subdomain and the WHOLE cert
	// stays pending.
	SkipAutoSAN bool `gorm:"column:skip_auto_san;type:tinyint(1);not null;default:0" json:"skip_auto_san"`
	// MailProvider (GH#181 / ADR-0120, migration 000166) is the single
	// source of truth for where this domain's mail lives. EmailEnabled +
	// SkipAutoSAN are DERIVED from it on create/edit (see
	// DeriveMailFlags). Values: "jabali" (default) | "none" | "m365" |
	// "google". External/none providers set EmailEnabled=false +
	// SkipAutoSAN=true and publish the provider's own DNS records.
	MailProvider string `gorm:"column:mail_provider;type:varchar(16);not null;default:'jabali'" json:"mail_provider"`
	// M365Onmicrosoft / GoogleDKIM are optional per-provider DKIM tokens
	// the operator pastes from the provider's admin console. Empty ->
	// the provider's DKIM records are simply not published.
	M365Onmicrosoft *string    `gorm:"column:m365_onmicrosoft;type:varchar(255)" json:"m365_onmicrosoft,omitempty"`
	GoogleDKIM      *string    `gorm:"column:google_dkim;type:text" json:"google_dkim,omitempty"`
	DkimSelector    *string    `gorm:"type:varchar(64)" json:"dkim_selector,omitempty"`
	DkimPublicKey   *string    `gorm:"type:text" json:"dkim_public_key,omitempty"`
	EmailEnabledAt  *time.Time `gorm:"type:datetime(6)" json:"email_enabled_at,omitempty"`

	// GH #648 (DMARCbis): operator-settable DMARC tags the reconciler folds
	// into the canonical _dmarc record (jabali-mail domains only). DmarcNP is
	// the non-existent-subdomain policy ("", none, quarantine, reject; empty =
	// omit np -> receiver falls back to sp). DmarcTesting adds t=y.
	DmarcNP      string `gorm:"column:dmarc_np;type:varchar(12);not null;default:''" json:"dmarc_np"`
	DmarcTesting bool   `gorm:"column:dmarc_testing;type:tinyint(1);not null;default:0" json:"dmarc_testing"`

	// IsPanelPrimary marks the single domain row auto-registered for the
	// panel hostname (ADR-0048). Delete-protected at the repo and API
	// layer; surfaced in Settings → Email. At-most-one is enforced in
	// the Go repo layer (MarkPanelPrimary transaction) — NOT a SQL
	// UNIQUE (see migration 000057 for the reasoning).
	IsPanelPrimary bool `gorm:"type:tinyint(1);not null;default:0;index:ix_domains_panel_primary" json:"is_panel_primary"`

	// M6.5 Email Features: Catch-All, Disclaimer (per-domain fields).
	// CatchallTarget is the email address that receives unmatched domain mail.
	// Stalwart integration: x:Domain.catchAllAddress (ADR-0051).
	CatchallTarget *string `gorm:"type:varchar(255)" json:"catchall_target,omitempty"`

	// DisclaimerEnabled controls whether to append a text disclaimer to
	// outbound mail from this domain. DisclaimerText holds the text.
	// Stalwart integration: x:SieveSystemScript (ADR-0051).
	DisclaimerEnabled bool    `gorm:"type:tinyint(1);not null;default:0" json:"disclaimer_enabled"`
	DisclaimerText    *string `gorm:"type:text" json:"disclaimer_text,omitempty"`

	// DNSSEC: operator intent + enable timestamp (ADR-0076). Actual signing
	// state lives in PowerDNS; key cache in domain_dnssec_keys.
	// Pin the column names — GORM's default snake_case derivation turns
	// DNSSECEnabled into `dns_sec_enabled` (splits on every uppercase run),
	// but migration 000070 creates the column as `dnssec_enabled`. Without
	// the explicit `column:` tag every INSERT fails with "Unknown column
	// 'dns_sec_enabled'". Mirrors the CrowdSec pattern.
	DNSSECEnabled   bool       `gorm:"column:dnssec_enabled;type:tinyint(1);not null;default:0" json:"dnssec_enabled"`
	DNSSECEnabledAt *time.Time `gorm:"column:dnssec_enabled_at;type:datetime(6)" json:"dnssec_enabled_at,omitempty"`

	// RegistrarExpiry (GH #259): the domain's registration expiry, populated by
	// the scheduled WHOIS-fetch ticker (StartDomainExpiryTicker). checked_at
	// gates the refresh cadence and is set even when the lookup yields no date
	// (unparseable / no whois) so we don't re-query every tick.
	RegistrarExpiresAt *time.Time `gorm:"column:registrar_expires_at;type:datetime(6)" json:"registrar_expires_at,omitempty"`
	RegistrarCheckedAt *time.Time `gorm:"column:registrar_checked_at;type:datetime(6)" json:"registrar_checked_at,omitempty"`

	// CacheEnabled (migration 000140, ADR-0108) is the per-domain
	// opt-in nginx FastCGI micro-cache switch. Off by default; the
	// reconciler passes it into domain.create and the agent renders
	// the cache + bypass directives into the vhost. Pinned column tag:
	// the GORM initialism-splitter would otherwise be fine here, but
	// we pin it to match the DNSSEC/SSL toggles and the
	// gorm-column-tags scar.
	CacheEnabled bool `gorm:"column:cache_enabled;type:tinyint(1);not null;default:0" json:"cache_enabled"`
	// CachePath (migration 000191, Gitea #420) scopes the page cache to the WP
	// install path prefix; "/" = whole domain (default).
	CachePath string `gorm:"column:cache_path;type:varchar(255);not null;default:'/'" json:"cache_path"`
	// CacheTTLSeconds (migration 000204, Gitea #596) is the per-domain page-cache
	// validity in seconds. Default 600; the agent clamps to [10, 86400].
	CacheTTLSeconds int `gorm:"column:cache_ttl_seconds;type:int;not null;default:600" json:"cache_ttl_seconds"`
	// CacheQueryAllowlist (migration 000230) is the comma-joined, lower-cased,
	// sorted set of query-param names that get their own page-cache entry
	// (e.g. "paged" for pagination). Empty = feature off (default): every real
	// query param bypasses, exactly as before. See CacheQueryAllowlistNames.
	CacheQueryAllowlist string `gorm:"column:cache_query_allowlist;type:varchar(255);not null;default:''" json:"cache_query_allowlist"`

	// MTA-STS per-domain opt-in (migration 000141, ADR-0109). When
	// flipped on, the reconciler ensures:
	//   - /var/www/jabali-mta-sts/<domain>/.well-known/mta-sts.txt
	//     (the policy file nginx serves)
	//   - /etc/nginx/sites-available/<domain>-mta-sts.conf vhost
	//   - mta-sts.<domain> SAN on the domain's TLS cert
	//   - mta-sts.<domain> A + _mta-sts.<domain> TXT DNS records
	// MTASTSId is the policy version cookie embedded in the TXT
	// record ("v=STSv1; id=<this>"). Bumped on every enable / mode
	// change so receivers invalidate cached policies. Column tags
	// pinned to match the DNSSEC/Cache toggles.
	MTASTSEnabled bool   `gorm:"column:mta_sts_enabled;type:tinyint(1);not null;default:0" json:"mta_sts_enabled"`
	MTASTSId      uint64 `gorm:"column:mta_sts_id;type:bigint unsigned;not null;default:0" json:"mta_sts_id"`
	// MTASTSAppliedId tracks the policy version last ACKed by
	// mail.mtasts.apply (M47 Wave 7c). The reconciler dispatches the
	// agent call whenever applied_id != MTASTSId so the vhost reload
	// happens at most once per id rotation. Migration 000142.
	MTASTSAppliedId uint64 `gorm:"column:mta_sts_applied_id;type:bigint unsigned;not null;default:0" json:"mta_sts_applied_id"`

	// M38 Ghost Domain Detector — periodic DNS-alignment state.
	// GhostState is one of: unchecked / ok / mismatch / nxdomain / error.
	// GhostCheckedAt is the last detector-pass timestamp; NULL for
	// rows the detector hasn't seen yet. GhostDetail is a short
	// human-readable explanation surfaced in the admin badge tooltip
	// and the M14 notification body.
	GhostState     string     `gorm:"column:ghost_state;type:enum('unchecked','ok','mismatch','nxdomain','error');not null;default:'unchecked'" json:"ghost_state"`
	GhostCheckedAt *time.Time `gorm:"column:ghost_checked_at;type:datetime(0)" json:"ghost_checked_at,omitempty"`
	GhostDetail    *string    `gorm:"column:ghost_detail;type:varchar(255)" json:"ghost_detail,omitempty"`

	// M48 Phase 1 (ADR-0116 Decision 4): tag rows the docker-app
	// subsystem auto-creates so tenant /domains handlers and the
	// docker-app handlers don't fight over the same row.
	//
	// managed_by values:
	//   tenant     — created via the existing /domains handler (default).
	//   docker_app — created by the docker-app installer; auto-managed.
	//
	// DockerAppID back-references docker_apps.id when ManagedBy='docker_app'.
	// Reconciler uses this to render a proxy_pass vhost to the app's
	// loopback port (resolved through docker_app_published_ports for
	// reverse_proxy=true rows).
	ManagedBy   string  `gorm:"column:managed_by;type:varchar(32);not null;default:'tenant';index:ix_domains_managed_by" json:"managed_by"`
	DockerAppID *string `gorm:"column:docker_app_id;type:char(26);null" json:"docker_app_id,omitempty"`

	// CreateWWW (GH #225, migration 000176) is the per-domain opt-in for
	// the bootstrap `www` CNAME. Unchecked by default in the create form;
	// especially useful for subdomain entries that don't want a
	// www.<sub>.<domain> alias. Read by the reconciler when it bootstraps
	// the zone (BootstrapRecords). No gorm `default` tag on purpose: GORM
	// elides a defaulted false bool from the INSERT and lets the DB default
	// kick in (the EmailEnabled scar) — the API always sets this field
	// explicitly, so we want the real value on the wire. The DB column
	// keeps DEFAULT 1 for any non-API insert (docker-app etc.).
	CreateWWW bool `gorm:"column:create_www;type:tinyint(1);not null" json:"create_www"`

	// TempURLEnabled (migration 000243) is the per-domain opt-in for a
	// preview URL: <slug>.preview.<hostname> serving the same docroot, so
	// the site is reachable before the real domain's DNS points here.
	// Opt-in (default 0 — zero value, so the GORM default-tag insert trap
	// from the scar list cannot bite).
	TempURLEnabled bool `gorm:"column:temp_url_enabled;type:tinyint(1);not null" json:"temp_url_enabled"`

	CreatedAt time.Time `gorm:"type:datetime(6);not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"type:datetime(6);not null" json:"updated_at"`
}

// Domain.ManagedBy values.
const (
	DomainManagedByTenant    = "tenant"
	DomainManagedByDockerApp = "docker_app"
)

func (Domain) TableName() string { return "domains" }

// CacheQueryAllowlistNames splits the stored comma-joined allowlist into
// individual param names, dropping blanks. The stored value is already
// normalized (lower-cased, de-duped, sorted) by the API on write, so this is a
// plain split; nil/empty when the feature is off.
func (d Domain) CacheQueryAllowlistNames() []string {
	raw := strings.TrimSpace(d.CacheQueryAllowlist)
	if raw == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
