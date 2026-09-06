# Notifications — Events

`/jabali-admin/notifications/events`. The catalog of event sources the panel can emit. Configure routing on [Routing](./notifications-routing.md).

## Built-in event sources

| Event source | When it fires |
|---|---|
| `cert_renew` | Let's Encrypt issuance or renewal completes — success or failure. |
| `disk_full` | A user crosses the high-watermark threshold against their disk quota. |
| `service_down` | A watched systemd unit enters `failed`, restarts unexpectedly, or remains degraded for longer than its grace period. |
| `crowdsec_spike` | CrowdSec emits more decisions in a short window than the configured threshold (default: 50 decisions per 5 minutes). |
| `domain_expiry` | A managed TLS certificate enters the 14-day expiry window. (Note: the source name dates back to a planned WHOIS expiry detector that is not implemented; the current behavior is cert-only.) |
| `aide_diff` | The daily AIDE host-integrity scan reports a difference outside the panel's drop-in paths. |
| `cron_failed` | A systemd-user cron timer's service unit exits non-zero. |
| `backup_succeeded` | A scheduled backup completes successfully. |
| `backup_failed` | A scheduled backup fails. |
| `mail_quarantined` | Stalwart or the async post-delivery YARA scan moves a message to quarantine. |
| `malware_file_hit` | A M33 detector (ClamAV, LMD, YARA, Tetragon) hits on a file. |
| `db_root_rotated` | The administrator rotates the MariaDB root password. |
| `mail_throttle_suspended` | A sender is suspended after repeated throttle hits. |
| `ssh_login` | A shell / SFTP login to an account. Each account owner can keep a per-account **ignore list** so logins from known hosts / users don't notify (GH #1436). |

## Stub sources

Defined but not yet wired to producers:

- `domain_registrar_expiry` — would observe WHOIS expiry data; deferred pending an external WHOIS lookup pipeline.
- `backup_future_warnings` — predictive backup-failure detection.

## Severity

Each event has a default severity (`info`, `notice`, `warn`, `error`, `critical`). Routing rules may filter by severity threshold.

## Custom event sources

Adding a new source is one Go file under `panel-api/internal/notifications/sources/`. The dispatcher discovers sources by interface registration at boot; no schema migration required.

## Discovery in the UI

Each event row shows: description, default severity, producer module, default routing rule(s), and last 24 h emission count.
