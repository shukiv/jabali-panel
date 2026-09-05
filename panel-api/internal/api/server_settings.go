package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	internalbackup "git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ginctx"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/settingsops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

type ServerSettingsHandlerConfig struct {
	Repo  repository.ServerSettingsRepository
	Agent agent.AgentInterface
	Log   *slog.Logger
	// SSOKey seals the Cloudflare API token at rest (JAB-235). Nil disables
	// the token endpoints (503) — a panel without sso.key must not hold the
	// secret in plaintext.
	SSOKey *ssokey.Key
	// CFVerify overrides the Cloudflare token-verification call in tests.
	// Nil = verify against the real Cloudflare API.
	CFVerify func(ctx context.Context, token string) (zones int, err error)
}

// RegisterServerSettingsRoutes mounts server settings endpoints under the
// given group (typically /api/v1 with auth middleware already applied).
func RegisterServerSettingsRoutes(g *gin.RouterGroup, cfg ServerSettingsHandlerConfig) {
	h := &serverSettingsHandler{cfg: cfg}
	admin := g.Group("/admin/settings")
	admin.Use(middleware.RequireAdmin())
	admin.GET("", h.get)
	admin.PATCH("", h.update)
	admin.GET("/python-runtime", h.pythonRuntimeStatus)
	// GH #243: reveal / rotate the Stalwart admin GUI login (admin:<token>).
	admin.POST("/stalwart-admin-credential", h.stalwartAdminCredential)
	// M353 module install-on-enable: the Modules card polls status while a
	// toggle installs, and re-dispatches an install as a retry affordance.
	admin.GET("/modules/status", h.moduleStatus)
	admin.POST("/modules/install", h.moduleInstall)
	// JAB-259/260 phase C: observed-vs-desired FTP posture so the FTP card can
	// warn when the host disagrees with the panel (still serving after a disable,
	// or plaintext after a tighten) instead of rendering a false off/secure.
	admin.GET("/modules/ftp/status", h.ftpStatus)
	// JAB-213: activate a free jabalihosted.com hostname from Server Settings.
	admin.POST("/free-hostname/register", h.freeHostnameRegister)
	admin.POST("/free-hostname/claim", h.freeHostnameClaim)
	// JAB-235: the Cloudflare API token behind the ACME DNS-01 fallback.
	admin.GET("/cloudflare-token", h.cfTokenStatus)
	admin.PUT("/cloudflare-token", h.cfTokenSet)
	admin.DELETE("/cloudflare-token", h.cfTokenClear)
}

type serverSettingsHandler struct{ cfg ServerSettingsHandlerConfig }

