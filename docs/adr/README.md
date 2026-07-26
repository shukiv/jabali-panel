# Architecture Decision Records

This directory contains Architecture Decision Records (ADRs) documenting significant architectural decisions in the Jabali Panel project. Decisions are written in MADR (Markdown Any Decision Records) 3.0 format.

## Status Key
- **Accepted** — Decision is locked in and enforced
- **Proposed** — Under consideration
- **Deprecated** — No longer in use
- **Superseded by** — Replaced by a newer ADR

## ADR Index

<!-- AUTO-GENERATED:adr-index — rows generated from docs/adr/NNNN-*.md (title + status). Regenerate via /update-docs. Curated titles are preserved on merge; do not hand-edit inside the markers. -->
| # | Title | Status |
|---|-------|--------|
| [0000](0000-control-plane-model.md) | Control plane model (overview) | Accepted |
| [0001](0001-go-agent-over-ndjson-unix-socket.md) | Go agent over NDJSON Unix socket | Accepted |
| [0002](0002-database-source-of-truth.md) | Database is the source of truth | Accepted |
| [0003](0003-one-write-path-the-api.md) | One write path: the API | Accepted |
| [0004](0004-reconciler-driven-convergence.md) | Reconciler-driven convergence | Accepted |
| [0005](0005-gorm-golang-migrate.md) | GORM for ORM, golang-migrate for schema | Accepted |
| [0006](0006-in-process-worker.md) | In-process worker, not separate daemon | Accepted |
| [0007](0007-english-only-no-i18n.md) | English-only UI, no i18n infrastructure | Accepted |
| [0008](0008-sibling-repos-out-of-scope.md) | Sibling repos are out-of-scope for panel | Accepted |
| [0009](0009-nginx-file-per-vhost.md) | Nginx file-per-vhost with force-regen path | Accepted |
| [0010](0010-install-via-curl-bash.md) | Install via `curl \| bash` only | Accepted |
| [0011](0011-powerdns-mysql-backend.md) | PowerDNS with MySQL backend | Accepted |
| [0012](0012-refine-antd-tanstack.md) | Refine + Ant Design + TanStack Query frontend | Accepted |
| [0013](0013-users-inline-best-effort.md) | Users inline best-effort (not reconciler-managed) | Accepted |
| [0014](0014-panel-port-8443-user-443.md) | PANEL_PORT 8443, user sites on 443 | Accepted |
| [0015](0015-admin-impersonation-jwt-claim.md) | Admin impersonation with `impersonated_by` JWT claim | Accepted |
| [0016](0016-break-glass-cli-admin-login.md) | Break-glass admin login via CLI with `purpose=cli_login` claim | Accepted |
| [0017](0017-ssl-try-acme-then-selfsigned-with-backoff.md) | SSL: try ACME first, fall back to self-signed, retry with backoff | Accepted |
| [0018](0018-m7-mariadb-first-postgres-deferred.md) | M7 Databases — MariaDB first, Postgres deferred | Accepted |
| [0019](0019-m7-per-database-grants-only.md) | M7 Databases — Per-database grants only (rw/ro), defer per-table | Accepted |
| [0020](0020-m7-phpmyadmin-sso-signon-proxy.md) | M7 phpMyAdmin SSO via server-side signon proxy + single-use token | Accepted (partially superseded by ADR-0022) |
| [0021](0021-m7-database-entity-lifecycle.md) | M7 Databases — Entity lifecycle (naming, quota, cascade, password) | Accepted |
| [0022](0022-m7-phpmyadmin-sso-shadow-account-and-uds.md) | M7 phpMyAdmin SSO — shadow admin account + UDS validate transport | Accepted — Parked pending M9 (2026-04-17) |
| [0023](0023-m9-php-fpm-pool-manager.md) | M9 PHP/FPM pool manager | Accepted |
| [0025](0025-per-user-systemd-slices.md) | Per-user systemd slices | Accepted |
| [0026](0026-m10-wordpress-installs.md) | M10 WordPress installs — schema + lifecycle | Accepted |
| [0027](0027-m11-filebrowser-integration.md) | M11 File manager via filebrowser + proxy auth | Accepted |
| [0028](0028-m12-sftp-integration.md) | M12 SFTP via openssh group-based Match (no chroot) | Accepted |
| [0029](0029-m8-cron-systemd-user-timers.md) | M8 Cron via systemd-user timers with closed-set allowlist | Accepted |
| [0030](0030-antd-file-manager-replaces-filebrowser.md) | AntD-native file manager replaces filebrowser | Accepted |
| [0031](0031-php-extensions-management.md) | PHP extensions management — server-wide, dpkg-live, fixed allowlist | Accepted |
| [0032](0032-m18-resource-limits.md) | M18 per-user resource limits — POSIX quota + cgroups v2 + nginx | Accepted |
| [0033](0033-m19-applications-framework.md) | M19 Applications Framework | Accepted |
| [0034](0034-m20-kratos-identity.md) | M20 Kratos identity — self-hosted Ory Kratos as IdP | Accepted |
| [0036](0036-m16-hydra-identity.md) | M16 Ory Hydra OAuth2/OIDC provider | Superseded by ADR-0038 |
| [0037](0037-drop-refine.md) | Drop Refine → TanStack Query + AntD + react-router | Accepted |
| [0038](0038-m16-rollback.md) | M16 rollback — OIDC+Hydra dropped, magic-link (M22) replaces | Accepted |
| [0039](0039-m22-magic-link.md) | Magic-link token for Panel→WP admin SSO | Superseded by ADR-0040 |
| [0040](0040-m22-sso-file.md) | Self-deleting SSO file for Panel→WP admin login (M22 rework) | Accepted |
| [0041](0041-m6-mail-storage-rocksdb.md) | M6 mail storage — RocksDB + Bulwark webmail | Accepted (amended by 0045) |
| [0042](0042-m6-sql-directory-mailboxes-table.md) | M6 SQL directory — Stalwart reads jabali_panel.mailboxes | Accepted (amended by 0045) |
| [0043](0043-m6-dkim-key-rotation-policy.md) | M6 DKIM — Ed25519 primary, RSA-2048 escape hatch | Accepted |
| [0044](0044-m6-imap-migrate-deferred-to-m15.md) | M6 IMAP migration — deferred to M15 | Accepted |
| [0045](0045-m6-stalwart-v016-pivot.md) | M6 pivot to Stalwart v0.16 management model | Accepted |
| [0046](0046-responsive-ui-strategy.md) | M23 responsive UI strategy | Accepted |
| [0047](0047-pdns-recursor-local-self-resolution.md) | M6.3 pdns-recursor for local self-resolution | Accepted |
| [0048](0048-panel-hostname-primary-mail-domain.md) | Panel hostname as primary mail domain | Accepted |
| [0049](0049-m24-ip-address-manager.md) | M24 IP address manager | Accepted |
| [0050](0050-m25-unix-sockets.md) | M25 localhost hardening via Unix sockets | Accepted |
| [0051](0051-m6.5-email-features-db-as-truth.md) | M6.5 email features — DB-as-truth reconciliation | Accepted |
| [0052](0052-disclaimer-sieve-vs-mtahook.md) | Disclaimer — Sieve system script (HTML covered) | Accepted |
| [0053](0053-crowdsec-over-fail2ban.md) | CrowdSec over fail2ban for behaviour-based IP blocking | Accepted |
| [0054](0054-ufw-over-iptables.md) | UFW over raw iptables/nftables for the host firewall | Accepted |
| [0055](0055-modsecurity-per-domain.md) | ModSecurity-nginx + OWASP CRS, per-domain toggle | SUPERSEDED (2026-04-26) by ADR-0060 + M27 AppSec |
| [0056](0056-notification-dispatcher-redis-streams.md) | M14 Notification dispatcher via Redis Streams + consumer group | Accepted |
| [0057](0057-webpush-vapid.md) | M14 Web Push via VAPID, keypair in server_settings | Accepted |
| [0058](0058-ntfy-channel.md) | M14 ntfy.sh channel: plain HTTP POST + optional bearer + priority + tags | Accepted |
| [0059](0059-redis-shared-cache.md) | Redis as shared local cache/queue (unix socket, jabali-sockets group) | Accepted |
| [0060](0060-appsec-geoblock.md) | AppSec geoblock (server-wide country filter) — opt-in | Accepted |
| [0061](0061-allowlists-lapi-truth.md) | CrowdSec allowlists — LAPI is truth, no DB mirror | Accepted |
| [0062](0062-console-enrollment-machine-scope.md) | CrowdSec Console enrollment — operator-driven, disenroll wipes online_api_credentials.yaml | Accepted (amended 2026-04-26) |
| [0063](0063-profiles-yaml-for-remediation-override.md) | Per-scenario remediation override via `/etc/crowdsec/profiles.yaml` | Accepted |
| [0064](0064-diagnostic-report-enclosed-mail.md) | Diagnostic report — enclosed.cc upload + email delivery | Accepted |
| [0065](0065-server-status.md) | Server Status aggregator | Accepted |
| [0066](0066-le-cert-panel-hostname.md) | Let's Encrypt cert for the panel hostname | Accepted |
| [0067](0067-ssh-shell-sandbox.md) | M13 SSH shell sandbox (bubblewrap / nspawn) | Accepted |
| [0068](0068-per-user-cgroup-slice-metrics.md) | Per-user slice metrics via direct cgroup v2 read | Accepted |
| [0069](0069-server-status-masonry-layout.md) | AntD `<Masonry>` for Server Status page layout | Accepted |
| [0070](0070-le-cert-san-scope.md) | LE cert SAN list scoped to auto-provisioned DNS only (amends 0066) | Accepted |
| [0071](0071-mariadb-loopback-only-not-skip-networking.md) | MariaDB loopback-only bind (amends 0050 M25.1) | Accepted |
| [0072](0072-malware-detection-stack.md) | M33 Malware stack: maldet + YARA (Tetragon removed 2026-04-30 per ADR-0085) | Accepted (amended) |
| [0073](0073-stalwart-email-aliases-query.md) | Stalwart `queryEmailAliases` + apply-plan schema evolution | Accepted |
| [0074](0074-lazy-service-alert-suppression.md) | Suppress critical alert on inactive + disabled services (lazy-started units; renumbered 2026-04-28 from 0067) | Accepted |
| [0075](0075-backup-restore-restic.md) | M30 Backup & Restore: restic-backed, single shared repo | Accepted |
| [0076](0076-dnssec-per-domain-pdnsutil.md) | M15 Per-domain DNSSEC via pdnsutil shell-out (renumbered 2026-04-28 from 0057) | Accepted |
| [0077](0077-jabali-repair-self-heal.md) | `jabali repair` — host-state self-heal subcommand | Accepted |
| [0078](0078-backup-destinations-and-schedules.md) | M30.1 Backup destinations + scheduled backups (renumbered 2026-04-28 from 0077) | Accepted |
| [0079](0079-mail-yara-async-scanner.md) | M33.2 Mail YARA async scanner (JMAP-driven, in-process tick) | Accepted |
| [0080](0080-per-destination-backup.md) | M30.2 Per-destination backup model | Accepted |
| [0081](0081-domain-email-enabled-default-true.md) | Email enabled by default for new domains (migration 000104) | Accepted |
| [0082](0082-ghost-domain-detector.md) | M38 Ghost Domain Detector — periodic DNS-alignment check | Accepted |
| [0083](0083-shared-ops-packages.md) | M41 Shared ops packages for REST + CLI code reuse (dbops/cronops/sshkeyops) | Accepted |
| [0084](0084-per-user-egress-firewall-cgroupv2.md) | M34 Per-user PHP-FPM egress firewall via nftables + cgroupv2 socket match | Proposed |
| [0085](0085-narrow-scoped-auditd-exec-audit.md) | M39 Narrow-scoped auditd as L3 forensic exec audit (replaces Tetragon) | Accepted |
| [0086](0086-apparmor-jabali-daemons.md) | M40 AppArmor profiles for jabali daemons + critical system services | Accepted |
| [0087](0087-aide-system-fim.md) | M42 AIDE file integrity monitor for system binaries + configs | Accepted |
| [0088](0088-snuffleupagus-php-hardening.md) | M41 Snuffleupagus PHP RCE hardening | Proposed |
| [0089](0089-crowdsec-single-ip-trust-source.md) | M43 CrowdSec as single IP-trust source of truth | Accepted |
| [0090](0090-2fa-totp-via-kratos-builtins.md) | 2FA TOTP + recovery codes via Kratos built-ins | Accepted |
| [0091](0091-postgresql-parity-phase-1.md) | M37 PostgreSQL feature parity Phase 1 foundation | Accepted |
| [0092](0092-apparmor-aa4-rules.md) | M40.1 AppArmor 4.x profile authoring patterns — empirical unix-socket rules | Accepted |
| [0093](0093-automation-api-tokens.md) | M44 Automation API scoped tokens | Accepted |
| [0094](0094-migration-importers.md) | M35 Migration importers (cPanel / DA / Hestia / WHM / IMAP) | Proposed |
| [0095](0095-m35-migration-gui-design-decisions.md) | M35.1 Migration GUI design decisions (wizard, SSE, SSRF, retry, batch) | Proposed |
| [0096](0096-root-web-terminal.md) | Root web terminal in admin panel | Accepted |
| [0097](0097-db-root-password-alongside-socket-peer-auth.md) | M46 DB root/superuser password alongside socket/peer auth | Accepted |
| [0098](0098-curated-reconciler-converged-db-config-tuner.md) | M46 Curated reconciler-converged DB config tuner | Accepted |
| [0099](0099-admin-scoped-privileged-db-web-access.md) | M46 Admin-scoped privileged DB web access (phpMyAdmin/Adminer) | Accepted |
| [0100](0100-db-maintenance-processlist-privilege-model.md) | M46 DB maintenance + processlist privilege model | Accepted |
| [0101](0101-cron-intake-synchronous-apply.md) | Cron Job Intake owns synchronous agent-apply (amends 0083) | Accepted |
| [0102](0102-appsec-exempt-authenticated-admin-api.md) | Authenticated admin API exempt from AppSec WAF (ADR-0060/0063 related) | Accepted |
| [0103](0103-email-deliverability-suite.md) | Email deliverability suite (queue/throttle/RBL/DMARC + MTA-STS/TLS-RPT) | Proposed |
| [0105](0105-panel-cert-split-hostname-mail.md) | Panel cert split — independent hostname + mail certs | Accepted (amends 0066) |
| [0106](0106-unified-audit-log.md) | M49 Unified audit log (append-only hash-chained, dedicated audit stream, admin + per-user scope) | Accepted |
| [0107](0107-operator-dns-edits-authoritative.md) | Operator/admin DNS record edits are authoritative | Accepted |
| [0108](0108-per-domain-fastcgi-microcache.md) | Per-domain opt-in nginx FastCGI micro-cache | Accepted |
| [0109](0109-per-domain-mta-sts.md) | Per-domain MTA-STS (M47 Wave 7) | Accepted |
| [0110](0110-m47-stalwart-report-ingest.md) | M47 Stalwart-report ingest + deliverability score | Accepted |
| [0111](0111-m47-wave3-outbound-throttle.md) | M47 outbound throttle reconciler | Accepted |
| [0112](0112-m47-wave3v2-expression-filters.md) | M47 Stalwart Expression filters + per-domain widget | Accepted |
| [0113](0113-drop-crowdsec-console.md) | Drop CrowdSec Console integration | Accepted |
| [0114](0114-stalwart-auth-fail-bruteforce.md) | Mail-bf via Stalwart webhook | Superseded by ADR-0115 |
| [0115](0115-stalwart-bruteforce-crowdsec-parser.md) | Mail bruteforce via CrowdSec parser + Stalwart tracer | Proposed |
| [0116](0116-m48-docker-app-marketplace.md) | M48 Docker App Marketplace | Proposed |
| [0117](0117-m6.6-per-domain-mail-tls.md) | M6.6 per-domain mail TLS | Accepted |
| [0118](0118-m53-updates-center.md) | M53 Updates Center | Accepted |
| [0119](0119-m54-username-login.md) | M54 username-only login (hard cutover) | Accepted |
| [0120](0120-mail-provider-dns-templates.md) | Mail-provider DNS templates | Proposed |
| [0121](0121-safe-restore-mail-apply.md) | Safe per-user restore-side mail apply (scoped, additive, dry-run-gated) | Superseded by ADR-0122 |
| [0122](0122-restore-mail-no-stalwart-apply.md) | Account-restore does NOT run stalwart-cli apply — DB rows authoritative | Accepted |
| [0123](0123-per-user-mail-message-backup-restore.md) | Per-user mail message backup + restore via JMAP Maildir export/import | Accepted |
| [0124](0124-crs-false-positive-exclusion-plugin.md) | jabali CRS "before" plugin for targeted AppSec false-positive exclusions | Accepted |
| [0125](0125-m50-directory-privacy.md) | M50 Directory Privacy — per-subdirectory HTTP Basic Auth | Accepted |
| [0126](0126-per-user-cli-php-version.md) | Per-user CLI PHP version follows the FPM version pin | Accepted |
| [0127](0127-ssh-sandbox-userns-apparmor-profile.md) | AppArmor userns profile for the M13 SSH sandbox (Ubuntu 24.04+) | Accepted |
| [0128](0128-admin-impersonation-act-as.md) | Admin act-as impersonation via effective-user override | Accepted |
| [0129](0129-ssh-classic-listener-mask-socket.md) | Normalize SSH to a single classic ssh.service listener (mask ssh.socket) | Accepted |
| [0130](0130-htaccess-to-rule-builder-converter.md) | `.htaccess` → Rule Builder converter (typed rules, not raw nginx) | Accepted |
| [0131](0131-python-app-manager.md) | Python Application Manager via native per-user systemd + nginx proxy (not Passenger) | Proposed |
| [0132](0132-m51-mailbox-groups.md) | M51 Mailbox User Groups — DB-as-truth groups, Stalwart registry projection, native resource sharing | Accepted |
| [0141](0141-ssl-cert-modes.md) | Per-domain SSL certificate modes | Accepted |
| [0142](0142-stalwart-webadmin-reverse-proxy.md) | Stalwart WebAdmin reverse-proxy (opt-in, default-off) | Accepted |
| [0143](0143-disclaimer-mtahook.md) | Disclaimer via MTA Hook (lossless HTML) | Proposed |
| [0144](0144-api-token-scopes.md) | Scope-restricted user API tokens (RBAC) | Accepted |
| [0145](0145-cron-http-trigger-allowlist.md) | Constrained curl/wget self-domain cron http-triggers | Accepted |
| [0146](0146-tenant-php-exec-and-metadata-hardening.md) | Tenant PHP command-exec lockdown + always-on cloud-metadata egress floor | Accepted |
| [0147](0147-appsec-wordpress-builder-exemption.md) | AppSec exemption for WordPress page-builder endpoints (scoped CRS rule drop) | Accepted |
| [0148](0148-wp-cache-redis-multitenant-acl.md) | Multi-tenant Redis ACL model for the WordPress cache | Proposed |
| [0149](0149-pty-broker-peercred-authz.md) | Agent-side SO_PEERCRED authorization on the root PTY broker | Accepted |
| [0150](0150-dns-record-type-permissions.md) | Admin-controlled DNS record-type permissions for tenants | Accepted |
| [0151](0151-migration-ssh-host-key-pinning.md) | SSH host-key pinning for migration connectors and rsync restore | Accepted |
| [0152](0152-admin-breadcrumbs-cross-entity-links.md) | Admin breadcrumbs + cross-entity navigation | Accepted |
| [0153](0153-login-ip-crowdsec-allowlist.md) | Auto-allowlist successful panel + SSH login IPs in CrowdSec | Accepted |
| [0154](0154-panel-daemon-apparmor-confinement.md) | Panel daemon AppArmor confinement via aa-exec | Accepted |
| [0155](0155-php-fpm-performance-tiers.md) | PHP-FPM performance tiers (package-gated) | Accepted |
| [0156](0156-send-as-delegation.md) | Send-as delegation (mustMatchSender expression) | Accepted |
| [0157](0157-automation-write-endpoints.md) | Write-automation endpoints + write scopes (JAB-140) | Accepted |
| [0158](0158-cacheable-query-param-allowlist.md) | Per-domain cacheable query-param allowlist | Accepted |
| [0159](0159-per-user-notification-channels.md) | Per-user notification channels + routing (JAB-171) | Accepted |
| [0160](0160-build-tag-demo-mode.md) | Build-tag-gated demo mode on main (JAB-159) | Accepted |
<!-- 0133-0140: numbers reserved during planning, no ADR file was ever written (JAB-161). -->
<!-- /AUTO-GENERATED -->

