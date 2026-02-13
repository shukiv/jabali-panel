# Documentation Summary (Jabali Panel)

Last updated: 2026-02-10

## Product Overview
Jabali Panel is a modern web hosting control panel for WordPress and general PHP hosting. It provides an admin panel for server-wide operations and a user panel for per-tenant management. The core goals are safe automation, clean multi-tenant isolation, and operational clarity.

## Architecture
- Control plane: Laravel 12 app using Filament v5 + Livewire v4.
- Data plane: Root-level agent (bin/jabali-agent) that performs privileged operations.
- Health monitor: bin/jabali-health-monitor restarts critical services and sends alerts.
- Job queue: async tasks and long-running operations.

## Panels and Routes
- Admin panel: /jabali-admin
- User panel: /jabali-panel

## Data and Storage
- Panel metadata: SQLite at database/database.sqlite.
- Hosting services: MariaDB, Postfix, Dovecot, BIND, Redis (as configured by the agent).
- Sysstat-based server status charts: app/Services/SysstatMetrics.php uses /var/log/sysstat.

## Services and Monitoring
Health monitor checks services every 30s and auto-restarts when needed. Notifications are sent via AdminNotificationService based on settings in dns_settings (notify_service_health, notify_high_load, etc.). Audit logs are stored in audit_logs and pruned daily based on retention settings.

## Backup System
- User and server backups with restore options for files, databases, mail, SSL, and DNS.
- Restore is guarded by strict security validations (prefix checks, SQL sanitization, path validation, DNS validation).
- Supports local and remote destinations (SFTP, NFS, S3). Retention is applied after each backup.

## DNSSEC
DNSSEC can be enabled per domain and generates KSK/ZSK keys, DS records, and signed zones. Managed through the admin panel and backed by agent actions.

## UI and Filament Rules (Critical)
- Use only Filament native components (no custom HTML, no custom CSS, no inline styles).
- Use tables for list data (EmbeddedTable or HasTable).
- Use icons and icon colors for status indication.
- Keep layouts responsive and consistent with existing patterns.

## Installation and Upgrade
- Target OS: Fresh Debian 12/13 install with no pre-existing web/mail stack.
- Installer: install.sh, builds assets as www-data and ensures permissions.
- Upgrade: php artisan jabali:upgrade manages dependencies, caches, and permissions for public/build and node_modules.
- Deploy helper: scripts/deploy.sh rsyncs to `root@192.168.100.50`, commits there, bumps VERSION, updates install.sh fallback, pushes to Git remotes from that server, then runs composer/npm, migrations, and caches.

## Packaging
Debian packaging is supported via scripts:
- scripts/build-jabali-deps-deb.sh
- scripts/build-jabali-panel-deb.sh

## Documentation MCP Server
mcp-docs-server exposes README, AGENT docs, and changelog through MCP tools for search and section retrieval.

## Miscellaneous Docs
- Screenshot regeneration script: tests/take-screenshots.cjs.
- DirectAdmin migration blueprint: docs/architecture/directadmin-migration-blueprint.md.
- Policies: resources/markdown/policy.md and resources/markdown/terms.md are placeholders.
- WordPress plugin: resources/wordpress/jabali-cache/readme.txt documents the Jabali Cache plugin.