func (h *serverSettingsHandler) get(c *gin.Context) {
	ctx := c.Request.Context()
	s, err := h.cfg.Repo.Get(ctx)
	if errors.Is(err, repository.ErrNotFound) {
		// First boot before the seed has run, or a brand-new install
		// where config.toml had no [server] identity to seed from.
		// Return an empty shell instead of 500 so the form loads clean.
		s = &models.ServerSettings{ID: 1}
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Timezone fallback: when the DB column is empty (admin never picked
	// one yet), surface the OS-configured zone so the UI dropdown shows
	// the actual current value instead of blank. Doesn't write back —
	// the row stays empty until the admin saves explicitly.
	if s.Timezone == "" && h.cfg.Agent != nil {
		infoCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if raw, err := h.cfg.Agent.Call(infoCtx, "system.info", nil); err == nil {
			var info struct {
				Timezone string `json:"timezone"`
			}
			if jsonErr := json.Unmarshal(raw, &info); jsonErr == nil && info.Timezone != "" {
				s.Timezone = info.Timezone
			}
		} else {
			h.cfg.Log.Debug("agent system.info failed during timezone fallback", "err", err)
		}
	}

	// Surface the EFFECTIVE tenant channel-kind allowlist: an empty column means
	// "safe defaults", so show them rather than a blank list. Doesn't write back.
	if len(s.TenantNotificationKinds) == 0 {
		s.TenantNotificationKinds = models.DefaultTenantNotificationKinds()
	}

	c.JSON(http.StatusOK, s)
}

type updateServerSettingsRequest struct {
	Hostname                 *string          `json:"hostname,omitempty"`
	PreviewBase              *string          `json:"preview_base,omitempty"`
	PublicIPv4               *string          `json:"public_ipv4,omitempty"`
	PublicIPv6               *string          `json:"public_ipv6,omitempty"`
	NS1Name                  *string          `json:"ns1_name,omitempty"`
	NS1IPv4                  *string          `json:"ns1_ipv4,omitempty"`
	NS2Name                  *string          `json:"ns2_name,omitempty"`
	NS2IPv4                  *string          `json:"ns2_ipv4,omitempty"`
	AdminEmail               *string          `json:"admin_email,omitempty"`
	LogRetention             *json.RawMessage `json:"log_retention,omitempty"`
	Timezone                 *string          `json:"timezone,omitempty"`
	SSHPort                  *uint16          `json:"ssh_port,omitempty"`
	SSHPasswordAuth          *bool            `json:"ssh_password_auth,omitempty"`
	SSHUserPasswordAuth      *bool            `json:"ssh_user_password_auth,omitempty"`
	PanelBrandText           *string          `json:"panel_brand_text,omitempty"`
	PanelFontSize            *string          `json:"panel_font_size,omitempty"`
	PanelPrimaryColor        *string          `json:"panel_primary_color,omitempty"`
	PanelAccentColor         *string          `json:"panel_accent_color,omitempty"`
	PanelSuccessColor        *string          `json:"panel_success_color,omitempty"`
	PanelWarningColor        *string          `json:"panel_warning_color,omitempty"`
	PanelErrorColor          *string          `json:"panel_error_color,omitempty"`
	PanelInfoColor           *string          `json:"panel_info_color,omitempty"`
	PanelLinkColor           *string          `json:"panel_link_color,omitempty"`
	PanelLightTopbarColor    *string          `json:"panel_light_topbar_color,omitempty"`
	PanelDarkTopbarColor     *string          `json:"panel_dark_topbar_color,omitempty"`
	PanelLightBgColor        *string          `json:"panel_light_bg_color,omitempty"`
	PanelDarkBgColor         *string          `json:"panel_dark_bg_color,omitempty"`
	PanelLightContainerColor *string          `json:"panel_light_container_color,omitempty"`
	PanelDarkContainerColor  *string          `json:"panel_dark_container_color,omitempty"`
	PanelLightTextColor      *string          `json:"panel_light_text_color,omitempty"`
	PanelDarkTextColor       *string          `json:"panel_dark_text_color,omitempty"`
	ReleaseChannel           *string          `json:"release_channel,omitempty"`
	// DNS record-type permissions (GH #466): supply either a named preset
	// (permissive | hosting-safe | locked-down) or the full per-type matrix.
	DNSUserRecordPolicy       *models.DNSUserRecordPolicy `json:"dns_user_record_policy,omitempty"`
	DNSUserRecordPolicyPreset *string                     `json:"dns_user_record_policy_preset,omitempty"`
	DiskQuotaEnabled          *bool                       `json:"disk_quota_enabled,omitempty"`
	WebmailEnabled            *bool                       `json:"webmail_enabled,omitempty"`
	// M353 Phase 1 (GH #353): per-module enable flags (admin toggles them from
	// Server Settings -> Modules; the SPA gates nav + routes on them).
	DNSEnabled      *bool `json:"dns_enabled,omitempty"`
	MailEnabled     *bool `json:"mail_enabled,omitempty"`
	SecurityEnabled *bool `json:"security_enabled,omitempty"`
	QuotaEnabled    *bool `json:"quota_enabled,omitempty"`
	APIEnabled      *bool `json:"api_enabled,omitempty"`
	// GH #1053: FTP module opt-in (default OFF). ftp_allow_plaintext is a
	// second explicit gate — TLS stays required without it. ftp_pasv_address
	// is the NAT passive-mode address vsftpd advertises.
	FTPEnabled        *bool   `json:"ftp_enabled,omitempty"`
	FTPAllowPlaintext *bool   `json:"ftp_allow_plaintext,omitempty"`
	FTPPasvAddress    *string `json:"ftp_pasv_address,omitempty"`
	// vsftpd tuning; 0 = unlimited. Bounded at the API so a typo can't
	// render a nonsensical config.
	FTPMaxClients              *uint32 `json:"ftp_max_clients,omitempty"`
	FTPMaxPerIP                *uint32 `json:"ftp_max_per_ip,omitempty"`
	FTPLocalMaxRateKBs         *uint32 `json:"ftp_local_max_rate_kbs,omitempty"`
	TenantDomainOptionsEnabled *bool   `json:"tenant_domain_options_enabled,omitempty"`
	TenantDocrootEditable      *bool   `json:"tenant_docroot_editable,omitempty"`
	// GH #860: opt-in branded page on the default catch-all for unknown
	// hosts (default off = keep the 444 drop).
	UnconfiguredPageEnabled *bool `json:"unconfigured_page_enabled,omitempty"`
	// GH #1161: opt-in — also serve /api/v1/automation/ on :443 (default off,
	// :8443 only). For billing hosts whose outbound firewall blocks 8443.
	AutomationApiPublicEnabled *bool `json:"automation_api_public_enabled,omitempty"`
	AdminFileManagerEnabled    *bool `json:"admin_file_manager_enabled,omitempty"` // GH #1184
	// GH #879: server-wide default for the branded application-error page;
	// domains override via nginx_safe_options.intercept_errors.
	InterceptAppErrorsDefault *bool `json:"intercept_app_errors_default,omitempty"`
	// GH #648: opt-in DKIM2 signing alongside classic DKIM (default off).
	DKIM2SigningEnabled *bool `json:"dkim2_signing_enabled,omitempty"`
	// TenantNotificationKinds — admin-configurable tenant channel-kind allowlist
	// (phase 4b).
	TenantNotificationKinds *models.TenantNotificationKinds `json:"tenant_notification_kinds,omitempty"`
	// TenantNotificationsEnabled — master switch for the whole /me/notifications
	// surface (phase 4e). Now exposed: the abuse controls it gated on (SSRF 4a,
	// allowlist 4b, quota+rate-limit 4c, email-to-own 4d) have all landed, and
	// the dispatcher honours this flag as a live delivery kill switch.
	TenantNotificationsEnabled   *bool   `json:"tenant_notifications_enabled,omitempty"`
	RootTerminalEnabled          *bool   `json:"root_terminal_enabled,omitempty"`
	BandwidthQuotaEnforceEnabled *bool   `json:"bandwidth_quota_enforce_enabled,omitempty"`
	UploadMaxSizeMB              *uint32 `json:"upload_max_size_mb,omitempty"`
	// TenantBackupCron (GH #454): the admin-owned cron time tenant scheduled
	// backups run at. Tenants choose content + destination; only the admin sets
	// the timing. Validated server-side via internalbackup.ParseCron.
	TenantBackupCron *string `json:"tenant_backup_cron,omitempty"`
	// TenantBackupWindow{Start,End} (GH #454 7B): the admin-owned UTC maintenance
	// window ("HH:MM") that tenant window-governed multi-schedules fire inside.
	// Validated server-side via internalbackup.ParseWindow round-trip.
	TenantBackupWindowStart *string `json:"tenant_backup_window_start,omitempty"`
	TenantBackupWindowEnd   *string `json:"tenant_backup_window_end,omitempty"`
	// TenantBackupWindowEnforce (GH #1097) — opt-in gate for the window above.
	TenantBackupWindowEnforce *bool `json:"tenant_backup_window_enforce,omitempty"`
	// DefaultLocalBackupsEnabled (GH #1240) — opt-in automatic daily local
	// backups for every user; the scheduler converges the managed schedule.
	DefaultLocalBackupsEnabled *bool `json:"default_local_backups_enabled,omitempty"`

	// M13 SSH shell sandbox.
	SSHSandboxMode            *string `json:"ssh_sandbox_mode,omitempty"`
	DefaultNspawnImageVersion *string `json:"default_nspawn_image_version,omitempty"`

	// M30.1 backup concurrency. Per-schedule retention is the source
	// of truth — server-wide keep_* fields exist on the row but are
	// no longer writeable from the API.
	BackupMaxConcurrentJobs *uint32 `json:"backup_max_concurrent_jobs,omitempty"`

	// M37 PostgreSQL parity (ADR-0091). PostgresEnabled gates the
	// /databases POST handler from accepting engine="postgres" + the
	// reconciler from starting the postgresql service.
	PostgresEnabled *bool `json:"postgres_enabled,omitempty"`
	// M48 docker-app marketplace opt-in. flip true -> agent installs
	// docker via install_docker_engine; flip false -> agent stops +
	// disables docker units (data kept).
	DockerMarketplaceEnabled *bool `json:"docker_marketplace_enabled,omitempty"`
	// Admin opt-in: expose tenant Docker Apps to users (GH docker-opt-in).
	DockerAppsForUsersEnabled *bool `json:"docker_apps_for_users_enabled,omitempty"`
	// Admin-curated CSV of tenant-exposed Docker app slugs (empty = all eligible).
	DockerTenantApps *string `json:"docker_tenant_apps,omitempty"`
	// ADR-0131: Python Application Manager opt-in.
	PythonAppsEnabled *bool `json:"python_apps_enabled,omitempty"`
	// GH #243 (ADR-0142): opt-in Stalwart WebAdmin exposure + IP allowlist.
	StalwartWebadminEnabled    *bool   `json:"stalwart_webadmin_enabled,omitempty"`
	StalwartWebadminAllowCIDRs *string `json:"stalwart_webadmin_allow_cidrs,omitempty"`
	// Transient (not persisted): force a fresh gateway credential.
	PostgresMaxConnectionsPerUser *uint16 `json:"postgres_max_connections_per_user,omitempty"`

	// M35 SSRF override. When true, migrate.ValidateHost accepts
	// RFC1918 / loopback / link-local targets. Operator opts in for
	// local-network test migrations; default-deny remains the safe
	// production stance. ADR-0095 decision 8.
	MigrationAllowPrivateHosts *bool `json:"migration_allow_private_hosts,omitempty"`

	// WorkingFolder is the on-disk base for migrations + backups.
	// Default '/var/lib/jabali'. Must be an absolute path. Operator
	// retargets to a larger disk by setting + symlinking the legacy
	// jabali-migrations / jabali-backups dirs underneath.
	WorkingFolder *string `json:"working_folder,omitempty"`

	// DefaultDNSTTL is the server-wide default for new DNS records
	// (seconds). Operator-settable via Server Settings → DNS. Range
	// 60–86400 (1 min to 1 day); values outside that range are
	// rejected with HTTP 422.
	DefaultDNSTTL *uint32 `json:"default_dns_ttl,omitempty"`

	// CrowdsecSensitivity preset. relaxed | balanced | strict.
	// Applied via agent verb security.crowdsec.sensitivity.apply
	// which writes scenario/profile/anomaly drop-ins.
	CrowdsecSensitivity *string `json:"crowdsec_sensitivity,omitempty"`

	// CrowdsecBouncerMode preset. live | stream. Applied via agent verb
	// security.crowdsec.bouncer.mode.apply which rewrites MODE= in the
	// nginx-bouncer conf + reloads nginx.
	CrowdsecBouncerMode *string `json:"crowdsec_bouncer_mode,omitempty"`

	// GH #598 auto-allowlist-on-login. Enabled applies to BOTH the panel-login
	// middleware and the agent SSH watcher (pushed via
	// security.crowdsec.login_allowlist.apply). TTLHours is the allowlist
	// lifetime (1..8760); refreshed on activity.
	CrowdsecLoginAllowlistEnabled  *bool `json:"crowdsec_login_allowlist_enabled,omitempty"`
	CrowdsecLoginAllowlistTTLHours *int  `json:"crowdsec_login_allowlist_ttl_hours,omitempty"`

	// M55 Nginx Settings tab. All optional; any present field triggers a
	// re-render + nginx.tunables.apply dispatch (re-sync semantics, like SSH).
	NginxClientMaxBodySize   *string `json:"nginx_client_max_body_size,omitempty"`
	NginxKeepaliveTimeout    *string `json:"nginx_keepalive_timeout,omitempty"`
	NginxServerTokens        *bool   `json:"nginx_server_tokens,omitempty"`
	NginxGzip                *bool   `json:"nginx_gzip,omitempty"`
	NginxClientBodyTimeout   *string `json:"nginx_client_body_timeout,omitempty"`
	NginxClientHeaderTimeout *string `json:"nginx_client_header_timeout,omitempty"`
	NginxSendTimeout         *string `json:"nginx_send_timeout,omitempty"`
	NginxProxyConnectTimeout *string `json:"nginx_proxy_connect_timeout,omitempty"`
	NginxProxyReadTimeout    *string `json:"nginx_proxy_read_timeout,omitempty"`
	NginxProxySendTimeout    *string `json:"nginx_proxy_send_timeout,omitempty"`
	NginxCacheMaxSizeGB      *int    `json:"nginx_cache_max_size_gb,omitempty"`
	NginxCacheKeyzoneMB      *int    `json:"nginx_cache_keyzone_mb,omitempty"`
	NginxCacheInactiveMin    *int    `json:"nginx_cache_inactive_min,omitempty"`
	NginxWorkerProcesses     *string `json:"nginx_worker_processes,omitempty"`
	NginxWorkerConnections   *uint32 `json:"nginx_worker_connections,omitempty"`
	NginxCustomHTTP          *string `json:"nginx_custom_http,omitempty"`
}

func (h *serverSettingsHandler) update(c *gin.Context) {
	claims := ginctx.Claims(c)
	if claims == nil {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthenticated"})
		return
	}

	var req updateServerSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "validation_failed", "detail": err.Error()})
		return
	}

	ctx := c.Request.Context()
	current, err := h.cfg.Repo.Get(ctx)
	if errors.Is(err, repository.ErrNotFound) {
		// No row yet (e.g. seed hadn't run or config was empty at boot).
		// Treat PATCH as initial save so the first admin edit lands cleanly.
		current = &models.ServerSettings{ID: 1}
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Full pre-merge snapshot. settingsops.ModuleEffects and NginxEffects both
	// diff against it (nginx fields are untouched until the nginx merge far
	// below, so this single snapshot serves both — no separate beforeNginx).
	before := *current

	// Optional-module transitions (postgres, docker marketplace + tenant, python,
	// and the dns/mail/quota/security/ftp loop) are detected from `before` by
	// settingsops.ModuleEffects below; the SSH config + sandbox transitions are
	// detected from `before` by settingsops.SSHEffects — no per-flag prev* locals
	// needed for either family.
	prevHostname := current.Hostname
	prevPanelBrandText := current.PanelBrandText
	prevWebadminEnabled := current.StalwartWebadminEnabled
	prevWebadminCIDRs := current.StalwartWebadminAllowCIDRs
	prevTimezone := current.Timezone
	prevCrowdsecSensitivity := current.CrowdsecSensitivity
	prevCrowdsecBouncerMode := current.CrowdsecBouncerMode
	prevCrowdsecLoginAllowlistEnabled := current.CrowdsecLoginAllowlistEnabled
	prevCrowdsecLoginAllowlistTTLHours := current.CrowdsecLoginAllowlistTTLHours

	if req.Hostname != nil {
		current.Hostname = strings.TrimSpace(*req.Hostname)
	}
	if req.PreviewBase != nil {
		current.PreviewBase = models.NormalizePreviewBase(*req.PreviewBase)
	}
	if req.PublicIPv4 != nil {
		current.PublicIPv4 = strings.TrimSpace(*req.PublicIPv4)
	}
	if req.PublicIPv6 != nil {
		current.PublicIPv6 = strings.TrimSpace(*req.PublicIPv6)
	}
	if req.NS1Name != nil {
		current.NS1Name = strings.TrimSpace(*req.NS1Name)
	}
	if req.NS1IPv4 != nil {
		current.NS1IPv4 = strings.TrimSpace(*req.NS1IPv4)
	}
	if req.NS2Name != nil {
		current.NS2Name = strings.TrimSpace(*req.NS2Name)
	}
	if req.NS2IPv4 != nil {
		current.NS2IPv4 = strings.TrimSpace(*req.NS2IPv4)
	}
	if req.AdminEmail != nil {
		current.AdminEmail = strings.TrimSpace(*req.AdminEmail)
	}
	if req.Timezone != nil {
		current.Timezone = strings.TrimSpace(*req.Timezone)
	}
	if req.LogRetention != nil {
		if err := validateLogRetention(*req.LogRetention); err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "validation_failed", "detail": err.Error()})
			return
		}
		lr := *req.LogRetention
		current.LogRetention = &lr
	}
	if req.SSHPort != nil {
		current.SSHPort = *req.SSHPort
	}
	if req.DefaultDNSTTL != nil {
		current.DefaultDNSTTL = *req.DefaultDNSTTL
	}
	if req.CrowdsecSensitivity != nil {
		v := strings.TrimSpace(*req.CrowdsecSensitivity)
		switch v {
		case "relaxed", "balanced", "strict":
			current.CrowdsecSensitivity = v
		default:
			c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{
				"error":   "invalid_crowdsec_sensitivity",
				"message": "must be one of: relaxed, balanced, strict",
			})
			return
		}
	}
	if req.CrowdsecBouncerMode != nil {
		v := strings.TrimSpace(*req.CrowdsecBouncerMode)
		switch v {
		case "live", "stream":
			current.CrowdsecBouncerMode = v
		default:
			c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{
				"error":   "invalid_crowdsec_bouncer_mode",
				"message": "must be one of: live, stream",
			})
			return
		}
	}
	if req.CrowdsecLoginAllowlistEnabled != nil {
		current.CrowdsecLoginAllowlistEnabled = *req.CrowdsecLoginAllowlistEnabled
	}
	if req.CrowdsecLoginAllowlistTTLHours != nil {
		v := *req.CrowdsecLoginAllowlistTTLHours
		if v < 1 || v > 8760 { // 1h .. 365d
			c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{
				"error":   "invalid_crowdsec_login_allowlist_ttl_hours",
				"message": "must be between 1 and 8760 hours",
			})
			return
		}
		current.CrowdsecLoginAllowlistTTLHours = v
	}
	if req.SSHPasswordAuth != nil {
		current.SSHPasswordAuth = *req.SSHPasswordAuth
	}
	if req.SSHUserPasswordAuth != nil {
		current.SSHUserPasswordAuth = *req.SSHUserPasswordAuth
	}
	if req.PanelBrandText != nil {
		current.PanelBrandText = strings.TrimSpace(*req.PanelBrandText)
	}
	if req.PanelFontSize != nil {
		v := strings.TrimSpace(*req.PanelFontSize)
		switch v {
		case "small", "medium", "large":
			current.PanelFontSize = v
		default:
			c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{
				"error":   "invalid_panel_font_size",
				"message": "must be one of: small, medium, large",
			})
			return
		}
	}
	// "Look and feel" colors — each empty-or-hex, validated uniformly.
	validateColor := func(reqVal *string, dst *string, name string) bool {
		if reqVal == nil {
			return true
		}
		v := strings.TrimSpace(*reqVal)
		if !isValidHexColor(v) {
			c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{
				"error":   "invalid_" + name,
				"message": "must be empty or a hex color (#rgb, #rrggbb, #rrggbbaa)",
			})
			return false
		}
		*dst = v
		return true
	}
	if !validateColor(req.PanelPrimaryColor, &current.PanelPrimaryColor, "panel_primary_color") {
		return
	}
	if !validateColor(req.PanelAccentColor, &current.PanelAccentColor, "panel_accent_color") {
		return
	}
	if !validateColor(req.PanelSuccessColor, &current.PanelSuccessColor, "panel_success_color") {
		return
	}
	if !validateColor(req.PanelWarningColor, &current.PanelWarningColor, "panel_warning_color") {
		return
	}
	if !validateColor(req.PanelErrorColor, &current.PanelErrorColor, "panel_error_color") {
		return
	}
	if !validateColor(req.PanelInfoColor, &current.PanelInfoColor, "panel_info_color") {
		return
	}
	if !validateColor(req.PanelLinkColor, &current.PanelLinkColor, "panel_link_color") {
		return
	}
	if !validateColor(req.PanelLightTopbarColor, &current.PanelLightTopbarColor, "panel_light_topbar_color") {
		return
	}
	if !validateColor(req.PanelDarkTopbarColor, &current.PanelDarkTopbarColor, "panel_dark_topbar_color") {
		return
	}
	if !validateColor(req.PanelLightBgColor, &current.PanelLightBgColor, "panel_light_bg_color") {
		return
	}
	if !validateColor(req.PanelDarkBgColor, &current.PanelDarkBgColor, "panel_dark_bg_color") {
		return
	}
	if !validateColor(req.PanelLightContainerColor, &current.PanelLightContainerColor, "panel_light_container_color") {
		return
	}
	if !validateColor(req.PanelDarkContainerColor, &current.PanelDarkContainerColor, "panel_dark_container_color") {
		return
	}
	if !validateColor(req.PanelLightTextColor, &current.PanelLightTextColor, "panel_light_text_color") {
		return
	}
	if !validateColor(req.PanelDarkTextColor, &current.PanelDarkTextColor, "panel_dark_text_color") {
		return
	}
	if req.ReleaseChannel != nil {
		ch := strings.TrimSpace(*req.ReleaseChannel)
		if ch != "stable" && ch != "development" {
			c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{
				"error":   "invalid_release_channel",
				"message": "must be 'stable' or 'development'",
			})
			return
		}
		current.ReleaseChannel = ch
	}
	if req.DNSUserRecordPolicyPreset != nil {
		pol, ok := models.DNSPolicyPreset(*req.DNSUserRecordPolicyPreset)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnprocessableEntity, gin.H{
				"error":   "invalid_dns_policy_preset",
				"message": "must be 'permissive', 'hosting-safe', or 'locked-down'",
			})
			return
		}
		current.DNSUserRecordPolicy = pol
	} else if req.DNSUserRecordPolicy != nil {
		// Sanitize: drop NS/SOA and unknown types so a PUT can't grant a
		// policy for an admin-only record type.
		current.DNSUserRecordPolicy = req.DNSUserRecordPolicy.Sanitize()
	}
	if req.DiskQuotaEnabled != nil {
		current.DiskQuotaEnabled = *req.DiskQuotaEnabled
	}
	if req.WebmailEnabled != nil {
		current.WebmailEnabled = *req.WebmailEnabled
	}
	if req.DNSEnabled != nil {
		current.DNSEnabled = *req.DNSEnabled
	}
	if req.MailEnabled != nil {
		current.MailEnabled = *req.MailEnabled
	}
	if req.SecurityEnabled != nil {
		current.SecurityEnabled = *req.SecurityEnabled
	}
	if req.QuotaEnabled != nil {
		current.QuotaEnabled = *req.QuotaEnabled
	}
	if req.APIEnabled != nil {
		current.APIEnabled = *req.APIEnabled
	}
	if req.FTPEnabled != nil {
		current.FTPEnabled = *req.FTPEnabled
	}
	if req.FTPAllowPlaintext != nil {
		current.FTPAllowPlaintext = *req.FTPAllowPlaintext
	}
	if req.FTPPasvAddress != nil {
		addr := strings.TrimSpace(*req.FTPPasvAddress)
		// Lands in a root-rendered config file: hostname/IP charset only.
		if addr != "" && !hostnameRE.MatchString(addr) {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "validation_failed", "detail": "ftp_pasv_address must be a hostname or IP address"})
			return
		}
		current.FTPPasvAddress = addr
	}
	if req.FTPMaxClients != nil {
		if *req.FTPMaxClients > 100000 {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "validation_failed", "detail": "ftp_max_clients must be 0-100000"})
			return
		}
		current.FTPMaxClients = *req.FTPMaxClients
	}
	if req.FTPMaxPerIP != nil {
		if *req.FTPMaxPerIP > 10000 {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "validation_failed", "detail": "ftp_max_per_ip must be 0-10000"})
			return
		}
		current.FTPMaxPerIP = *req.FTPMaxPerIP
	}
	if req.FTPLocalMaxRateKBs != nil {
		if *req.FTPLocalMaxRateKBs > 10000000 {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": "validation_failed", "detail": "ftp_local_max_rate_kbs must be 0-10000000"})
			return
		}
		current.FTPLocalMaxRateKBs = *req.FTPLocalMaxRateKBs
	}
	if req.TenantDomainOptionsEnabled != nil {
		current.TenantDomainOptionsEnabled = *req.TenantDomainOptionsEnabled
	}
	if req.TenantDocrootEditable != nil {
		current.TenantDocrootEditable = *req.TenantDocrootEditable
	}
	if req.UnconfiguredPageEnabled != nil {
		current.UnconfiguredPageEnabled = *req.UnconfiguredPageEnabled
	}
	if req.AutomationApiPublicEnabled != nil {
		current.AutomationApiPublicEnabled = *req.AutomationApiPublicEnabled
	}
	if req.AdminFileManagerEnabled != nil {
		current.AdminFileManagerEnabled = *req.AdminFileManagerEnabled
	}
	if req.InterceptAppErrorsDefault != nil {
		current.InterceptAppErrorsDefault = *req.InterceptAppErrorsDefault
	}
	if req.DKIM2SigningEnabled != nil {
		current.DKIM2SigningEnabled = *req.DKIM2SigningEnabled
	}
	if req.TenantNotificationKinds != nil {
		// Sanitize drops unknown kinds, so a PATCH can never smuggle an
		// unsupported kind into the tenant allowlist.
		current.TenantNotificationKinds = req.TenantNotificationKinds.Sanitize()
	}
	if req.TenantNotificationsEnabled != nil {
		current.TenantNotificationsEnabled = *req.TenantNotificationsEnabled
	}
	if req.RootTerminalEnabled != nil {
		current.RootTerminalEnabled = *req.RootTerminalEnabled
	}
	if req.BandwidthQuotaEnforceEnabled != nil {
		current.BandwidthQuotaEnforceEnabled = *req.BandwidthQuotaEnforceEnabled
	}
	if req.UploadMaxSizeMB != nil {
		current.UploadMaxSizeMB = *req.UploadMaxSizeMB
	}
	if req.TenantBackupCron != nil {
		cronExpr := strings.TrimSpace(*req.TenantBackupCron)
		if _, err := internalbackup.ParseCron(cronExpr); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_cron", "detail": err.Error()})
			return
		}
		current.TenantBackupCron = cronExpr
	}
	if req.TenantBackupWindowStart != nil {
		v := strings.TrimSpace(*req.TenantBackupWindowStart)
		if !internalbackup.ValidHHMM(v) {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_backup_window", "detail": "tenant_backup_window_start must be HH:MM (00:00–23:59)"})
			return
		}
		current.TenantBackupWindowStart = v
	}
	if req.TenantBackupWindowEnd != nil {
		v := strings.TrimSpace(*req.TenantBackupWindowEnd)
		if !internalbackup.ValidHHMM(v) {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_backup_window", "detail": "tenant_backup_window_end must be HH:MM (00:00–23:59)"})
			return
		}
		current.TenantBackupWindowEnd = v
	}
	if req.TenantBackupWindowEnforce != nil {
		current.TenantBackupWindowEnforce = *req.TenantBackupWindowEnforce
	}
	if req.DefaultLocalBackupsEnabled != nil {
		current.DefaultLocalBackupsEnabled = *req.DefaultLocalBackupsEnabled
	}
	if req.SSHSandboxMode != nil {
		current.SSHSandboxMode = strings.TrimSpace(*req.SSHSandboxMode)
	}
	if req.DefaultNspawnImageVersion != nil {
		// Reject empty string — keeps UIs from clobbering a valid pin
		// when the field is hidden / not yet loaded on submit. To clear,
		// the operator must change the column DEFAULT in a migration.
		v := strings.TrimSpace(*req.DefaultNspawnImageVersion)
		if v != "" {
			current.DefaultNspawnImageVersion = v
		}
	}
	if req.BackupMaxConcurrentJobs != nil {
		// 0 acts as "use default" in the dispatcher. Cap at 64 — even a
		// single restic chain at 8 cores tops out the IO ceiling well
		// before then.
		v := *req.BackupMaxConcurrentJobs
		if v > 64 {
			v = 64
		}
		current.BackupMaxConcurrentJobs = v
	}
	if req.PostgresEnabled != nil {
		current.PostgresEnabled = *req.PostgresEnabled
	}
	if req.DockerMarketplaceEnabled != nil {
		current.DockerMarketplaceEnabled = *req.DockerMarketplaceEnabled
	}
	if req.DockerAppsForUsersEnabled != nil {
		current.DockerAppsForUsersEnabled = *req.DockerAppsForUsersEnabled
	}
	if req.DockerTenantApps != nil {
		current.DockerTenantApps = strings.TrimSpace(*req.DockerTenantApps)
	}
	if req.PythonAppsEnabled != nil {
		current.PythonAppsEnabled = *req.PythonAppsEnabled
	}
	if req.StalwartWebadminEnabled != nil {
		current.StalwartWebadminEnabled = *req.StalwartWebadminEnabled
	}
	if req.StalwartWebadminAllowCIDRs != nil {
		current.StalwartWebadminAllowCIDRs = strings.TrimSpace(*req.StalwartWebadminAllowCIDRs)
	}
	if req.MigrationAllowPrivateHosts != nil {
		current.MigrationAllowPrivateHosts = *req.MigrationAllowPrivateHosts
	}
	if req.PostgresMaxConnectionsPerUser != nil {
		v := *req.PostgresMaxConnectionsPerUser
		if v == 0 {
			v = 25
		}
		if v > 1000 {
			v = 1000
		}
		current.PostgresMaxConnectionsPerUser = v
	}
	if req.WorkingFolder != nil {
		v := strings.TrimSpace(*req.WorkingFolder)
		if v == "" {
			v = "/var/lib/jabali"
		}
		current.WorkingFolder = v
	}

	// M55 nginx tunables. nginxTouched drives the re-sync dispatch below.
	cacheCapTouched := req.NginxCacheMaxSizeGB != nil || req.NginxCacheKeyzoneMB != nil || req.NginxCacheInactiveMin != nil
	nginxTouched := req.NginxClientMaxBodySize != nil || req.NginxKeepaliveTimeout != nil ||
		req.NginxServerTokens != nil || req.NginxGzip != nil ||
		req.NginxClientBodyTimeout != nil || req.NginxClientHeaderTimeout != nil ||
		req.NginxSendTimeout != nil || req.NginxProxyConnectTimeout != nil ||
		req.NginxProxyReadTimeout != nil || req.NginxProxySendTimeout != nil ||
		req.NginxWorkerProcesses != nil || req.NginxWorkerConnections != nil ||
		req.NginxCustomHTTP != nil
	// The pre-merge nginx values live in `before` (captured at the top, before any
	// field merge — nginx fields are untouched until here). settingsops uses it to
	// classify the effect (changed vs explicit re-apply). The dispatch decision
	// below stays keyed on nginxTouched/cacheCapTouched, preserving re-sync.
	if req.NginxClientMaxBodySize != nil {
		current.NginxClientMaxBodySize = strings.TrimSpace(*req.NginxClientMaxBodySize)
	}
	if req.NginxKeepaliveTimeout != nil {
		current.NginxKeepaliveTimeout = strings.TrimSpace(*req.NginxKeepaliveTimeout)
	}
	if req.NginxServerTokens != nil {
		current.NginxServerTokens = *req.NginxServerTokens
	}
	if req.NginxGzip != nil {
		current.NginxGzip = *req.NginxGzip
	}
	if req.NginxClientBodyTimeout != nil {
		current.NginxClientBodyTimeout = strings.TrimSpace(*req.NginxClientBodyTimeout)
	}
	if req.NginxClientHeaderTimeout != nil {
		current.NginxClientHeaderTimeout = strings.TrimSpace(*req.NginxClientHeaderTimeout)
	}
	if req.NginxSendTimeout != nil {
		current.NginxSendTimeout = strings.TrimSpace(*req.NginxSendTimeout)
	}
	if req.NginxProxyConnectTimeout != nil {
		current.NginxProxyConnectTimeout = strings.TrimSpace(*req.NginxProxyConnectTimeout)
	}
	if req.NginxProxyReadTimeout != nil {
		current.NginxProxyReadTimeout = strings.TrimSpace(*req.NginxProxyReadTimeout)
	}
	if req.NginxProxySendTimeout != nil {
		current.NginxProxySendTimeout = strings.TrimSpace(*req.NginxProxySendTimeout)
	}
	if req.NginxWorkerProcesses != nil {
		current.NginxWorkerProcesses = strings.TrimSpace(*req.NginxWorkerProcesses)
	}
	if req.NginxWorkerConnections != nil {
		current.NginxWorkerConnections = *req.NginxWorkerConnections
	}
	if req.NginxCustomHTTP != nil {
		current.NginxCustomHTTP = strings.TrimSpace(*req.NginxCustomHTTP)
	}
	if req.NginxCacheMaxSizeGB != nil {
		current.NginxCacheMaxSizeGB = *req.NginxCacheMaxSizeGB
	}
	if req.NginxCacheKeyzoneMB != nil {
		current.NginxCacheKeyzoneMB = *req.NginxCacheKeyzoneMB
	}
	if req.NginxCacheInactiveMin != nil {
		current.NginxCacheInactiveMin = *req.NginxCacheInactiveMin
	}

	// Validate — reject obviously bad input so we don't persist garbage.
	if err := settingsops.Validate(current); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_settings", "detail": err.Error()})
		return
	}

	// Admin opt-in for tenant Docker apps. Enabling requires the engine; the
	// host tenant setup itself is dispatched AFTER persist (below), detached
	// from this request — see the background goroutine.
	if current.DockerAppsForUsersEnabled != before.DockerAppsForUsersEnabled &&
		current.DockerAppsForUsersEnabled && !current.DockerMarketplaceEnabled {
		c.JSON(http.StatusBadRequest, gin.H{"error": "docker_not_installed", "detail": "enable the Docker marketplace first"})
		return
	}

	if err := h.cfg.Repo.Upsert(ctx, current); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}

	// Apply hostname to the OS via agent, best-effort. If the agent isn't
	// reachable we still report success — the DB has the truth and a later
	// reconciliation can sync.
	if current.Hostname != prevHostname && h.cfg.Agent != nil {
		go func() {
			bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := h.cfg.Agent.Call(bgCtx, "system.set_hostname", map[string]any{
				"hostname": current.Hostname,
			}); err != nil {
				h.cfg.Log.Error("agent set_hostname failed", "err", err)
			}
		}()
		// JAB-389: the GH#135 dedicated :443 landing vhost keeps its server_name
		// on the old FQDN after a rename (install.sh renders it; nothing
		// re-renders it live), so https://<new-fqdn>/ falls to the return-444
		// default block. Re-point it to the new hostname. Dispatched
		// independently of set_hostname — the DB row is the truth, so the
		// landing vhost should track it even if hostnamectl failed.
		go func() {
			bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := h.cfg.Agent.Call(bgCtx, "nginx.panel_landing_rehost", map[string]any{
				"hostname": current.Hostname,
			}); err != nil {
				h.cfg.Log.Error("agent nginx.panel_landing_rehost failed", "err", err)
			}
		}()
	}

	// Optional-module lifecycle (JAB-294). settingsops.ModuleEffects is the single
	// owner of every optional-module transition table, agent verb/params/timeout,
	// and the failed-enable rollback decision (postgres, docker marketplace +
	// tenant, python, and the dns/mail/quota/security/ftp system.module.* loop).
	// The REST handler and the CLI both execute this one plan — the CLI consuming
	// only the subset it has setters for. This adapter dispatches each non-no-op
	// effect the REST way: detached, best-effort, one goroutine per effect, so a
	// slow apt install or a client disconnect never blocks or aborts the PATCH.
	// The DB row already holds operator intent; a failed apply reconciles on the
	// next pass. FTP disable is the fail-closed exception (JAB-259): it takes the
	// ftp.disable verb (not the fail-soft system.module.disable) and is reconciled
	// toward inactive+masked+ports-closed every tick by convergeFtpDisabled — a
	// disabled FTP that stays reachable is a security hole. For most modules there
	// is deliberately NO disable-convergence (stopping a "disabled" service every
	// tick would fight an operator troubleshooting a manual start).
	modulePlan := settingsops.ModuleEffects(&before, current)
	if h.cfg.Agent != nil {
		if call := modulePlan.Postgres.Call; call != nil {
			go h.dispatchSettingsCall(*call, "agent postgres lifecycle failed")
		}
		for _, me := range modulePlan.Modules {
			call := me.Call
			if call == nil {
				continue
			}
			logMsg := "agent module install failed"
			switch call.Method {
			case "system.module.disable":
				logMsg = "agent module disable failed"
			case "ftp.disable":
				logMsg = "ftp fail-closed disable failed (reconciler will retry)"
			}
			go h.dispatchSettingsCall(*call, logMsg)
		}
		// GH #1053: FTP config change while the module stays enabled re-renders
		// vsftpd.conf via the idempotent install.
		if call := modulePlan.FTPReapply.Call; call != nil {
			go h.dispatchSettingsCall(*call, "agent module install failed")
		}
	}

	// GH #200: brand text drives Bulwark webmail branding (app name +
	// login company name) server-wide. Push it when it changes; the
	// agent restarts jabali-webmail only on a real change.
	if current.PanelBrandText != prevPanelBrandText && h.cfg.Agent != nil {
		go func(brandText string) {
			bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := h.cfg.Agent.Call(bgCtx, "webmail.branding.apply", map[string]any{
				"brand_text": brandText,
			}); err != nil {
				h.cfg.Log.Warn("webmail branding sync failed", "err", err)
			}
		}(current.PanelBrandText)
	}

	// Optional-module lifecycle, continued: docker marketplace, tenant Docker, and
	// the Python runtime — the same settingsops plan. Tenant Docker additionally
	// reverts its persisted flag if an ENABLE fails (GH #272 — unprivileged LXC):
	// the module flags the decision (RevertOnFailure), this adapter runs the Repo
	// write via the shared dispatchReverting. Python installs on enable only;
	// disable leaves packages, which the module encodes as a no-op.
	if h.cfg.Agent != nil {
		if call := modulePlan.Docker.Call; call != nil {
			go h.dispatchSettingsCall(*call, "agent docker lifecycle failed")
		}
		if call := modulePlan.DockerTenant.Call; call != nil {
			var revert func(*models.ServerSettings)
			if modulePlan.DockerTenant.RevertOnFailure {
				revert = func(cur *models.ServerSettings) { cur.DockerAppsForUsersEnabled = false }
			}
			go h.dispatchReverting(*call, "agent docker.tenant_set failed", revert)
		}
		if call := modulePlan.Python.Call; call != nil {
			go h.dispatchSettingsCall(*call, "agent python runtime install failed")
		}
	}

	// Apply timezone to the OS via agent if changed and not empty.
	if current.Timezone != prevTimezone && current.Timezone != "" && h.cfg.Agent != nil {
		go func() {
			bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := h.cfg.Agent.Call(bgCtx, "system.set_timezone", map[string]any{
				"timezone": current.Timezone,
			}); err != nil {
				h.cfg.Log.Error("agent set_timezone failed", "err", err)
			}
		}()
	}

	// SSH config + sandbox transitions (JAB-295). settingsops.SSHEffects is the
	// single owner of both agent verbs, their params, timeouts, and the two
	// distinct detection semantics:
	//   - system.set_ssh_config rewrites /etc/ssh/sshd_config.d/jabali-sshd.conf
	//     (global, affects root/admin) + jabali-sftp.conf (M12 Match Group, hosting
	//     users), validates with `sshd -t`, and reloads (ADR-0028). It is
	//     touched-based: re-saving ANY ssh field re-syncs the file even when the
	//     value is unchanged, so an operator can heal a drifted file (DB=1, file=no)
	//     by re-submitting. The startup reconcile in serve.go covers boot; this
	//     covers the operator-driven re-sync.
	//   - system.set_ssh_sandbox_mode writes the mode + default-image files the
	//     connect wrapper reads on every exec (no reload). It is value-diff: it
	//     fires only on a genuine change.
	// AC5: a real change (Kind == Changed) is flagged RevertOnFailure, so a failed
	// apply reverts the persisted SSH fields to their pre-merge values — the agent
	// rolls the file back on `sshd -t` failure, and this keeps the DB from claiming
	// a security state the host never accepted. The revert body is this adapter's
	// (a repo write against its own handle), driven by the module's decision.
	sshPlan := settingsops.SSHEffects(&before, current, settingsops.SSHTouched{
		Config: req.SSHPort != nil || req.SSHPasswordAuth != nil || req.SSHUserPasswordAuth != nil,
	})
	if h.cfg.Agent != nil {
		if call := sshPlan.Config.Call; call != nil {
			var revert func(*models.ServerSettings)
			if sshPlan.Config.RevertOnFailure {
				revert = func(cur *models.ServerSettings) {
					cur.SSHPort = before.SSHPort
					cur.SSHPasswordAuth = before.SSHPasswordAuth
					cur.SSHUserPasswordAuth = before.SSHUserPasswordAuth
				}
			}
			go h.dispatchReverting(*call, "agent set_ssh_config failed", revert)
		}
		if call := sshPlan.Sandbox.Call; call != nil {
			var revert func(*models.ServerSettings)
			if sshPlan.Sandbox.RevertOnFailure {
				revert = func(cur *models.ServerSettings) {
					cur.SSHSandboxMode = before.SSHSandboxMode
					cur.DefaultNspawnImageVersion = before.DefaultNspawnImageVersion
				}
			}
			go h.dispatchReverting(*call, "agent set_ssh_sandbox_mode failed", revert)
		}
	}

	// CrowdSec sensitivity preset: re-apply when either the operator
	// explicitly touched the field OR the persisted level diverged
	// from what was on disk (drift after a manual edit / reset).
	if (req.CrowdsecSensitivity != nil || current.CrowdsecSensitivity != prevCrowdsecSensitivity) && h.cfg.Agent != nil {
		go func() {
			bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := h.cfg.Agent.Call(bgCtx, "security.crowdsec.sensitivity.apply", map[string]any{
				"level": current.CrowdsecSensitivity,
			}); err != nil {
				h.cfg.Log.Error("agent crowdsec sensitivity apply failed", "err", err)
				// GH #710: the apply did not reach the host. The agent apply is
				// reload-committed (writes are atomic + the reload is the LAST
				// step, so a failed apply never reloads and the host stays on the
				// PREVIOUS level). Revert the persisted level so the DB/UI stop
				// claiming a preset that is not live — closes the drift the QA
				// flagged, without a schema change.
				rctx, rcancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer rcancel()
				if cur, gerr := h.cfg.Repo.Get(rctx); gerr == nil && cur != nil {
					cur.CrowdsecSensitivity = prevCrowdsecSensitivity
					if uerr := h.cfg.Repo.Upsert(rctx, cur); uerr != nil {
						h.cfg.Log.Error("revert crowdsec sensitivity after failed apply", "err", uerr)
					}
				}
			}
		}()
	}

	// CrowdSec nginx-bouncer mode: rewrite MODE= + reload nginx when the
	// operator flipped the preset (or the persisted value diverged from
	// what's on disk after a manual edit).
	if (req.CrowdsecBouncerMode != nil || current.CrowdsecBouncerMode != prevCrowdsecBouncerMode) && h.cfg.Agent != nil {
		go func() {
			bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := h.cfg.Agent.Call(bgCtx, "security.crowdsec.bouncer.mode.apply", map[string]any{
				"mode": current.CrowdsecBouncerMode,
			}); err != nil {
				h.cfg.Log.Error("agent crowdsec bouncer mode apply failed", "err", err)
			}
		}()
	}

	// GH #598 auto-allowlist-on-login: push {enabled, ttl_hours} to the agent's
	// login-allowlist.conf (the SSH watcher reads it) whenever the operator
	// touched either field or the persisted value diverged from disk. The panel
	// middleware reads the DB directly, so it needs no push.
	if (req.CrowdsecLoginAllowlistEnabled != nil || req.CrowdsecLoginAllowlistTTLHours != nil ||
		current.CrowdsecLoginAllowlistEnabled != prevCrowdsecLoginAllowlistEnabled ||
		current.CrowdsecLoginAllowlistTTLHours != prevCrowdsecLoginAllowlistTTLHours) && h.cfg.Agent != nil {
		go func() {
			bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := h.cfg.Agent.Call(bgCtx, "security.crowdsec.login_allowlist.apply", map[string]any{
				"enabled":   current.CrowdsecLoginAllowlistEnabled,
				"ttl_hours": current.CrowdsecLoginAllowlistTTLHours,
			}); err != nil {
				h.cfg.Log.Error("agent crowdsec login_allowlist apply failed", "err", err)
			}
		}()
	}

	// M55 nginx tunables: re-render the http-scope fragment + patch worker
	// lines whenever the operator touched the Nginx form. Background dispatch
	// (atomic-write + nginx -t gate + reload happen agent-side). Re-sync
	// semantics: re-saving the form always re-applies, healing on-disk drift.
	// M55/GH #634: derive the nginx settings effect plan (tunables + FastCGI
	// cache capacity) in settingsops — the single owner of the agent wire,
	// shared with the CLI (JAB-290) — and dispatch each touched verb the REST
	// way: async, detached, best-effort (the DB already holds the truth; a later
	// reconcile syncs on failure). Dispatch stays keyed on Touched, so the
	// re-sync semantics are unchanged.
	nginxPlan := settingsops.NginxEffects(&before, current, settingsops.NginxTouched{
		Tunables: nginxTouched,
		Cache:    cacheCapTouched,
	})
	if h.cfg.Agent != nil {
		if call := nginxPlan.Tunables.Call; call != nil {
			go h.dispatchSettingsCall(*call, "agent nginx tunables apply failed")
		}
		if call := nginxPlan.Cache.Call; call != nil {
			go h.dispatchSettingsCall(*call, "agent nginx cache capacity apply failed")
		}
	}

	h.cfg.Log.Info("event=audit kind=server_settings_updated actor_id=" + claims.UserID)
	// GH #243: apply the Stalwart WebAdmin reverse-proxy SYNCHRONOUSLY when the
	// toggle or allowlist changed. Stalwart itself stays loopback-pinned; the
	// gate is TLS + the IP allowlist + Stalwart's own admin login (ADR-0142).
	webadminChanged := current.StalwartWebadminEnabled != prevWebadminEnabled ||
		current.StalwartWebadminAllowCIDRs != prevWebadminCIDRs
	if webadminChanged && h.cfg.Agent != nil {
		if aerr := h.applyStalwartWebadmin(ctx, current); aerr != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "webadmin_apply_failed", "detail": firstLineString(aerr.Error())})
			return
		}
	}

	c.JSON(http.StatusOK, current)
}

