# Notifications — Events

`/jabali-admin/notifications/events`. Every event the panel can send, and whether it is on. [Routing](./notifications-routing.md) explains where an event goes.

## Turning an event on or off

Each row shows the event's label, its name, its description and its severity. The switch turns the event on or off for the whole server. A row whose state differs from its default shows **Overridden**. A change takes effect within 30 seconds.

An event that is off is dropped: it reaches no channel and no bell. The `ssh.login` row also has an ignore list for logins from known hosts and users (GH #1436).

From a shell:

```bash
jabali notification events list
jabali notification events set <event-kind> --enabled <true|false>
```

## Severity

Each event has one of four severities: `info`, `warning`, `error` or `critical`.

## Event catalog

<!-- BEGIN generated event catalog: go test ./panel-api/internal/models -run TestNotificationEventCatalogDoc -update -->

| Event | Severity | On by default | What it means |
|---|---|---|---|
| `cert.renew.fail` | error | yes | SSL renewal failed. ACME renewal failed — issued via panel certbot. Action usually required. |
| `cert.renew.ok` | info | no | SSL renewal succeeded. ACME renewal completed. Informational confirmation. |
| `domain.expiry.7d` | warning | yes | SSL cert expiring in 7 days. Cert crosses the 7-day pre-expiry window without a fresh renewal. |
| `domain.expiry.1d` | error | yes | SSL cert expiring in 1 day. Cert crosses the 1-day pre-expiry window — imminent expiry. |
| `disk.full.warn` | warning | no | Disk usage 80%. System disk passed the 80% threshold. Cleanup recommended. |
| `disk.full.crit` | critical | yes | Disk usage 95%. System disk passed the 95% threshold — services will fail soon. |
| `dr.sync.stalled` | error | yes | DR standby sync stalled. This DR standby has not applied a fresh snapshot within several sync cycles — the replica is going stale. Fix before you need to promote. |
| `disk.quota.warn` | warning | yes | User reached 90% quota. A hosting user crossed 90% of their disk quota. |
| `load.high` | warning | no | High server load. 1-minute load average exceeded the configured threshold. |
| `system.update.available` | info | no | System updates available. apt has new package upgrades waiting (security or otherwise). |
| `docker_app.update_available` | info | yes | Docker app update available. Marketplace poller detected a newer upstream image digest for an installed Docker app. |
| `service.down` | error | yes | Service down or restarted. A managed service unit (nginx, php-fpm, mariadb, …) failed or was restarted. |
| `crowdsec.ban.spike` | warning | no | CrowdSec ban spike. Unusually large burst of new bans within a short window. |
| `backup.fail` | error | yes | Backup failed. A scheduled backup job exited non-zero. |
| `backup.success` | info | no | Backup completed. A backup job finished. Fires once per fully-succeeded job (info); a job that finished with some items skipped/failed (partial) fires as a warning. Opt-in — off by default to avoid a message per backup. |
| `backup.retention.fail` | error | yes | Backup retention failed. The nightly restic forget/prune retention sweep failed for one or more backup destinations (a stale repository lock is the usual cause). Snapshots accumulate unpruned until fixed, growing the offsite repo toward disk-full (JAB-392). |
| `backup.limit.reached` | warning | yes | Tenant backup limit reached. A tenant hit their package's max_backups cap. Depending on the package retention policy the backup was blocked (reject) or the oldest was auto-pruned (prune). Notifies the tenant and admins (GH #454). |
| `admin.login` | info | yes | Admin signed in. First request of a new Kratos admin session. |
| `ssh.login` | info | yes | SSH login. Successful SSH authentication for a panel-managed user. |
| `notifications.channel.auto_disabled` | warning | yes | Channel auto-disabled. A notification channel was disabled after repeated send failures. |
| `panel.welcome` | info | yes | Welcome. Fired once at install time for the bootstrap admin to point them at notification setup. |
| `security.root_terminal.opened` | critical | yes | Root web terminal opened. An admin opened the in-panel root shell (M45). Highest-trust action; every byte is recorded to /var/log/jabali/terminal/<id>.cast. |
| `security.decision.fired` | info | no | Security decision fired. Aggregated drop/throttle from any decision brain (UFW, nginx limit_req, CrowdSec). One envelope per polling window when activity exceeds 0. |
| `snuffleupagus.incident.detected` | warning | yes | PHP Defense incident. PHP Defense rule fired on a tenant request (block / simulated block / log). |
| `postgres.service_down` | error | yes | PostgreSQL service down. postgresql.service is enabled but inactive — running connections fail. |
| `postgres.disk_high` | warning | yes | PostgreSQL data dir disk high. /var/lib/postgresql usage above 85%. |
| `postgres.connections_exhausted` | error | yes | PostgreSQL connections exhausted. Active connection count above 90% of max_connections — new clients will be refused. |
| `agent.dispatch.failure` | warning | yes | Agent dispatch failure. panel-api → agent RPC returned an internal error. One envelope per error bucket per 30-min window. Common causes: systemctl/DBus permission issues, missing helper binaries, exit-status leaks from wrapped commands. |
| `reconciler.error` | warning | yes | Reconciler error. A ReconcileAll pass logged a level=ERROR. Could be a domain rebuild that crashed, an SSL cert provisioning rollback, a managed-IP rebind that the kernel rejected, etc. |
| `agent.unreachable` | error | yes | Agent unreachable. panel-api couldn't reach /run/jabali/agent.sock. Usually means jabali-agent.service crashed or is restart-looping; check `systemctl status jabali-agent`. |
| `notifications.dlq.nonzero` | warning | yes | Notifications DLQ non-empty. The M14 dead-letter queue has unprocessed envelopes. Means at least one channel send (Slack/email/web push) failed after the dispatcher retry budget. DLQ is a marker of channel rot — operator should inspect and either reset or disable the offending channel. |
| `panel.api.error` | warning | no | panel-api 5xx. An HTTP request to /api/v1/* returned 5xx. Bucketed per endpoint+status pair; one envelope per bucket per 30-min window. |
| `bandwidth.quota.warn` | warning | yes | Bandwidth quota — 80%. User crossed 80% of their package's monthly BandwidthQuotaMB. Per-user dedupe via 6h cooldown. |
| `bandwidth.quota.crit` | critical | yes | Bandwidth quota — 100%. User crossed their package's monthly BandwidthQuotaMB. v1 does not auto-suspend; admin decides. |
| `digest.daily` | info | no | Daily digest. One summary email a day: alerts by severity, backup outcomes, certificates expiring soon, and fleet counts for the last 24 hours. |
| `update.completed` | info | no | Panel update applied. The panel finished updating to a new release. Informational; noisy on the development channel. |
| `aide.tamper.detected` | error | yes | File integrity tamper. AIDE detected an unexpected change to a monitored system file. Security-relevant — investigate. |
| `nginx.config.invalid` | error | yes | nginx config invalid. A rendered nginx configuration failed validation and was not applied. Action required. |
| `malware.realtime.critical` | critical | yes | Malware — critical detection. The real-time scanner flagged a high-confidence malicious file. Security-relevant — investigate. |
| `malware.quarantine.added` | warning | yes | Malware quarantined. A file was quarantined by the malware scanner. Security-relevant. |
| `egress.drop.burst` | warning | yes | Outbound traffic blocked (burst). A burst of outbound connections was dropped by the egress firewall. Possible compromise or misconfiguration. |
| `exec.audit.burst` | warning | yes | Suspicious process burst. The exec-audit source saw a burst of flagged process executions. Security-relevant. |
| `domain.ghost_detected.mismatch` | warning | yes | Ghosted domain — IP mismatch. The domain resolves to a different IP than this server. Expected behind a proxy/CDN such as Cloudflare — disable if you front domains that way. |
| `domain.ghost_detected.nxdomain` | warning | yes | Ghosted domain — does not resolve. The domain does not resolve in public DNS. It may be newly added or its DNS is not yet live. |
| `domain.ghost_detected.partial` | warning | yes | Ghosted domain — partial DNS. Only some of the domain's expected records point here. DNS may be mid-propagation or misconfigured. |
| `domain.ownership.verified` | info | yes | Domain ownership verified. A domain (or its administrator) proved ownership, so its DNS zone, certificate and mail are being set up. |
| `domain.ownership.revoked` | warning | yes | Domain ownership withdrawn. An administrator withdrew a domain's verification. The domain is offline until its owner proves it again. |
| `domain.ownership.expiring` | warning | yes | Unverified domain about to be removed. A domain or alias is still not verified and will be removed in a few days unless its owner proves it. |
| `domain.ownership.expired` | warning | yes | Unverified domain removed. A domain or alias was never verified and was removed. Its site files were kept. |
| `domain.ownership.needs_admin` | warning | yes | Domain needs administrator approval. A pending domain cannot be proven by DNS (its nameservers already point here, or its DNS does not answer). An administrator must approve it. |
| `mail.rbl.listed` | error | yes | Mail IP blocklisted (RBL). The server's sending IP was found on a DNS blocklist. Outbound mail may be rejected — action required. |
| `mail.rbl.cleared` | info | no | Mail IP delisted (RBL). The server's sending IP is no longer on a previously-seen blocklist. Informational. |
| `mail.dmarc.report_received` | info | no | DMARC aggregate report received. An aggregate DMARC (RUA) report arrived for one of your domains. Informational. |
| `mail.tls.report_received` | info | no | SMTP TLS report received. An SMTP TLS-RPT report arrived for one of your domains. Informational. |
| `mail.feedback.received` | info | no | Mail feedback-loop report. A feedback-loop / abuse report was received about outbound mail. Informational. |
| `docker_app.entitlement_stopped` | warning | yes | Docker app stopped — entitlement. A tenant Docker app was stopped because its plan no longer entitles it. Action may be required. |
| `docker_app.disk_quota_stopped` | warning | yes | Docker app stopped — disk quota. A tenant Docker app was stopped after exceeding its disk quota. Action may be required. |
| `docker_app.removed_from_package` | warning | yes | Docker app removed from package. A Docker app was removed because it is no longer part of the tenant's package. |
| `db.admin.config_apply_failed_unrecoverable` | error | yes | Database config rejected. A database configuration change failed to apply and could not be recovered. Action required. |
| `db.admin.config_applied` | info | no | Database settings updated. A database configuration change was applied successfully. Informational. |
| `db.admin.maintenance_finished` | info | no | Database maintenance finished. A scheduled database maintenance run completed. Informational. |
| `db.admin.root_password_rotated` | info | no | Database root password rotated. The managed database root/admin password was rotated. Informational confirmation. |
| `automation.user.created` | info | no | Automation — user created. The Automation API created a user. Informational. |
| `automation.user.deleted` | warning | yes | Automation — user deleted. The Automation API deleted a user. Significant — on by default. |
| `automation.user.disabled` | warning | yes | Automation — user disabled. The Automation API disabled (suspended) a user. |
| `automation.user.enabled` | info | no | Automation — user enabled. The Automation API re-enabled a user. Informational. |
| `automation.domain.created` | info | no | Automation — domain created. The Automation API created a domain. Informational. |
| `automation.domain.suspended` | warning | yes | Automation — domain suspended. The Automation API suspended a domain. Significant — on by default. |
| `automation.domain.unsuspended` | info | no | Automation — domain unsuspended. The Automation API unsuspended a domain. Informational. |

<!-- END generated event catalog -->

## Adding an event

Add a row to `AllNotificationEventKinds` in `panel-api/internal/models/notification_event_setting.go`, and fire the event from a producer; most producers live in `panel-api/internal/eventsources/`. The panel adds the new row, with its default state, on its next start. Then regenerate the table above:

```bash
go test ./panel-api/internal/models -run TestNotificationEventCatalogDoc -update
```
