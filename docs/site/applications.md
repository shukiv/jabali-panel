# Applications Framework

15 apps, one wizard, one install pipeline. ADR-0033.

## Registry

| App | What | DB | Notes |
|---|---|---|---|
| WordPress | CMS | MariaDB | Primary citizen (M10). Magic SSO file. Clone supported. |
| Moodle | LMS | MariaDB / PostgreSQL | Heavy. Recommend ≥2 GB RAM on the install's docroot user. |
| Drupal | CMS | MariaDB / PostgreSQL | Composer-based install. |
| Joomla | CMS | MariaDB | |
| NextCloud | File sync + collab | MariaDB | Requires Redis + cron — both auto-wired. |
| MediaWiki | Wiki | MariaDB | |
| PrestaShop | E-commerce | MariaDB | |
| OpenCart | E-commerce | MariaDB | `cli_install.php` failure-mode caveat handled (26-char ULID truncated to ≤20). |
| phpBB | Forum | MariaDB | |
| Matomo | Analytics | MariaDB | |
| MyBB | Forum | MariaDB | |
| Pixelfed | Photo sharing (Fediverse) | MariaDB | |
| Mautic | Marketing automation | MariaDB | |
| Mahara | E-portfolio | MariaDB | |
| SuiteCRM | CRM | MariaDB | |

## Install pipeline

Every app uses the same 6-step pipeline:

1. **Pre-flight** — verify the destination directory is empty, the chosen DB engine is available, the user has quota headroom, the chosen path is not already an install.
2. **DB provisioning** — create DB + DB user via the agent.
3. **Download** — fetch the app from upstream (cached on the panel host for repeat installs).
4. **Configure** — write the app's config file with DB creds + admin user + site URL.
5. **Install** — call the app's `cli_install` / `wp core install` / equivalent. Scan stdout for `ERROR:` markers and a "success" sentinel (silent-exit-0 scar — OpenCart in particular exits 0 on failure).
6. **Magic SSO** — write the self-deleting `jabali-sso-*.php` shim into the install dir.

## Admin controls

`/jabali-admin/applications` — per-app:

- Global enable/disable (users can't install disabled apps).
- Pinned version (vs. "latest").
- Per-app PHP extension prerequisites (validated against the user's PHP version pool).

## User flow

`/jabali-panel/applications`:

- Lists installed apps (with version, install path, last update).
- "Install new" → app picker → install wizard.
- "Open Admin" → magic SSO redirect.
- "Update" → manual update (separate from auto-update timer).
- "Clone" → currently WP-only, others on the roadmap.
- "Delete" → destructive teardown.

## Python apps — media directory

A Python app can serve a per-app **media directory straight from nginx** (GH
#878), parallel to the static-files mapping: `media_url` → `media_root` gets its
own nginx `alias` rule (1-hour cache, vs 30-day for static assets), so
user-uploaded media is served without going through the WSGI/ASGI worker.

## Database admin tooling

MariaDB databases open in **phpMyAdmin**; PostgreSQL databases open in
**Adminer** (upgraded to 6.0.1 with a ported single-sign-on plugin, GH #1405).
See [Databases](./databases.md).

## CLI

```bash
jabali app list [--user <id>]
jabali app install --user <id> --domain <name> --app wordpress
jabali app delete <install-id>
```

See [platform/cli.md#applications](./platform/cli.md#applications).