// dispatchSettingsCall executes one settingsops AgentCall the REST way: detached
// from the request (a client disconnect must not cancel the apply), bounded by
// the call's own timeout, best-effort (failure is logged, not surfaced — the DB
// already holds the truth and a later reconcile syncs). Run it in a goroutine.
// Shared by the nginx and optional-module dispatches (JAB-290 / JAB-294).
func (h *serverSettingsHandler) dispatchSettingsCall(call settingsops.AgentCall, logMsg string) {
	bgCtx, cancel := context.WithTimeout(context.Background(), call.Timeout)
	defer cancel()
	if _, err := h.cfg.Agent.Call(bgCtx, call.Method, call.Params); err != nil {
		// Log method + params so the module key / postgres|docker method survives
		// (a superset of the pre-refactor per-verb attributes).
		h.cfg.Log.Error(logMsg, "method", call.Method, "params", call.Params, "err", err)
	}
}

// dispatchReverting runs a settings AgentCall detached (like dispatchSettingsCall)
// and, when revert is non-nil and the call fails, rolls the persisted row back so
// the DB never claims a state the host rejected. The module owns the decision
// (whether an effect reverts and in which direction — Effect.RevertOnFailure); the
// caller supplies revert, which mutates the freshly-read row in place with the
// fields this effect owns. The revert is a persistence write against the handler's
// own repo handle, so it cannot be an agent Rollback. Used by tenant Docker
// (GH #272) and the SSH config + sandbox verbs (JAB-295, AC5). Run in a goroutine;
// the revert uses its own short-lived context so a call-timeout failure can still
// persist the rollback.
func (h *serverSettingsHandler) dispatchReverting(call settingsops.AgentCall, logMsg string, revert func(*models.ServerSettings)) {
	bgCtx, cancel := context.WithTimeout(context.Background(), call.Timeout)
	defer cancel()
	if _, err := h.cfg.Agent.Call(bgCtx, call.Method, call.Params); err != nil {
		h.cfg.Log.Error(logMsg, "params", call.Params, "err", err)
		if revert == nil {
			return
		}
		rctx, rcancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer rcancel()
		if cur, gerr := h.cfg.Repo.Get(rctx); gerr == nil && cur != nil {
			revert(cur)
			if uerr := h.cfg.Repo.Upsert(rctx, cur); uerr != nil {
				h.cfg.Log.Error("revert settings after failed apply", "method", call.Method, "err", uerr)
			}
		}
	}
}

