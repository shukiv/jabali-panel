package models

import (
	"encoding/json"
	"time"
)

// ServerSettings is the single-row table holding server identity and
// DNS configuration. Operators edit these from the admin Settings page;
// at first boot the row is seeded from the installer's config.toml.
type ServerSettings struct {
	ID       uint8  `gorm:"primaryKey;default:1"                json:"id"`
	Hostname string `gorm:"type:varchar(253);not null;default:''" json:"hostname"`
	// PreviewBase (GH #836, migration 000244) overrides the derived
	// preview-URL base. Empty = preview.<hostname>. Set a custom domain
	// (preview.example.com) or a magic-DNS base (203-0-113-7.sslip.io)
	// when the hostname does not resolve publicly.
	PreviewBase string `gorm:"type:varchar(253);not null;default:''" json:"preview_base"`
	PublicIPv4  string `gorm:"type:varchar(45);not null;default:''"  json:"public_ipv4"`
	PublicIPv6  string `gorm:"type:varchar(45);not null;default:''"  json:"public_ipv6"`
	NS1Name     string `gorm:"type:varchar(253);not null;default:''" json:"ns1_name"`
	NS1IPv4     string `gorm:"type:varchar(45);not null;default:''"  json:"ns1_ipv4"`
	NS2Name     string `gorm:"type:varchar(253);not null;default:''" json:"ns2_name"`
	NS2IPv4     string `gorm:"type:varchar(45);not null;default:''"  json:"ns2_ipv4"`
	AdminEmail  string `gorm:"type:varchar(320);not null;default:''" json:"admin_email"`
	// DefaultDNSTTL is the TTL (seconds) applied to newly-created DNS
	// records when the API caller doesn't pass one. Editable via
	// Server Settings → DNS in the admin UI. Range 60–86400 enforced
	// at the API layer; column default 3600 matches the pre-2026-06
	// hardcoded value so existing installs see no behaviour change.
	DefaultDNSTTL uint32 `gorm:"column:default_dns_ttl;type:int unsigned;not null;default:300" json:"default_dns_ttl"`
	// DefaultPHPVersion is the PHP version new user pools are seeded with
	// (reconciler default-pool path) and the version the admin UI pre-selects.
	// Admin changes it via POST /admin/php/versions/:version/default; agent
	// php.version.status reports it in default_version. Schema default '8.4'.
	DefaultPHPVersion string `gorm:"type:varchar(8);not null;default:'8.4'" json:"default_php_version"`
	Timezone          string `gorm:"type:varchar(64);not null;default:''"   json:"timezone"`
	SSHPort           uint16 `gorm:"type:int unsigned;not null;default:22"  json:"ssh_port"`
	// SSHPasswordAuth governs the GLOBAL PasswordAuthentication directive
	// in /etc/ssh/sshd_config.d/jabali-sshd.conf. In practice this only
	// affects users NOT in the jabali-sftp group (root, admin shell users)
	// because the M12 Match Group jabali-sftp block in jabali-sftp.conf
	// overrides the global for hosting users. Use SSHUserPasswordAuth to
	// flip the per-group setting.
	SSHPasswordAuth bool `gorm:"type:tinyint(1);not null;default:0"     json:"ssh_password_auth"`
	// SSHUserPasswordAuth governs PasswordAuthentication inside the
	// Match Group jabali-sftp block — i.e. for hosting users. The agent's
	// system.set_ssh_config rewrites jabali-sftp.conf to honor this flag.
	// Note: ForceCommand internal-sftp still applies, so password-authed
	// hosting users land in SFTP, not a shell — per-package shell access
	// is a separate, future feature.
	SSHUserPasswordAuth bool `gorm:"type:tinyint(1);not null;default:0"     json:"ssh_user_password_auth"`

	// VAPIDPublicKey / VAPIDPrivateKey / VAPIDSubject hold the Web Push
	// keypair + subject mint. See ADR-0057. Generated on first boot by
	// ServerSettingsRepository.EnsureVAPID (called from serve.go seed
	// goroutine), NOT by the migration — per
	// feedback_migration_data_seed_ordering. All three columns are
	// nullable to represent "not yet seeded" on fresh boot; once
	// populated they remain stable until an explicit operator rotation
	// (deferred to a future milestone).
	//
	// json:"-" keeps the private_key out of the server_settings GET
	// response served to the admin UI. The public_key and subject are
	// exposed through a dedicated /api/v1/admin/vapid/public endpoint
	// in Step 5 (so the SPA can register the service worker) rather
	// than leaking through the generic settings endpoint.
	VAPIDPublicKey  *string `gorm:"type:varchar(128);column:vapid_public_key"  json:"-"`
	VAPIDPrivateKey *string `gorm:"type:varchar(64);column:vapid_private_key"  json:"-"`
	VAPIDSubject    *string `gorm:"type:varchar(320);column:vapid_subject"     json:"-"`

	// M28 Panel Branding. PanelBrandText is the short label next to
	// the logo and in the browser title. LogoLightPath / LogoDarkPath
	// hold absolute on-disk paths to operator-uploaded logo files
	// under /var/lib/jabali-panel/branding/; empty string means "no
	// logo uploaded — fall back to the built-in default".
	PanelBrandText string `gorm:"column:panel_brand_text;type:varchar(60);not null;default:''" json:"panel_brand_text"`
	LogoLightPath  string `gorm:"column:logo_light_path;type:varchar(255);not null;default:''" json:"logo_light_path"`
	LogoDarkPath   string `gorm:"column:logo_dark_path;type:varchar(255);not null;default:''"  json:"logo_dark_path"`

	// #433 panel font-size: "small" | "medium" | "large" (medium = default).
	PanelFontSize string `gorm:"column:panel_font_size;type:varchar(10);not null;default:'medium'" json:"panel_font_size"`

	// "Look and feel" panel colors. Empty string = built-in default. Hex
	// (#rgb / #rrggbb / #rrggbbaa). PrimaryColor drives the antd colorPrimary
	// token; AccentColor drives the selected sidebar-row / active-tab accent.
	PanelPrimaryColor string `gorm:"column:panel_primary_color;type:varchar(9);not null;default:''" json:"panel_primary_color"`
	PanelAccentColor  string `gorm:"column:panel_accent_color;type:varchar(9);not null;default:''"  json:"panel_accent_color"`
	PanelSuccessColor string `gorm:"column:panel_success_color;type:varchar(9);not null;default:''" json:"panel_success_color"`
	PanelWarningColor string `gorm:"column:panel_warning_color;type:varchar(9);not null;default:''" json:"panel_warning_color"`
	PanelErrorColor   string `gorm:"column:panel_error_color;type:varchar(9);not null;default:''"   json:"panel_error_color"`
	PanelInfoColor    string `gorm:"column:panel_info_color;type:varchar(9);not null;default:''"    json:"panel_info_color"`
	PanelLinkColor    string `gorm:"column:panel_link_color;type:varchar(9);not null;default:''"    json:"panel_link_color"`
	// Per-theme chrome colors (GH #435): separate light+dark values for the
	// top bar, page/sidebar bg, card/container bg, and text. Empty = default.
	PanelLightTopbarColor    string `gorm:"column:panel_light_topbar_color;type:varchar(9);not null;default:''" json:"panel_light_topbar_color"`
	PanelDarkTopbarColor     string `gorm:"column:panel_dark_topbar_color;type:varchar(9);not null;default:''" json:"panel_dark_topbar_color"`
	PanelLightBgColor        string `gorm:"column:panel_light_bg_color;type:varchar(9);not null;default:''" json:"panel_light_bg_color"`
	PanelDarkBgColor         string `gorm:"column:panel_dark_bg_color;type:varchar(9);not null;default:''" json:"panel_dark_bg_color"`
	PanelLightContainerColor string `gorm:"column:panel_light_container_color;type:varchar(9);not null;default:''" json:"panel_light_container_color"`
	PanelDarkContainerColor  string `gorm:"column:panel_dark_container_color;type:varchar(9);not null;default:''" json:"panel_dark_container_color"`
	PanelLightTextColor      string `gorm:"column:panel_light_text_color;type:varchar(9);not null;default:''" json:"panel_light_text_color"`
	PanelDarkTextColor       string `gorm:"column:panel_dark_text_color;type:varchar(9);not null;default:''" json:"panel_dark_text_color"`
	// ReleaseChannel (GH #445): "stable" (track the promoted `stable` tag) or
	// "development" (track main). Default stable.
	ReleaseChannel string `gorm:"column:release_channel;type:varchar(16);not null;default:'stable'" json:"release_channel"`
	// WebmailEnabled toggles the Bulwark webmail client server-wide (GH #316).
	WebmailEnabled bool `gorm:"column:webmail_enabled;type:tinyint(1);not null;default:1" json:"webmail_enabled"`
	// Module enable flags (M353 Phase 1, GH #353): each gates its install +
	// its panel pages. Default on so upgrades keep every feature; fresh
	// installs seed these from JABALI_MODULES at first boot.
	DNSEnabled      bool `gorm:"column:dns_enabled;type:tinyint(1);not null;default:1" json:"dns_enabled"`
	MailEnabled     bool `gorm:"column:mail_enabled;type:tinyint(1);not null;default:1" json:"mail_enabled"`
	SecurityEnabled bool `gorm:"column:security_enabled;type:tinyint(1);not null;default:1" json:"security_enabled"`
	QuotaEnabled    bool `gorm:"column:quota_enabled;type:tinyint(1);not null;default:1" json:"quota_enabled"`
	APIEnabled      bool `gorm:"column:api_enabled;type:tinyint(1);not null;default:1" json:"api_enabled"`
	// TenantDomainOptionsEnabled opts non-admin owners into the curated safe
	// nginx domain options (GH #307). Default off.
	TenantDomainOptionsEnabled bool `gorm:"column:tenant_domain_options_enabled;type:tinyint(1);not null;default:0" json:"tenant_domain_options_enabled"`

	// DigestLastSentDate is the UTC day (YYYY-MM-DD) the daily digest last
	// fired for (GH #840). Written by the digest ticker via
	// SetDigestLastSent; empty = never sent.
	DigestLastSentDate string `gorm:"column:digest_last_sent_date;type:varchar(10);not null;default:''" json:"digest_last_sent_date"`

	// DKIM2SigningEnabled adds a DKIM2 chain-of-custody signature (GH #648,
	// draft-ietf-dkim-dkim2) alongside the classic DKIM signature on every
	// mail domain's outbound. Needs no DNS change — DKIM2 verifies against
	// the already-published DKIM1 TXT records by design. Default off while
	// the spec is a working-group draft; DKIM1 always keeps signing.
	DKIM2SigningEnabled bool `gorm:"column:dkim2_signing_enabled;type:tinyint(1);not null;default:0" json:"dkim2_signing_enabled"`

	// UnconfiguredPageEnabled serves the branded "domain not configured" page
	// (page_templates.domain_unconfigured) on the default catch-all vhost for
	// hosts nginx has no server block for (GH #860). Default off: the shipped
	// behaviour is `return 444` — drop without a response — so scanners can't
	// fingerprint the box from an unknown Host. Flipping this on is a
	// deliberate trade of that stealth for a branded operator page.
	UnconfiguredPageEnabled bool `gorm:"column:unconfigured_page_enabled;type:tinyint(1);not null;default:0" json:"unconfigured_page_enabled"`

	// AutomationApiPublicEnabled (GH #1161) also serves the HMAC-gated
	// /api/v1/automation/ tree on the standard :443 port, not just :8443.
	// Default off: exposing the API on the public port is opt-in. Billing
	// hosts with a locked-down outbound firewall (CSF TCP_OUT omits 8443)
	// can't reach :8443; enabling this makes the agent write the nginx
	// include that adds the automation proxy to the panel-hostname :443
	// vhost. Only /api/v1/automation/ (all RequireAutomationHMAC) is
	// exposed — the internal/unauthenticated endpoints stay :8443-only.
	AutomationApiPublicEnabled bool `gorm:"column:automation_api_public_enabled;type:tinyint(1);not null;default:0" json:"automation_api_public_enabled"`

	// InterceptAppErrorsDefault (GH #879) — server-wide default for the
	// branded application-error page: nginx serves the themed 500 when a
	// PHP app returns a 5xx (a fatal is a 500 with an empty body). Domains
	// can override either way via nginx_safe_options.intercept_errors
	// (nil = inherit this). Default off: apps that render their own error
	// screens (WordPress recovery notice, JSON APIs) keep them.
	InterceptAppErrorsDefault bool `gorm:"column:intercept_app_errors_default;type:tinyint(1);not null;default:0" json:"intercept_app_errors_default"`

	// TenantDocrootEditable lets a non-admin owner repoint their own domain's
	// document root within the domain's own tree (GH #526). Default ON — the
	// edit is confined to /home/<user>/domains/<domain>/, many apps (Laravel et
	// al.) need a subdir docroot, and peer panels expose it by default; admins
	// can still toggle it off. Decoupled from TenantDomainOptionsEnabled so it
	// stands apart from the riskier raw nginx options.
	TenantDocrootEditable bool `gorm:"column:tenant_docroot_editable;type:tinyint(1);not null;default:1" json:"tenant_docroot_editable"`

	// TenantNotificationsEnabled is the master switch for the tenant-facing
	// notification surface (/me/notifications/*, JAB-171). Default OFF and, in
	// phase 3b, there is NO admin API to flip it — the endpoints are built,
	// ownership-scoped and secret-redacting but stay un-enablable until phase 4
	// adds the SSRF guard, per-kind allowlist and email-verify. Handlers fail
	// CLOSED on this flag (unlike the module guard), so an unreadable settings
	// row keeps the surface off. Security-sensitive tenant cap → default OFF.
	TenantNotificationsEnabled bool `gorm:"column:tenant_notifications_enabled;type:tinyint(1);not null;default:0" json:"tenant_notifications_enabled"`

	// TenantNotificationKinds is the admin-configurable allowlist of channel
	// kinds a tenant may create (JAB-171 phase 4b). Empty column = safe default
	// set (ntfy/telegram/discord/webpush); webhook/slack/sms are admin opt-in.
	// Enforcement substitutes the default via OrDefault(); "deny all" is the
	// master TenantNotificationsEnabled switch, not an empty list.
	TenantNotificationKinds TenantNotificationKinds `gorm:"column:tenant_notification_kinds;type:longtext" json:"tenant_notification_kinds"`

	// DNSUserRecordPolicy — per-type create/edit/delete matrix for non-admin
	// tenants (GH #466, ADR-0150). JSON object keyed by UPPERCASE record type.
	DNSUserRecordPolicy DNSUserRecordPolicy `gorm:"column:dns_user_record_policy;type:longtext" json:"dns_user_record_policy"`

	// M26 AppSec geoblock (migration 000067). Server-wide rule applied
	// by crowdsec AppSec. Mode ∈ {"off", "allow", "deny"}:
	//   off   — rule file written with no filter; all traffic passes
	//   allow — requests from listed countries pass; everything else 403
	//   deny  — requests from listed countries 403; everything else passes
	// Countries is a comma-separated list of ISO 3166-1 alpha-2 codes.
	// "off" + empty is the default; toggling needs nginx AppSec wiring
	// (see plans/m26-security-tab-runbook.md).
	AppSecGeoblockMode      string `gorm:"column:appsec_geoblock_mode;type:varchar(10);not null;default:'off'"    json:"appsec_geoblock_mode"`
	AppSecGeoblockCountries string `gorm:"column:appsec_geoblock_countries;type:varchar(1000);not null;default:''" json:"appsec_geoblock_countries"`

	// Country ban exemption (ADR-0166, migration 000262). Countries whose
	// IPs must never be blocked by CrowdSec from any decision source
	// (scenario bans, AppSec inband, CAPI/console blocklists, captchas).
	// Comma-separated ISO 3166-1 alpha-2 codes; empty = feature off.
	// Applied by the agent via the s02-enrich GeoIP parser whitelist and
	// the jabali-country-allowlist LAPI AllowList (CIDR sets synced by
	// panel-api's countryexempt syncer). Wins over the geoblock deny-list
	// (AllowLists evaluate before AppSec pre_eval — ADR-0061).
	CountryExemptCountries  string `gorm:"column:country_exempt_countries;type:text;not null;default:''" json:"country_exempt_countries"`
	CountryExemptExtraCIDRs string `gorm:"column:country_exempt_extra_cidrs;type:text;not null;default:''" json:"country_exempt_extra_cidrs"`
	// CrowdsecSensitivity preset (M27 follow-up). Applied via agent's
	// security.crowdsec.sensitivity.apply verb which writes:
	//   /etc/crowdsec/profiles.yaml          (ban duration)
	//   /etc/crowdsec/scenarios/jabali-*     (override scenario capacity)
	//   /etc/crowdsec/appsec-configs/...     (anomaly threshold)
	// Default 'balanced' matches CrowdSec/CRS upstream defaults so an
	// admin who never visits the toggle gets pre-toggle behaviour.
	CrowdsecSensitivity string `gorm:"column:crowdsec_sensitivity;type:varchar(16);not null;default:'balanced'" json:"crowdsec_sensitivity"`

	// CrowdsecBouncerMode — server-wide nginx-bouncer MODE. Two values:
	//   live   per-request LAPI lookup; instant block but ~23% CPU
	//   stream in-process Lua cache, 60s poll; ~10% CPU, up-to-60s
	//          new-ban lag (L7 only; firewall bouncer already 60s)
	// Applied by the agent via security.crowdsec.bouncer.mode.apply.
	// Default 'stream' matches the install.sh shipped default.
	CrowdsecBouncerMode string `gorm:"column:crowdsec_bouncer_mode;type:varchar(16);not null;default:'stream'" json:"crowdsec_bouncer_mode"`

	// GH #598 — auto-allowlist successful panel + SSH login source IPs in
	// CrowdSec, time-boxed and refreshed on activity, so a logged-in admin/user
	// is never bounced from the IP they're actively working from. The panel
	// middleware (WhitelistLoginIP) reads these directly; the agent's SSH
	// journald watcher reads them via the pushed login-allowlist.conf. Enabled
	// by default (matches the pre-#598 shipped panel behaviour). TTL in hours,
	// default 168h (7d). SECURITY: this is a broad CrowdSec exemption — a login
	// from a stolen key shields that IP from all decisions for the TTL; admins
	// who want a tighter window lower the TTL or disable it (ADR-0153).
	CrowdsecLoginAllowlistEnabled  bool `gorm:"column:crowdsec_login_allowlist_enabled;type:boolean;not null;default:true" json:"crowdsec_login_allowlist_enabled"`
	CrowdsecLoginAllowlistTTLHours int  `gorm:"column:crowdsec_login_allowlist_ttl_hours;type:int;not null;default:168" json:"crowdsec_login_allowlist_ttl_hours"`

	// M27 Step 5 — captcha remediation for crowdsec-nginx-bouncer.
	// Secret is plaintext-at-rest (convention matches kratos_admin_secret,
	// vapid_private_key, smtp_relay_password) but WRITE-ONLY at the API
	// edge — GET responses NEVER include the secret.
	CrowdSecCaptchaEnabled   bool   `gorm:"column:crowdsec_captcha_enabled;type:boolean;not null;default:false"       json:"crowdsec_captcha_enabled"`
	CrowdSecCaptchaProvider  string `gorm:"column:crowdsec_captcha_provider;type:varchar(32);not null;default:''"     json:"crowdsec_captcha_provider"`
	CrowdSecCaptchaSiteKey   string `gorm:"column:crowdsec_captcha_site_key;type:varchar(512);not null;default:''"    json:"crowdsec_captcha_site_key"`
	CrowdSecCaptchaSecretKey string `gorm:"column:crowdsec_captcha_secret_key;type:varchar(512);not null;default:''"  json:"-"`

	// Global disk-quota toggle (migration 000071). When false (default),
	// the reconciler does not apply POSIX user quota and the Packages UI
	// disables the disk-quota fields. cgroups limits (cpu/memory/io/
	// tasks) are independent of this flag and always apply.
	//
	// Operator must independently mount /home on its own filesystem with
	// usrquota,grpquota options before flipping this true. install.sh
	// refuses to enable quota when /home == / (M18); this DB flag is the
	// runtime equivalent — flipping it true on an unprepared host means
	// agent quota.apply will fail and the panel will surface the error.
	DiskQuotaEnabled bool `gorm:"column:disk_quota_enabled;type:tinyint(1);not null;default:0" json:"disk_quota_enabled"`

	// Bandwidth-quota auto-suspend toggle (M13.1.1). When true, the
	// reconciler disables every owned domain of a user whose monthly
	// bytes ≥ BandwidthQuotaMB and re-enables when ≤ 80%. Off by default.
	BandwidthQuotaEnforceEnabled bool `gorm:"column:bandwidth_quota_enforce_enabled;type:tinyint(1);not null;default:0" json:"bandwidth_quota_enforce_enabled"`

	// File-manager upload cap (MB). Enforced by panel-api at request
	// time via http.MaxBytesReader; nginx vhost client_max_body_size
	// is set to the static ceiling (1G) so this is the operator-tunable
	// knob. Migration 000073. 0 falls back to the compile-time default.
	UploadMaxSizeMB uint32 `gorm:"column:upload_max_size_mb;type:int unsigned;not null;default:1024" json:"upload_max_size_mb"`

	// M45 root web terminal (ADR-0096). Off by default — when false the
	// token-mint endpoint 403s and the UI hides the Terminal tab. True
	// unrestricted root PTY; flipping it true is an explicit audited
	// operator decision. Every session is byte-recorded to
	// /var/log/jabali/terminal/<id>.cast and M14-notified on open.
	RootTerminalEnabled bool `gorm:"column:root_terminal_enabled;type:tinyint(1);not null;default:0" json:"root_terminal_enabled"`

	// M13 SSH shell sandbox (ADR-0067).
	// SSHSandboxMode ∈ {"bubblewrap", "nspawn"}. Default bubblewrap on
	// fresh installs. Wrapper at /usr/local/bin/jabali-ssh-shell reads
	// /etc/jabali/ssh-sandbox-mode (kept in lockstep with this column
	// by system.set_ssh_sandbox_mode) on every connect, so toggling the
	// mode does NOT require a sshd reload.
	SSHSandboxMode string `gorm:"column:ssh_sandbox_mode;type:varchar(16);not null;default:'bubblewrap'" json:"ssh_sandbox_mode"`
	// DefaultNspawnImageVersion is the image new SSH-enabled users are
	// stamped with at user-create time (reconciler stamps NULL pins).
	// Existing users keep their pin even after the default bumps —
	// upgrades are an explicit admin action. Format: [a-z0-9-]+ matching
	// the directory under /var/lib/jabali-nspawn/images/.
	DefaultNspawnImageVersion string `gorm:"column:default_nspawn_image_version;type:varchar(64);not null;default:'debian-13-v1'" json:"default_nspawn_image_version"`

	// M30 backup-restore retention knobs (migration 000085). Pruning runs
	// daily via the jabali-backup-retention.timer drop-in; values mirror
	// `restic forget --keep-daily/--keep-weekly/--keep-monthly`. Empty
	// BackupRemoteURL keeps backups at the local repo
	// /var/lib/jabali-backups/repo. M30.1 wires remote backends; v1 leaves
	// these unused.
	BackupKeepDaily            uint32 `gorm:"column:backup_keep_daily;type:int unsigned;not null;default:7"     json:"backup_keep_daily"`
	BackupKeepWeekly           uint32 `gorm:"column:backup_keep_weekly;type:int unsigned;not null;default:4"    json:"backup_keep_weekly"`
	BackupKeepMonthly          uint32 `gorm:"column:backup_keep_monthly;type:int unsigned;not null;default:6"   json:"backup_keep_monthly"`
	BackupRemoteURL            string `gorm:"column:backup_remote_url;type:varchar(512);not null;default:''"    json:"backup_remote_url"`
	BackupRemoteCredentialsRef string `gorm:"column:backup_remote_credentials_ref;type:varchar(128);not null;default:''" json:"backup_remote_credentials_ref"`
	// TenantBackupCron (GH #454) is the admin-owned cron time that TENANT
	// scheduled backups run at. Tenants choose the content + destination of
	// their own scheduled backup; the admin owns the timing (resource
	// planning). Default 03:00 daily.
	TenantBackupCron string `gorm:"column:tenant_backup_cron;type:varchar(64);not null;default:'0 3 * * *'" json:"tenant_backup_cron"`
	// TenantBackupWindowStart/End (GH #454 7B) is the admin-owned UTC maintenance
	// window ("HH:MM"). It restricts tenant multi-schedules ONLY when
	// TenantBackupWindowEnforce is on (GH #1097) — off by default, in which case
	// these values are informational and schedules run on their chosen interval.
	// When enforcing, the scheduler enqueues a due tenant schedule only within
	// the window and smears multiple due schedules across it. Defaults
	// 02:00–05:00. tenant_backup_cron stays authoritative for legacy
	// single-schedule installs; the window governs schedules created through the
	// multi-schedule surface (backup_schedules.cadence != '').
	TenantBackupWindowStart string `gorm:"column:tenant_backup_window_start;type:varchar(5);not null;default:'02:00'" json:"tenant_backup_window_start"`
	TenantBackupWindowEnd   string `gorm:"column:tenant_backup_window_end;type:varchar(5);not null;default:'05:00'" json:"tenant_backup_window_end"`
	// TenantBackupWindowEnforce (GH #1097) gates whether the window above
	// restricts tenant schedules at all. OFF by default: the scheduler ignores
	// the window and fires each schedule on its selected interval (Hourly = every
	// hour). ON: firings are confined to the window as in the original 7B model.
	// Persisted via the repo's Select("*") update, so the false zero-value is
	// written explicitly (no GORM default-tag zero-value trap).
	TenantBackupWindowEnforce bool `gorm:"column:tenant_backup_window_enforce;type:tinyint(1);not null;default:0" json:"tenant_backup_window_enforce"`

	// DR / standby (GH #331). ServerRole is 'primary' (default — box is fully
	// active) or 'standby' (a one-way async replica: pulls the primary's state,
	// serves no live traffic until manually promoted). The whole DR feature is
	// inert until `jabali dr pair` flips this. DRDestinationID is the read-only
	// backup destination the standby pulls from / the primary ships to; the
	// standby never holds a primary-mutating credential (least privilege).
	ServerRole      string     `gorm:"column:server_role;type:varchar(16);not null;default:'primary'" json:"server_role"`
	DRPairedAt      *time.Time `gorm:"column:dr_paired_at;type:datetime(6)" json:"dr_paired_at,omitempty"`
	DRPeerLabel     string     `gorm:"column:dr_peer_label;type:varchar(255);not null;default:''" json:"dr_peer_label"`
	DRDestinationID *string    `gorm:"column:dr_destination_id;type:char(26)" json:"dr_destination_id,omitempty"`
	// DR standby sync liveness (GH #331 Step 2, migration 000255). Written only
	// by the drsync loop on a standby; a primary leaves these at their defaults.
	// See models.DRSyncStatus* for the status vocabulary.
	DRLastSyncAt     *time.Time `gorm:"column:dr_last_sync_at;type:datetime(6)" json:"dr_last_sync_at,omitempty"`
	DRLastSnapshotID string     `gorm:"column:dr_last_snapshot_id;type:varchar(64);not null;default:''" json:"dr_last_snapshot_id"`
	DRLastSyncStatus string     `gorm:"column:dr_last_sync_status;type:varchar(16);not null;default:''" json:"dr_last_sync_status"`
	DRLastSyncError  string     `gorm:"column:dr_last_sync_error;type:varchar(1024);not null;default:''" json:"dr_last_sync_error"`

	// BackupMaxConcurrentJobs caps how many backup_jobs the in-process
	// dispatcher will keep in status=running at once. Scheduler ticks
	// enqueue rows as queued; the dispatcher drains them under this
	// ceiling. Migration 000096. 0 falls back to default 2.
	BackupMaxConcurrentJobs uint32 `gorm:"column:backup_max_concurrent_jobs;type:int unsigned;not null;default:2" json:"backup_max_concurrent_jobs"`

	// M34 per-user PHP-FPM egress firewall defaults (migration 000102).
	// NULL on the JSON columns means "use the agent's CanonicalDefaults()";
	// non-null arrays (including empty []) override. Burst threshold is
	// drops/tick that fires the M14 egress_drop_burst event source.
	EgressDefaultLoopbackCIDRs  *json.RawMessage `gorm:"column:egress_default_loopback_cidrs;type:json"          json:"egress_default_loopback_cidrs,omitempty"`
	EgressDefaultLoopback6CIDRs *json.RawMessage `gorm:"column:egress_default_loopback6_cidrs;type:json"         json:"egress_default_loopback6_cidrs,omitempty"`
	EgressDefaultPortsTCP       *json.RawMessage `gorm:"column:egress_default_ports_tcp;type:json"               json:"egress_default_ports_tcp,omitempty"`
	EgressDefaultPortsUDP       *json.RawMessage `gorm:"column:egress_default_ports_udp;type:json"               json:"egress_default_ports_udp,omitempty"`
	EgressBurstThreshold        uint32           `gorm:"column:egress_burst_threshold;type:int unsigned;not null;default:50" json:"egress_burst_threshold"`

	// M37 PostgreSQL parity (Phase 1, ADR-0091). Migration 000111.
	// PostgresEnabled gates the engine-discriminator code path —
	// fresh installs ship PG service installed but disabled; admin
	// flips this true in Server Settings to start using PG.
	// PostgresMaxConnectionsPerUser is surfaced when Wave A creates
	// per-user PG roles (mirrors MariaDB max_user_connections cap).
	PostgresEnabled bool `gorm:"column:postgres_enabled;type:tinyint(1);not null;default:0" json:"postgres_enabled"`
	// DockerMarketplaceEnabled gates the M48 docker-app marketplace.
	// When false, the panel installs no docker engine and unmounts
	// /admin/docker-apps; when flipped true, panel-api dispatches
	// docker.install on the agent (sources install.sh + runs
	// install_docker_engine). Flip false stops the daemon and masks
	// the units; per-app data under /var/lib/jabali/docker-apps/ is
	// left intact for a future re-enable.
	DockerMarketplaceEnabled bool `gorm:"column:docker_marketplace_enabled;type:tinyint(1);not null;default:0" json:"docker_marketplace_enabled"`
	// DockerAppsForUsersEnabled is the admin opt-in that exposes tenant Docker
	// Apps to users (separate from DockerMarketplaceEnabled, which only
	// installs the engine). Default off. When flipped on, panel-api dispatches
	// docker.tenant_set on the agent to run `jabali docker enable-tenant`
	// (userns-remap host setup); off removes the host tenant flag so the
	// tenant docker surface is gated off. Drives the user-panel Docker Apps
	// tab visibility via /me/server-capabilities.
	DockerAppsForUsersEnabled bool `gorm:"column:docker_apps_for_users_enabled;type:tinyint(1);not null;default:0" json:"docker_apps_for_users_enabled"`
	// DockerTenantApps is a CSV of catalog slugs the admin exposes to tenants.
	// Empty = expose ALL tenant-eligible apps (the curated default). Narrows
	// which Docker apps users may install.
	DockerTenantApps string `gorm:"column:docker_tenant_apps;type:varchar(4096);not null;default:''" json:"docker_tenant_apps"`
	// PythonAppsEnabled gates the Python Application Manager (ADR-0131 / GH
	// #203): hidden + endpoints 403 when off (default). Enabling installs the
	PythonAppsEnabled bool `gorm:"column:python_apps_enabled;type:tinyint(1);not null;default:0" json:"python_apps_enabled"`
	// StalwartWebadminEnabled (GH #243, ADR-0142) gates the opt-in nginx
	// reverse-proxy exposing Stalwart's WebAdmin UI. Default false; Stalwart
	// itself stays loopback-bound.
	StalwartWebadminEnabled bool `gorm:"column:stalwart_webadmin_enabled;type:tinyint(1);not null;default:0" json:"stalwart_webadmin_enabled"`
	// StalwartWebadminAllowCIDRs is an optional comma/space-separated nginx
	// source-IP allowlist for the WebAdmin vhost. Empty = allow any IP that
	// passes basic-auth.
	StalwartWebadminAllowCIDRs    string `gorm:"column:stalwart_webadmin_allow_cidrs;type:varchar(512);not null;default:''" json:"stalwart_webadmin_allow_cidrs"`
	PostgresMaxConnectionsPerUser uint16 `gorm:"column:postgres_max_connections_per_user;type:smallint unsigned;not null;default:25" json:"postgres_max_connections_per_user"`

	// MigrationAllowPrivateHosts — ADR-0095 decision 8. When TRUE the
	// SSRF guard for /admin/migrations outbound dials permits RFC1918
	// targets (10/8, 172.16/12, 192.168/16) as well as IPv6 ULA. DNS
	// rebinding protection still applies. Loopback/link-local stays
	// rejected regardless. Default off — flip only for VPN-tunneled
	// migrations from internal legacy infra.
	MigrationAllowPrivateHosts bool `gorm:"column:migration_allow_private_hosts;type:tinyint(1);not null;default:0" json:"migration_allow_private_hosts"`

	// WorkingFolder is the base directory used by migrations + backups
	// (migration 000131). Default '/var/lib/jabali' — operators retarget
	// to a larger disk when /var partition is small.
	// Subdirs (created on first use):
	//   <working_folder>/migrations/<job-id>/ — extracted source tarballs
	//   <working_folder>/backups/repo         — restic repo
	//   <working_folder>/backups/logs/        — per-job backup logs
	// Legacy /var/lib/jabali-migrations + /var/lib/jabali-backups paths
	// remain valid agent-side; install.sh symlinks them under the new
	// working_folder so existing data survives schema upgrade.
	WorkingFolder string `gorm:"column:working_folder;type:varchar(255);not null;default:'/var/lib/jabali'" json:"working_folder"`

	// M55 Nginx Settings tab (migration 000165). Server-wide nginx tunables.
	// The agent's nginx.tunables.apply verb renders the http-scope columns into
	// /etc/nginx/conf.d/05-jabali-tunables.conf (atomic-write + nginx -t gate +
	// rollback) and patches the two worker-scope knobs into /etc/nginx/nginx.conf.
	// install.sh seeds the fragment with these same defaults so a fresh install
	// matches the row. Timeout/size columns hold the nginx value verbatim
	// ("50m", "300s"); the API + agent validate by regex. NginxServerTokens=false
	// → `server_tokens off` (hide version, secure default). NginxCustomHTTP is
	// admin-supplied raw http{}-scope directives gated only by nginx -t; the UI
	// warns it can destabilize the server. NginxWorkerProcesses is "auto" or int.
	NginxClientMaxBodySize   string `gorm:"column:nginx_client_max_body_size;type:varchar(16);not null;default:'512m'"   json:"nginx_client_max_body_size"`
	NginxKeepaliveTimeout    string `gorm:"column:nginx_keepalive_timeout;type:varchar(16);not null;default:'65s'"      json:"nginx_keepalive_timeout"`
	NginxServerTokens        bool   `gorm:"column:nginx_server_tokens;type:tinyint(1);not null;default:0"               json:"nginx_server_tokens"`
	NginxGzip                bool   `gorm:"column:nginx_gzip;type:tinyint(1);not null;default:1"                        json:"nginx_gzip"`
	NginxClientBodyTimeout   string `gorm:"column:nginx_client_body_timeout;type:varchar(16);not null;default:'60s'"    json:"nginx_client_body_timeout"`
	NginxClientHeaderTimeout string `gorm:"column:nginx_client_header_timeout;type:varchar(16);not null;default:'60s'"  json:"nginx_client_header_timeout"`
	NginxSendTimeout         string `gorm:"column:nginx_send_timeout;type:varchar(16);not null;default:'60s'"           json:"nginx_send_timeout"`
	NginxProxyConnectTimeout string `gorm:"column:nginx_proxy_connect_timeout;type:varchar(16);not null;default:'300s'" json:"nginx_proxy_connect_timeout"`
	NginxProxyReadTimeout    string `gorm:"column:nginx_proxy_read_timeout;type:varchar(16);not null;default:'300s'"    json:"nginx_proxy_read_timeout"`
	NginxProxySendTimeout    string `gorm:"column:nginx_proxy_send_timeout;type:varchar(16);not null;default:'300s'"    json:"nginx_proxy_send_timeout"`
	NginxWorkerProcesses     string `gorm:"column:nginx_worker_processes;type:varchar(8);not null;default:'auto'"       json:"nginx_worker_processes"`
	NginxWorkerConnections   uint32 `gorm:"column:nginx_worker_connections;type:int unsigned;not null;default:768"      json:"nginx_worker_connections"`
	NginxCustomHTTP          string `gorm:"column:nginx_custom_http;type:varchar(4000);not null;default:''"             json:"nginx_custom_http"`
	// GH #634: configurable shared FastCGI cache capacity (ADR-0108 keyzone).
	NginxCacheMaxSizeGB   int `gorm:"column:nginx_cache_max_size_gb;type:int;not null;default:4"    json:"nginx_cache_max_size_gb"`
	NginxCacheKeyzoneMB   int `gorm:"column:nginx_cache_keyzone_mb;type:int;not null;default:64"    json:"nginx_cache_keyzone_mb"`
	NginxCacheInactiveMin int `gorm:"column:nginx_cache_inactive_min;type:int;not null;default:60"  json:"nginx_cache_inactive_min"`

	// LogRetention holds operator-configured retention windows for the log/report
	// tables, as a { category: days } JSON map (Server Settings -> Logs). An
	// absent/invalid category falls back to DefaultLogRetentionDays. Read via
	// RetentionDays(); never index into the raw map directly.
	LogRetention *json.RawMessage `gorm:"column:log_retention;type:json" json:"log_retention,omitempty"`

	// AuditChainAnchor is the row_hash of the newest sealed audit_events row that
	// retention pruning removed (JAB-105). audit verify starts the chain
	// recomputation from this anchor instead of genesis so the surviving chain
	// stays verifiable after a prune. NULL/empty = never pruned (root at "").
	AuditChainAnchor *string `gorm:"column:audit_chain_anchor;type:char(64)" json:"audit_chain_anchor,omitempty"`

	// CFAPITokenEnc (JAB-235) is the operator's Cloudflare API token, sealed
	// with ssokey (AES-256-GCM) — used by the ACME DNS-01 fallback to write
	// _acme-challenge TXT records into Cloudflare-hosted customer zones.
	// json:"-": the token must NEVER appear in any API response; the
	// dedicated /admin/ssl/cloudflare-token endpoints are the only writers
	// and report configured/not-configured only. Least privilege: the token
	// needs Zone:DNS:Edit + Zone:Read on the zones it should cover — advise
	// operators to mint a dedicated scoped token, not a global one.
	CFAPITokenEnc []byte `gorm:"column:cf_api_token_enc;type:varbinary(512)" json:"-"`

	// FTP module (GH #1053, plans/gh1053-ftp-accounts.md). FTPEnabled is the
	// server-level opt-in (operator decision: default OFF) — flipping it on
	// installs/starts the vsftpd module (M353 install-on-enable) and opens
	// the firewall ports; off masks the daemon and closes them. SFTP
	// subaccounts work regardless of this flag.
	FTPEnabled bool `gorm:"column:ftp_enabled;type:tinyint(1);not null;default:0" json:"ftp_enabled"`
	// FTPAllowPlaintext deliberately defaults to false: with FTP enabled,
	// TLS is still REQUIRED unless the operator explicitly allows legacy
	// cleartext logins. Never relax this silently.
	FTPAllowPlaintext bool `gorm:"column:ftp_allow_plaintext;type:tinyint(1);not null;default:0" json:"ftp_allow_plaintext"`
	// FTP server tuning (GH #1053 follow-up). All 0 = unlimited; rendered
	// into /etc/vsftpd.conf by install_vsftpd_config on every module
	// (re-)install.
	FTPMaxClients      uint32 `gorm:"column:ftp_max_clients;type:int unsigned;not null;default:50" json:"ftp_max_clients"`
	FTPMaxPerIP        uint32 `gorm:"column:ftp_max_per_ip;type:int unsigned;not null;default:8" json:"ftp_max_per_ip"`
	FTPLocalMaxRateKBs uint32 `gorm:"column:ftp_local_max_rate_kbs;type:int unsigned;not null;default:0" json:"ftp_local_max_rate_kbs"`
	// FTPPasvAddress is the external address vsftpd advertises for passive
	// connections when the host is behind NAT. Empty = advertise the local
	// address.
	FTPPasvAddress string `gorm:"column:ftp_pasv_address;type:text;not null;default:''" json:"ftp_pasv_address"`

	UpdatedAt time.Time `gorm:"type:datetime(6);not null"             json:"updated_at"`
}