## Decision Categories

### Architecture & Data
- [0000](0000-control-plane-model.md) — Control plane model (overview; superseded in scope by 0002/0003/0004)
- [0002](0002-database-source-of-truth.md) — Database is the source of truth
- [0004](0004-reconciler-driven-convergence.md) — Reconciler-driven convergence
- [0005](0005-gorm-golang-migrate.md) — GORM for ORM, golang-migrate for schema

### API & Communication
- [0001](0001-go-agent-over-ndjson-unix-socket.md) — Go agent over NDJSON Unix socket
- [0003](0003-one-write-path-the-api.md) — One write path: the API

### Deployment & Operations
- [0006](0006-in-process-worker.md) — In-process worker, not separate daemon
- [0010](0010-install-via-curl-bash.md) — Install via `curl | bash` only
- [0014](0014-panel-port-8443-user-443.md) — PANEL_PORT 8443, user sites on 443
- [0015](0015-admin-impersonation-jwt-claim.md) — Admin impersonation with impersonated_by JWT claim
- [0016](0016-break-glass-cli-admin-login.md) — Break-glass admin login via CLI with purpose=cli_login claim
- [0017](0017-ssl-try-acme-then-selfsigned-with-backoff.md) — SSL: try ACME first, fall back to self-signed, retry with backoff

