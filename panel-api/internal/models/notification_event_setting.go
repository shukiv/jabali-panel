package models

import "time"

// NotificationEventSetting is a per-event-kind enable toggle. The
// dispatcher consults this table on every Publish and skips
// envelopes whose event_kind is disabled. Operators edit values
// from the admin Notifications → Events tab.
type NotificationEventSetting struct {
	EventKind string    `gorm:"column:event_kind;primaryKey;type:varchar(60)" json:"event_kind"`
	Enabled   bool      `gorm:"column:enabled;type:tinyint(1);not null;default:0" json:"enabled"`
	UpdatedAt time.Time `gorm:"column:updated_at;type:datetime(6);not null" json:"updated_at"`
}

func (NotificationEventSetting) TableName() string { return "notification_event_settings" }

// NotificationEventKindMeta holds the static metadata for every
// event kind the dispatcher knows about — display label, short
// description, default-on flag, and severity colour. Used for both
// the EnsureDefaults seed and the admin UI list endpoint.
type NotificationEventKindMeta struct {
	Kind        string
	Label       string
	Description string
	Severity    string // info | warning | error | critical
	DefaultOn   bool
}

// AllNotificationEventKinds — the canonical, ordered list. Add new
// kinds here AND wire a publisher that fires them; the seed picks
// up new kinds with their declared default on next boot.
var AllNotificationEventKinds = []NotificationEventKindMeta{
	{
		Kind:        "cert.renew.fail",
		Label:       "SSL renewal failed",
		Description: "ACME renewal failed — issued via panel certbot. Action usually required.",
		Severity:    "error",
		DefaultOn:   true,
	},
	{
		Kind:        "cert.renew.ok",
		Label:       "SSL renewal succeeded",
		Description: "ACME renewal completed. Informational confirmation.",
		Severity:    "info",
		DefaultOn:   false,
	},
	{
		Kind:        "domain.expiry.7d",
		Label:       "SSL cert expiring in 7 days",
		Description: "Cert crosses the 7-day pre-expiry window without a fresh renewal.",
		Severity:    "warning",
		DefaultOn:   true,
	},
	{
		Kind:        "domain.expiry.1d",
		Label:       "SSL cert expiring in 1 day",
		Description: "Cert crosses the 1-day pre-expiry window — imminent expiry.",
		Severity:    "error",
		DefaultOn:   true,
	},
	{
		Kind:        "disk.full.warn",
		Label:       "Disk usage 80%",
		Description: "System disk passed the 80% threshold. Cleanup recommended.",
		Severity:    "warning",
		DefaultOn:   false,
	},
	{
		Kind:        "disk.full.crit",
		Label:       "Disk usage 95%",
		Description: "System disk passed the 95% threshold — services will fail soon.",
		Severity:    "critical",
		DefaultOn:   true,
	},
	{
		Kind:        "disk.quota.warn",
		Label:       "User reached 90% quota",
		Description: "A hosting user crossed 90% of their disk quota.",
		Severity:    "warning",
		DefaultOn:   true,
	},
	{
		Kind:        "load.high",
		Label:       "High server load",
		Description: "1-minute load average exceeded the configured threshold.",
		Severity:    "warning",
		DefaultOn:   false,
	},
	{
		Kind:        "system.update.available",
		Label:       "System updates available",
		Description: "apt has new package upgrades waiting (security or otherwise).",
		Severity:    "info",
		DefaultOn:   false,
	},
	{
		Kind:        "docker_app.update_available",
		Label:       "Docker app update available",
		Description: "Marketplace poller detected a newer upstream image digest for an installed Docker app.",
		Severity:    "info",
		DefaultOn:   true,
	},
	{
		Kind:        "service.down",
		Label:       "Service down or restarted",
		Description: "A managed service unit (nginx, php-fpm, mariadb, …) failed or was restarted.",
		Severity:    "error",
		DefaultOn:   true,
	},
	{
		Kind:        "crowdsec.ban.spike",
		Label:       "CrowdSec ban spike",
		Description: "Unusually large burst of new bans within a short window.",
		Severity:    "warning",
		DefaultOn:   false,
	},
	{
		Kind:        "backup.fail",
		Label:       "Backup failed",
		Description: "A scheduled backup job exited non-zero.",
		Severity:    "error",
		DefaultOn:   true,
	},
	{
		Kind:        "backup.limit.reached",
		Label:       "Tenant backup limit reached",
		Description: "A tenant hit their package's max_backups cap. Depending on the package retention policy the backup was blocked (reject) or the oldest was auto-pruned (prune). Notifies the tenant and admins (GH #454).",
		Severity:    "warning",
		DefaultOn:   true,
	},
	{
		Kind:        "admin.login",
		Label:       "Admin signed in",
		Description: "First request of a new Kratos admin session.",
		Severity:    "info",
		DefaultOn:   true,
	},
	{
		Kind:        "ssh.login",
		Label:       "SSH login",
		Description: "Successful SSH authentication for a panel-managed user.",
		Severity:    "info",
		DefaultOn:   true,
	},
	{
		Kind:        "notifications.channel.auto_disabled",
		Label:       "Channel auto-disabled",
		Description: "A notification channel was disabled after repeated send failures.",
		Severity:    "warning",
		DefaultOn:   true,
	},
	{
		Kind:        "panel.welcome",
		Label:       "Welcome",
		Description: "Fired once at install time for the bootstrap admin to point them at notification setup.",
		Severity:    "info",
		DefaultOn:   true,
	},
	{
		Kind:        "security.root_terminal.opened",
		Label:       "Root web terminal opened",
		Description: "An admin opened the in-panel root shell (M45). Highest-trust action; every byte is recorded to /var/log/jabali/terminal/<id>.cast.",
		Severity:    "critical",
		DefaultOn:   true,
	},
	{
		Kind:        "security.decision.fired",
		Label:       "Security decision fired",
		Description: "Aggregated drop/throttle from any decision brain (UFW, nginx limit_req, CrowdSec). One envelope per polling window when activity exceeds 0.",
		Severity:    "info",
		DefaultOn:   false,
	},
	{
		Kind:        "snuffleupagus.incident.detected",
		Label:       "PHP Defense incident",
		Description: "PHP Defense rule fired on a tenant request (block / simulated block / log).",
		Severity:    "warning",
		DefaultOn:   true,
	},
	// M37 PostgreSQL parity (ADR-0091).
	{
		Kind:        "postgres.service_down",
		Label:       "PostgreSQL service down",
		Description: "postgresql.service is enabled but inactive — running connections fail.",
		Severity:    "error",
		DefaultOn:   true,
	},
	{
		Kind:        "postgres.disk_high",
		Label:       "PostgreSQL data dir disk high",
		Description: "/var/lib/postgresql usage above 85%.",
		Severity:    "warning",
		DefaultOn:   true,
	},
	{
		Kind:        "postgres.connections_exhausted",
		Label:       "PostgreSQL connections exhausted",
		Description: "Active connection count above 90% of max_connections — new clients will be refused.",
		Severity:    "error",
		DefaultOn:   true,
	},
	{
		Kind:        "agent.dispatch.failure",
		Label:       "Agent dispatch failure",
		Description: "panel-api → agent RPC returned an internal error. One envelope per error bucket per 30-min window. Common causes: systemctl/DBus permission issues, missing helper binaries, exit-status leaks from wrapped commands.",
		Severity:    "warning",
		DefaultOn:   true,
	},
	{
		Kind:        "reconciler.error",
		Label:       "Reconciler error",
		Description: "A ReconcileAll pass logged a level=ERROR. Could be a domain rebuild that crashed, an SSL cert provisioning rollback, a managed-IP rebind that the kernel rejected, etc.",
		Severity:    "warning",
		DefaultOn:   true,
	},
	{
		Kind:        "agent.unreachable",
		Label:       "Agent unreachable",
		Description: "panel-api couldn't reach /run/jabali/agent.sock. Usually means jabali-agent.service crashed or is restart-looping; check `systemctl status jabali-agent`.",
		Severity:    "error",
		DefaultOn:   true,
	},
	{
		Kind:        "notifications.dlq.nonzero",
		Label:       "Notifications DLQ non-empty",
		Description: "The M14 dead-letter queue has unprocessed envelopes. Means at least one channel send (Slack/email/web push) failed after the dispatcher retry budget. DLQ is a marker of channel rot — operator should inspect and either reset or disable the offending channel.",
		Severity:    "warning",
		DefaultOn:   true,
	},
	{
		Kind:        "panel.api.error",
		Label:       "panel-api 5xx",
		Description: "An HTTP request to /api/v1/* returned 5xx. Bucketed per endpoint+status pair; one envelope per bucket per 30-min window.",
		Severity:    "warning",
		DefaultOn:   false,
	},
	{
		Kind:        "bandwidth.quota.warn",
		Label:       "Bandwidth quota — 80%",
		Description: "User crossed 80% of their package's monthly BandwidthQuotaMB. Per-user dedupe via 6h cooldown.",
		Severity:    "warning",
		DefaultOn:   true,
	},
	{
		Kind:        "bandwidth.quota.crit",
		Label:       "Bandwidth quota — 100%",
		Description: "User crossed their package's monthly BandwidthQuotaMB. v1 does not auto-suspend; admin decides.",
		Severity:    "critical",
		DefaultOn:   true,
	},
	// GH #840: opt-in Zimbra/Logwatch-style daily summary. Aggregated by
	// the digest ticker; off by default so it never surprises anyone.
	{
		Kind:        "digest.daily",
		Label:       "Daily digest",
		Description: "One summary email a day: alerts by severity, backup outcomes, certificates expiring soon, and fleet counts for the last 24 hours.",
		Severity:    "info",
		DefaultOn:   false,
	},
	// GH #979: expose the remaining kinds that publishers already fire but that
	// were never listed here, so the admin Notifications → Events tab can toggle
	// them (an unlisted kind falls back to always-on and cannot be silenced).
	// Policy: actionable/security events default ON, purely informational
	// confirmations default OFF (matches the info=off convention above).
	// `notifications.broadcast` and `notifications.channel.test` are deliberately
	// NOT here — a broadcast must always deliver and a channel test only fires on
	// an explicit click, so both stay always-on.
	{
		Kind:        "update.completed",
		Label:       "Panel update applied",
		Description: "The panel finished updating to a new release. Informational; noisy on the development channel.",
		Severity:    "info",
		DefaultOn:   false,
	},
	{
		Kind:        "aide.tamper.detected",
		Label:       "File integrity tamper",
		Description: "AIDE detected an unexpected change to a monitored system file. Security-relevant — investigate.",
		Severity:    "error",
		DefaultOn:   true,
	},
	{
		Kind:        "nginx.config.invalid",
		Label:       "nginx config invalid",
		Description: "A rendered nginx configuration failed validation and was not applied. Action required.",
		Severity:    "error",
		DefaultOn:   true,
	},
	{
		Kind:        "malware.realtime.critical",
		Label:       "Malware — critical detection",
		Description: "The real-time scanner flagged a high-confidence malicious file. Security-relevant — investigate.",
		Severity:    "critical",
		DefaultOn:   true,
	},
	{
		Kind:        "malware.quarantine.added",
		Label:       "Malware quarantined",
		Description: "A file was quarantined by the malware scanner. Security-relevant.",
		Severity:    "warning",
		DefaultOn:   true,
	},
	{
		Kind:        "egress.drop.burst",
		Label:       "Outbound traffic blocked (burst)",
		Description: "A burst of outbound connections was dropped by the egress firewall. Possible compromise or misconfiguration.",
		Severity:    "warning",
		DefaultOn:   true,
	},
	{
		Kind:        "exec.audit.burst",
		Label:       "Suspicious process burst",
		Description: "The exec-audit source saw a burst of flagged process executions. Security-relevant.",
		Severity:    "warning",
		DefaultOn:   true,
	},
	{
		Kind:        "domain.ghost_detected.mismatch",
		Label:       "Ghosted domain — IP mismatch",
		Description: "The domain resolves to a different IP than this server. Expected behind a proxy/CDN such as Cloudflare — disable if you front domains that way.",
		Severity:    "warning",
		DefaultOn:   true,
	},
	{
		Kind:        "domain.ghost_detected.nxdomain",
		Label:       "Ghosted domain — does not resolve",
		Description: "The domain does not resolve in public DNS. It may be newly added or its DNS is not yet live.",
		Severity:    "warning",
		DefaultOn:   true,
	},
	{
		Kind:        "domain.ghost_detected.partial",
		Label:       "Ghosted domain — partial DNS",
		Description: "Only some of the domain's expected records point here. DNS may be mid-propagation or misconfigured.",
		Severity:    "warning",
		DefaultOn:   true,
	},
	{
		Kind:        "mail.rbl.listed",
		Label:       "Mail IP blocklisted (RBL)",
		Description: "The server's sending IP was found on a DNS blocklist. Outbound mail may be rejected — action required.",
		Severity:    "error",
		DefaultOn:   true,
	},
	{
		Kind:        "mail.rbl.cleared",
		Label:       "Mail IP delisted (RBL)",
		Description: "The server's sending IP is no longer on a previously-seen blocklist. Informational.",
		Severity:    "info",
		DefaultOn:   false,
	},
	{
		Kind:        "mail.dmarc.report_received",
		Label:       "DMARC aggregate report received",
		Description: "An aggregate DMARC (RUA) report arrived for one of your domains. Informational.",
		Severity:    "info",
		DefaultOn:   false,
	},
	{
		Kind:        "mail.tls.report_received",
		Label:       "SMTP TLS report received",
		Description: "An SMTP TLS-RPT report arrived for one of your domains. Informational.",
		Severity:    "info",
		DefaultOn:   false,
	},
	{
		Kind:        "mail.feedback.received",
		Label:       "Mail feedback-loop report",
		Description: "A feedback-loop / abuse report was received about outbound mail. Informational.",
		Severity:    "info",
		DefaultOn:   false,
	},
	{
		Kind:        "docker_app.entitlement_stopped",
		Label:       "Docker app stopped — entitlement",
		Description: "A tenant Docker app was stopped because its plan no longer entitles it. Action may be required.",
		Severity:    "warning",
		DefaultOn:   true,
	},
	{
		Kind:        "docker_app.disk_quota_stopped",
		Label:       "Docker app stopped — disk quota",
		Description: "A tenant Docker app was stopped after exceeding its disk quota. Action may be required.",
		Severity:    "warning",
		DefaultOn:   true,
	},
	{
		Kind:        "docker_app.removed_from_package",
		Label:       "Docker app removed from package",
		Description: "A Docker app was removed because it is no longer part of the tenant's package.",
		Severity:    "warning",
		DefaultOn:   true,
	},
	{
		Kind:        "db.admin.config_apply_failed_unrecoverable",
		Label:       "Database config rejected",
		Description: "A database configuration change failed to apply and could not be recovered. Action required.",
		Severity:    "error",
		DefaultOn:   true,
	},
	{
		Kind:        "db.admin.config_applied",
		Label:       "Database settings updated",
		Description: "A database configuration change was applied successfully. Informational.",
		Severity:    "info",
		DefaultOn:   false,
	},
	{
		Kind:        "db.admin.maintenance_finished",
		Label:       "Database maintenance finished",
		Description: "A scheduled database maintenance run completed. Informational.",
		Severity:    "info",
		DefaultOn:   false,
	},
	{
		Kind:        "db.admin.root_password_rotated",
		Label:       "Database root password rotated",
		Description: "The managed database root/admin password was rotated. Informational confirmation.",
		Severity:    "info",
		DefaultOn:   false,
	},
	// GH #979: Automation API write events. Headless provisioning fires one of
	// these per user/domain mutation, so they get noisy at scale — expose them
	// as toggles. Confirmations default OFF; the destructive ones (delete,
	// suspend) default ON. Severities match what notifyWrite stamps.
	{
		Kind:        "automation.user.created",
		Label:       "Automation — user created",
		Description: "The Automation API created a user. Informational.",
		Severity:    "info",
		DefaultOn:   false,
	},
	{
		Kind:        "automation.user.deleted",
		Label:       "Automation — user deleted",
		Description: "The Automation API deleted a user. Significant — on by default.",
		Severity:    "warning",
		DefaultOn:   true,
	},
	{
		Kind:        "automation.user.disabled",
		Label:       "Automation — user disabled",
		Description: "The Automation API disabled (suspended) a user.",
		Severity:    "warning",
		DefaultOn:   true,
	},
	{
		Kind:        "automation.user.enabled",
		Label:       "Automation — user enabled",
		Description: "The Automation API re-enabled a user. Informational.",
		Severity:    "info",
		DefaultOn:   false,
	},
	{
		Kind:        "automation.domain.created",
		Label:       "Automation — domain created",
		Description: "The Automation API created a domain. Informational.",
		Severity:    "info",
		DefaultOn:   false,
	},
	{
		Kind:        "automation.domain.suspended",
		Label:       "Automation — domain suspended",
		Description: "The Automation API suspended a domain. Significant — on by default.",
		Severity:    "warning",
		DefaultOn:   true,
	},
	{
		Kind:        "automation.domain.unsuspended",
		Label:       "Automation — domain unsuspended",
		Description: "The Automation API unsuspended a domain. Informational.",
		Severity:    "info",
		DefaultOn:   false,
	},
}

// LookupNotificationEventKind returns the metadata for a known kind
// or nil if unknown. Used by handlers + dispatcher.
func LookupNotificationEventKind(kind string) *NotificationEventKindMeta {
	for i := range AllNotificationEventKinds {
		if AllNotificationEventKinds[i].Kind == kind {
			return &AllNotificationEventKinds[i]
		}
	}
	return nil
}