func (ServerSettings) TableName() string { return "server_settings" }

// EffectiveDNSTTL returns the operator-configured default DNS record TTL, or
// 300s when unset/nil. GH #527: apply the default consistently to auto-created
// zone records (apex/www/NS) and migrated records, not just hand-made ones —
// mirrors the API create path's fallback (dns.go defaultRecordTTL).
func EffectiveDNSTTL(s *ServerSettings) int {
	if s != nil && s.DefaultDNSTTL != 0 {
		return int(s.DefaultDNSTTL)
	}
	return 300
}

// Log-retention category keys (Server Settings -> Logs). Each groups one or more
// log/report tables the retention sweep prunes.
const (
	RetentionCatNotifications  = "notifications"
	RetentionCatUpdates        = "updates"
	RetentionCatMailReports    = "mail_reports"
	RetentionCatSecurityEvents = "security_events"
	RetentionCatDBAdminAudit   = "db_admin_audit"
	RetentionCatSessions       = "sessions"
	RetentionCatMigrations     = "migrations"
	RetentionCatBandwidth      = "bandwidth"
	RetentionCatAudit          = "audit"
)

// DefaultLogRetentionDays is the fallback window per category when the operator
// hasn't set one. 90d baseline; 365d for forensic security tables; 0 (keep
// forever) for the hash-chained audit log (a security audit must not silently
// self-delete — it prunes with a chain re-anchor only when explicitly set).
var DefaultLogRetentionDays = map[string]int{
	RetentionCatNotifications:  90,
	RetentionCatUpdates:        90,
	RetentionCatMailReports:    90,
	RetentionCatSecurityEvents: 365,
	RetentionCatDBAdminAudit:   365,
	RetentionCatSessions:       90,
	RetentionCatMigrations:     90,
	RetentionCatBandwidth:      90,
	RetentionCatAudit:          0,
}

// RetentionDays returns the configured retention window (in days) for a log
// category: the operator override when present + valid, else the code default
// (90 for an unknown category). 0 means keep forever (pruning disabled).
func (s ServerSettings) RetentionDays(category string) int {
	def, ok := DefaultLogRetentionDays[category]
	if !ok {
		def = 90
	}
	if s.LogRetention == nil || len(*s.LogRetention) == 0 {
		return def
	}
	var m map[string]int
	if err := json.Unmarshal(*s.LogRetention, &m); err != nil {
		return def
	}
	if v, present := m[category]; present && v >= 0 && v <= 3650 {
		return v
	}
	return def
}
