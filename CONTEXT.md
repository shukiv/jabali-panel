# CONTEXT.md

Last updated: 2026-02-03

## Stack
- Laravel 12, Filament v5, Livewire v4
- PHP 8.4, Node 20, Vite
- Debian 12/13 target

## Panels
- Admin panel: `/jabali-admin`
- User panel: `/jabali-panel`

## Demo
- Public demo: `https://demo.jabali-panel.com`
- Demo container port: `5555` behind Nginx TLS proxy
- Demo DB: `database/database-demo.sqlite`
- Demo mode: `JABALI_DEMO=1` (read-only)
- Demo credentials:
  - Admin: `admin@jabali-panel.com` / `demo1234`
  - User: `demo@jabali-panel.com` / `demo1234`
- Livewire HTTPS requires proxy trust (`trustProxies(at: '*')`) and `X-Forwarded-Proto`.

## Data
- Panel config DB: SQLite at `database/database.sqlite`
- Hosting services use MariaDB/Postfix/Dovecot/etc. as configured by the agent

## Services & Agents
- Privileged agent: `bin/jabali-agent` (root)
- Health monitor: `bin/jabali-health-monitor`

## Server Status Charts
- Data source: `app/Services/SysstatMetrics.php`
- Uses sysstat logs under `/var/log/sysstat`

## Installer / Upgrade
- Installer: `install.sh`
  - Clones repo to `/var/www/jabali`
  - Uses `www-data` as the runtime user
  - Builds assets with Vite to `public/build`
  - Sets npm caches under `storage/`
- Upgrade: `php artisan jabali:upgrade`
  - Handles git safe.directory
  - Runs composer/npm when needed
  - Ensures writable permissions for `node_modules` and `public/build`