### Infrastructure & Services
- [0009](0009-nginx-file-per-vhost.md) — Nginx file-per-vhost with force-regen path
- [0011](0011-powerdns-mysql-backend.md) — PowerDNS with MySQL backend

### Frontend & UX
- [0007](0007-english-only-no-i18n.md) — English-only UI, no i18n infrastructure
- [0012](0012-refine-antd-tanstack.md) — Refine + Ant Design + TanStack Query frontend

### Scope & Integration
- [0008](0008-sibling-repos-out-of-scope.md) — Sibling repos are out-of-scope for panel
- [0013](0013-users-inline-best-effort.md) — Users inline best-effort (not reconciler-managed)

## How to Use This Document

### When Making Changes
- Before implementing a feature, check which ADRs apply
- If your change violates an accepted ADR, raise it for discussion first
- Reference the relevant ADRs in PR descriptions and commit messages

### When Adding a New ADR
1. Assign the next number (starting from 0001)
2. Use kebab-case for the filename: `NNNN-kebab-case-title.md`
3. Include these sections: Status, Context, Decision, Consequences (positive/negative), Alternatives considered
4. Update this README with a link to the new ADR

### Related Documents
- `docs/runbooks/dns-secondary-nameserver.md` — Secondary nameserver setup (references ADR-0011)
- `BLUEPRINT.md` — Feature roadmap and milestones
