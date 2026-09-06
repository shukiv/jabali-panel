# Admin Shell

Lives at `/jabali-admin`. Top-level pages:

| Page | Path | Purpose |
|---|---|---|
| Dashboard | `/jabali-admin/dashboard` | Counts (users, domains, mailboxes), recent activity, health summary, version. |
| Users | `/jabali-admin/users` | Create / edit / delete panel users; reset password; reset 2FA; suspend; assign package; rename username in place; impersonate (scoped, time-bounded, audited); at-a-glance resource counts + monthly bandwidth. |
| Packages | `/jabali-admin/packages` | Bundles of quotas (disk, bandwidth, mailboxes, domains, DBs, PHP-INI overrides). |
| Domains | `/jabali-admin/domains` | All hosted domains across all users; per-domain settings (PHP, SSL, DNSSEC, listen IP, redirects, aliases, cache). |
| DNS | `/jabali-admin/dns` | All DNS zones; per-domain DNSSEC enable + DS record display. |
| SSL | `/jabali-admin/ssl` | Cross-user SSL state; LE issuance retry buttons; panel-hostname cert. |
| Mail | `/jabali-admin/mail/deliverability`, `/throttles` | Deliverability dashboard (DKIM / SPF / DMARC / MTA-STS state per domain), outbound throttle policy. |
| Databases (Admin Ops) | Server Settings → Database | Root password rotation, curated config tune (`innodb_buffer_pool_size`, etc.), maintenance, processlist + kill, admin phpMyAdmin SSO. |
| IPs | `/jabali-admin/ips` | Manage the IPv4 pool exposed for per-domain listen-IP selection. |
| Security | `/jabali-admin/security` | CrowdSec console + decisions + allowlists + alerts; AppSec WAF; AppArmor profile status; Snuffleupagus PHP hardening; AIDE host-integrity; per-user egress firewall; malware scans; UFW port baseline; CrowdSec test-IP card. |
| Server Status | `/jabali-admin/server-status` | Errgroup-aggregated live status of all services with 5 s polling; per-service start/stop/restart controls (behind an off-toggle). |
| Resource Limits | (per-user under Users → Edit) | POSIX quota + cgroup v2 slice + nginx `limit_req` per user. |
| PHP | `/jabali-admin/php-pools` | Installed PHP versions, per-version FPM tuning, server-wide extension manager (M9.6). |
| Applications | `/jabali-admin/applications` | App registry (WP + 14 others); per-app version pin and global enable. |
| Logs | `/jabali-admin/logs` | Tail panel / agent / nginx / Stalwart / Kratos / Bulwark / PowerDNS / CrowdSec. |
| Audit | `/jabali-admin/audit` | Append-only audit log (every privileged mutation; ADR-0106). |
| Server Settings | `/jabali-admin/settings` | Panel hostname; primary mail domain; default IP pool; DB admin sections; notifications channels; AppSec policy; CrowdSec console enrol; updates source. |
| Notifications | `/jabali-admin/notifications/{channels,events,routing,test}` | Configure channels (in-app, email, Slack, Telegram, Discord, ntfy, Web Push, SMS, webhook); per-event routing (category rail); test send; owns an **Owner** column for tenant-owned channels. |
| Updates | `/jabali-admin/updates` | Run `jabali update` from the UI as a transient systemd unit (survives panel restart mid-update); Update Center reports OS security-update state. |
| Support | `/jabali-admin/support` | Generate an encrypted diag bundle (enclosed.cc), `mailto:` ticket. |
| Migrations | `/jabali-admin/migrations` | Ingest cPanel / DirectAdmin / Hestia / WHM archives, track restore progress. |
| Backups | `/jabali-admin/backups` | Destinations (local / sftp / s3 / b2 / azure / gcs / rest), schedules, retention, restore; Full Server container backup + restore-from-upload. |
| Automation | `/jabali-admin/automation` | Scoped API tokens for the opt-in Automation API (read-only endpoints on :443 for firewalled billing hosts, GH #1161). |
| Terminal | `/jabali-admin/terminal` | Web terminal (root). |

## Sidebar groups

The sidebar groups pages by activity:

- **Overview** — Dashboard, Server Status, Audit.
- **Hosting** — Users, Packages, Domains, Applications, IPs.
- **Mail & DNS** — Mail (deliverability + throttles), DNS, SSL.
- **System** — Server Settings, PHP, Security, Resource Limits, Logs, Updates, Backups, Migrations.
- **Other** — Notifications, Support, Automation, Terminal, Profile.

## What is intentionally *not* here

- **OIDC / Hydra** — rolled back in M16. The panel does not act as an OIDC provider.
- **ModSecurity** — replaced by CrowdSec AppSec (M27).
- **filebrowser** — replaced by the in-panel AntD File Manager (M11).