// hostnameRE validates a bare hostname/IP charset. Server-settings validation
// moved to internal/settingsops (JAB-290), but this handler still uses the
// pattern directly for the FTP passive-address field (which lands verbatim in a
// root-rendered config file), so the regex stays local to the api package.
var hostnameRE = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$`)

// isImageNamePattern matches the [a-z0-9-]+ shape used everywhere
// from agent helpers to wrapper scripts. Kept inline here so the
// file doesn't depend on regexp at build time for one validation.
func isImageNamePattern(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return false
		}
	}
	return true
}

// pythonRuntimeStatus reports whether the Python app runtime prerequisites
// are installed, so the Apps-tab card can poll after enabling (ADR-0131).
func (h *serverSettingsHandler) pythonRuntimeStatus(c *gin.Context) {
	if h.cfg.Agent == nil {
		c.JSON(http.StatusOK, gin.H{"installed": false})
		return
	}
	raw, err := h.cfg.Agent.Call(c.Request.Context(), "app.python.runtime_status", map[string]any{})
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"installed": false})
		return
	}
	c.Data(http.StatusOK, "application/json", raw)
}

// --- GH #243: Stalwart WebAdmin reverse-proxy ---

// stalwartWebadminPort is the dedicated HTTPS port the WebAdmin proxy listens
// on (panel hostname, panel cert). Distinct from the panel :8443.
const stalwartWebadminPort = 8449

// stalwartAdminCredentialResponse carries the Stalwart admin GUI login. The
// password is the recovery-admin token — the same credential the panel uses to
// manage Stalwart — so a rotate restarts jabali-stalwart + jabali-panel.
type stalwartAdminCredentialResponse struct {
	User     string `json:"user"`
	Password string `json:"password"`
	Rotated  bool   `json:"rotated"`
	URL      string `json:"url"`
}

// stalwartAdminCredential reveals or rotates the Stalwart admin GUI login.
// POST /admin/settings/stalwart-admin-credential {action:"reveal"|"rotate"}.
func (h *serverSettingsHandler) stalwartAdminCredential(c *gin.Context) {
	ctx := c.Request.Context()
	var req struct {
		Action string `json:"action"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	if req.Action != "reveal" && req.Action != "rotate" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_action", "detail": "action must be reveal or rotate"})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(callCtx, "mail.admin_cred.manage", map[string]any{"action": req.Action})
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "admin_cred_failed", "detail": firstLineString(err.Error())})
		return
	}
	var resp struct {
		User     string `json:"user"`
		Password string `json:"password"`
		Rotated  bool   `json:"rotated"`
	}
	if uerr := json.Unmarshal(raw, &resp); uerr != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "admin_cred_decode"})
		return
	}
	hostname := ""
	if s, gerr := h.cfg.Repo.Get(ctx); gerr == nil && s != nil {
		hostname = s.Hostname
	}
	c.JSON(http.StatusOK, stalwartAdminCredentialResponse{
		User:     resp.User,
		Password: resp.Password,
		Rotated:  resp.Rotated,
		URL:      fmt.Sprintf("https://%s:%d/", hostname, stalwartWebadminPort),
	})
}

