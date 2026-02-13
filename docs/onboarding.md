# Jabali Panel Developer Onboarding

Last updated: 2026-02-06

## Start Here
- Read AGENT.md first. It is the authoritative guide.
- Work from /var/www/jabali.
- Use ASCII in edits unless the file already uses Unicode.

## What This Project Is
Jabali Panel is a modern web hosting control panel. It has two Filament panels:
- Admin: /jabali-admin (server-wide management)
- User: /jabali-panel (per-tenant management)

## Key Components
- Laravel 12 + Filament v5 + Livewire v4
- Root agent: bin/jabali-agent (privileged ops over Unix socket /var/run/jabali/agent.sock)
- Health monitor: bin/jabali-health-monitor (service restarts and alerts)
- Panel DB: SQLite at database/database.sqlite
- Server metrics: app/Services/SysstatMetrics.php uses /var/log/sysstat

## Local Development (Typical)
From /var/www/jabali:
- composer dev
- php artisan test --compact
- ./vendor/bin/pint

## Production Operations (Typical)
- php artisan migrate
- php artisan config:cache
- php artisan route:cache

## UI Rules (Non-Negotiable)
- Use Filament native components only.
- No custom HTML, no custom CSS, no inline styles.
- Use tables for list data (EmbeddedTable or HasTable).
- Use icons and icon colors for status.
- Keep pages responsive and match existing patterns.

## Backups and Security
- Backup/restore goes through the agent and includes strict validations.
- Audit logs stored in audit_logs with retention.
- DNSSEC is supported and managed via the agent.

## Installer and Upgrade Notes
- Target OS: Fresh Debian 12/13 install with no pre-existing web/mail stack.
- install.sh builds assets as www-data and manages permissions.
- php artisan jabali:upgrade ensures caches and permissions (public/build, node_modules).

## Git Rules
- Do not push unless explicitly asked.
- Bump VERSION before every push.
- Keep install.sh version fallback in sync with VERSION.
- Push to GitHub from `root@192.168.100.50`.

## Where to Look for Examples
- app/Filament/Admin/Pages and app/Filament/Jabali/Pages
- app/Filament/Admin/Widgets and app/Filament/Jabali/Widgets
- app/Services for business logic
- bin/jabali-agent for privileged operations

## Common Tasks
- Screenshots: node tests/take-screenshots.cjs --output-dir=docs/screenshots
- Package build: scripts/build-jabali-deps-deb.sh, scripts/build-jabali-panel-deb.sh

## Quick Checklist Before You Commit
- Ran at least one test command.
- UI changes follow Filament-only rules.
- No permission regressions for public/build or node_modules.
- No changes to vendor or node_modules.