// applyStalwartWebadmin dispatches mail.webadmin.apply with the effective
// state. Serves on a dedicated port of the panel hostname, reusing the panel
// cert; Stalwart stays loopback. No gateway credential — the gate is TLS + the
// IP allowlist + Stalwart's own admin login (ADR-0142).
func (h *serverSettingsHandler) applyStalwartWebadmin(ctx context.Context, s *models.ServerSettings) error {
	cidrs := splitCIDRList(s.StalwartWebadminAllowCIDRs)
	params := map[string]any{
		"enabled":       s.StalwartWebadminEnabled,
		"server_name":   s.Hostname,
		"port":          stalwartWebadminPort,
		"ssl_cert_path": "/etc/jabali/tls/panel.crt",
		"ssl_key_path":  "/etc/jabali/tls/panel.key",
		"allow_cidrs":   cidrs,
	}
	callCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	_, err := h.cfg.Agent.Call(callCtx, "mail.webadmin.apply", params)
	return err
}

// splitCIDRList parses a comma/space-separated allowlist into a clean slice.
func splitCIDRList(in string) []string {
	out := []string{}
	for _, f := range strings.FieldsFunc(in, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' }) {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// hexColorRE matches an empty string (use default) or a #rgb / #rrggbb /
// #rrggbbaa hex color — the accepted shapes for the "Look and feel" panel
// colors.
var hexColorRE = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6}|[0-9a-fA-F]{8})$`)

func isValidHexColor(v string) bool { return v == "" || hexColorRE.MatchString(v) }

// dispatchModuleInstall runs system.module.install for a module key in the
// background. apt can take minutes, so the PATCH must not block on it. The DB
// flag is already persisted (operator intent recorded), so a failed install
// surfaces via the module status endpoint and is retryable from the UI; we
// deliberately never roll the flag back here (that would reproduce the exact
// "flag On / service down" state the operator can't escape). Serialization
// against concurrent installs is enforced agent-side behind aptMu.
func (h *serverSettingsHandler) dispatchModuleInstall(key string) {
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if _, err := h.cfg.Agent.Call(bgCtx, "system.module.install", map[string]any{"key": key}); err != nil {
			h.cfg.Log.Error("agent module install failed", "key", key, "err", err)
		}
	}()
}

// The module-disable and fail-closed FTP-disable transitions now flow through
// settingsops.ModuleEffects + dispatchSettingsCall (JAB-294); the standalone
// dispatchModuleDisable / dispatchFtpDisable helpers were retired. Install still
// has its own helper because the module status endpoint re-dispatches it.

// installableModules are the module keys with a runtime install path (mirrors
// install.sh's --install-module dispatcher + the agent allowlist). api_enabled
// is flag-only (no packages) and is intentionally absent.
var installableModules = []string{"dns", "mail", "security", "quota"}

func isInstallableModule(key string) bool {
	for _, k := range installableModules {
		if k == key {
			return true
		}
	}
	return false
}

type moduleStatusEntry struct {
	Installed bool `json:"installed"`
	Active    bool `json:"active"`
}

// moduleStatus proxies the agent's system.module.status for every installable
// module so the Modules card can render installing / active / failed states and
// poll a just-toggled module until it comes up. Probes are cheap; a missing
// agent yields an empty map (the card falls back to flag-only display).
func (h *serverSettingsHandler) moduleStatus(c *gin.Context) {
	out := map[string]moduleStatusEntry{}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusOK, gin.H{"modules": out})
		return
	}
	ctx := c.Request.Context()
	for _, key := range installableModules {
		sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		raw, err := h.cfg.Agent.Call(sctx, "system.module.status", map[string]any{"key": key})
		cancel()
		if err != nil {
			continue // agent hiccup — omit this key; the UI treats absent as unknown
		}
		var e moduleStatusEntry
		if err := json.Unmarshal(raw, &e); err != nil {
			continue
		}
		out[key] = e
	}
	c.JSON(http.StatusOK, gin.H{"modules": out})
}

// ftpStatus returns the observed-vs-desired FTP posture (JAB-259/260 phase C).
// The FTP card renders a drift warning when the host disagrees with the panel:
//   - exposure: FTP is desired OFF but vsftpd is still active (a fail-open
//     disable — JAB-259).
//   - tls: FTP is desired ON + secure but the live daemon still accepts plaintext
//     (a silently-failed tighten — JAB-260).
//
// A missing/unreachable agent yields effective=null + no drift; the UI treats an
// absent effective state as "unknown", never as a confirmed secure state.
func (h *serverSettingsHandler) ftpStatus(c *gin.Context) {
	ctx := c.Request.Context()
	s, err := h.cfg.Repo.Get(ctx)
	if errors.Is(err, repository.ErrNotFound) {
		s = &models.ServerSettings{ID: 1}
	} else if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal"})
		return
	}
	desired := gin.H{"enabled": s.FTPEnabled, "allow_plaintext": s.FTPAllowPlaintext}
	noDrift := gin.H{"exposure": false, "tls": false}

	if h.cfg.Agent == nil {
		c.JSON(http.StatusOK, gin.H{"desired": desired, "effective": nil, "drift": noDrift})
		return
	}
	sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	raw, err := h.cfg.Agent.Call(sctx, "ftp.status", map[string]any{})
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"desired": desired, "effective": nil, "drift": noDrift})
		return
	}
	var eff struct {
		ConfExists  bool `json:"conf_exists"`
		SSLEnforced bool `json:"ssl_enforced"`
		Active      bool `json:"active"`
		Masked      bool `json:"masked"`
		PortsOpen   bool `json:"ports_open"`
	}
	if err := json.Unmarshal(raw, &eff); err != nil {
		c.JSON(http.StatusOK, gin.H{"desired": desired, "effective": nil, "drift": noDrift})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"desired":   desired,
		"effective": eff,
		"drift": gin.H{
			// exposure (error): FTP off but vsftpd still serving (JAB-259).
			"exposure": !s.FTPEnabled && eff.Active,
			// tls (error): FTP on + secure but the live daemon accepts plaintext (JAB-260).
			"tls": s.FTPEnabled && !s.FTPAllowPlaintext && eff.Active && !eff.SSLEnforced,
			// ports (warning): FTP off, daemon stopped, but the firewall still
			// allows the FTP ports. Not exposure (nothing is listening) but a
			// leftover rule worth flagging.
			"ports": !s.FTPEnabled && !eff.Active && eff.PortsOpen,
		},
	})
}

// moduleInstall re-dispatches a module install in the background. It is the UI's
// retry affordance: because a failed install never rolls the flag back, the
// operator needs a way to re-trigger the install without toggling off/on. The
// module flag must already be enabled — this only (re)installs, it does not flip
// the flag.
func (h *serverSettingsHandler) moduleInstall(c *gin.Context) {
	var req struct {
		Key string `json:"key"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	if !isInstallableModule(req.Key) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unknown_module", "detail": "want: dns|mail|security|quota"})
		return
	}
	if h.cfg.Agent == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "agent_unavailable"})
		return
	}
	h.dispatchModuleInstall(req.Key)
	c.JSON(http.StatusAccepted, gin.H{"status": "installing", "key": req.Key})
}

// validateLogRetention checks a Server Settings -> Logs retention map: every key
// must be a known category and every value an int in [0, 3650] days (0 = keep
// forever). Rejects unknown keys so a typo can't silently no-op.
func validateLogRetention(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var m map[string]int
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("log_retention must be a { category: days } object: %w", err)
	}
	for k, v := range m {
		if _, ok := models.DefaultLogRetentionDays[k]; !ok {
			return fmt.Errorf("unknown log-retention category %q", k)
		}
		if v < 0 || v > 3650 {
			return fmt.Errorf("log-retention days for %q must be 0..3650, got %d", k, v)
		}
	}
	return nil
}
