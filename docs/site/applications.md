# Applications Framework

16 PHP apps, one wizard, one install pipeline. ADR-0033.

## Registry

Every app runs on the account's own PHP-FPM pool. An app that needs a database gets its own MariaDB database and user.

| App | What | Database | Notes |
|---|---|---|---|
| WordPress | CMS / blog / shop | MariaDB | Primary citizen (M10). Magic SSO file. Clone supported. |
| Drupal | CMS | MariaDB | |
| Joomla | CMS | MariaDB | |
| MediaWiki | Wiki | MariaDB | |
| DokuWiki | Wiki | none (flat files) | |
| phpBB | Forum | MariaDB | |
| Flarum | Forum | MariaDB | Gets a managed WAF exclusion for its API (CRS 920450). |
| OpenCart | E-commerce | MariaDB | `cli_install.php` failure-mode caveat handled (26-char ULID truncated to ≤20). |
| PrestaShop | E-commerce | MariaDB | |
| Moodle | LMS | MariaDB | Heavy. Recommend ≥2 GB RAM on the install's docroot user. Course files live in `.moodledata/` beside the docroot, readable and writable by the account only. |
| ITFlow | CRM / project management | MariaDB | Domain root only. The admin signs in by email. |
| InvoiceShelf | Invoicing | MariaDB | |
| OpenEMR | Health records | MariaDB | |
| osTicket | Helpdesk | MariaDB | Domain root only. |
| Nextcloud | File sync, calendars, contacts | MariaDB | Domain root only. PHP 8.3 to 8.5. See [Nextcloud](#nextcloud). |
| PrivateBin | Encrypted pastebin | none (flat files) | |

## Nextcloud

Nextcloud installs at the root of a domain or subdomain, for example `cloud.example.com`. It needs PHP 8.3, 8.4 or 8.5 on that domain. On any other version the install stops with a message before downloading anything. Switch the domain's PHP version and install again.

What the install sets up:

- **Files.** Nextcloud's code is in the docroot. The files people store are in `nextcloud-data`, beside the docroot (`/home/<user>/domains/<domain>/nextcloud-data`), so the web server can never serve them.
- **Background jobs.** Nextcloud runs them from a cron job, `Nextcloud Cron`, every 5 minutes. You can see it on the Cron page.
- **Web server.** Nextcloud's own routes, calendar and contact discovery (`/.well-known/caldav`, `/.well-known/carddav`) and its JavaScript modules are set up for you. Its code, config and data folders are never served. Uploads up to 512 MB go through in one request; the desktop and mobile apps upload bigger files in parts.
- **Firewall.** The Files app, the sync clients and calendar/contact sync use WebDAV methods (PROPFIND, PUT, MOVE…). The web application firewall blocks those methods everywhere else. A Nextcloud install gets a managed exclusion for that one rule (CRS 911100), on its own domain and its WebDAV paths only. Admins see it under `/jabali-admin/security` → CrowdSec → **WAF exclusions**.
- **Host.** Nextcloud answers on the domain it was installed on. It does not answer on `www.` + that domain.

Deleting the app removes Nextcloud's code, its database, the cron job, the firewall exclusion, and the `nextcloud-data` folder with every file stored in it. Download anything you need first.

## Install pipeline

Every app uses the same 6-step pipeline:

1. **Pre-flight** — verify the destination directory is empty, the chosen DB engine is available, the user has quota headroom, the chosen path is not already an install. The admin username and password you type must meet the app's own rules: its username characters and length, its password length or banned characters, and names it refuses (osTicket won't take `admin`). The form shows each rule under the field and checks it before anything is created. Blank fields get a generated value that always passes.
2. **DB provisioning** — create DB + DB user via the agent.
3. **Download** — fetch the app from upstream (cached on the panel host for repeat installs).
4. **Configure** — write the app's config file with DB creds + admin user + site URL.
5. **Install** — call the app's `cli_install` / `wp core install` / equivalent. Scan stdout for `ERROR:` markers and a "success" sentinel (silent-exit-0 scar — OpenCart in particular exits 0 on failure). The database and admin passwords reach the installer on stdin, never as command-line options or environment variables of the systemd unit it runs in.
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

## Python apps — build and status

When a Python app is created, or its `requirements.txt` changes, the panel builds it as the account's user: it creates the app's virtualenv, runs `pip install -r requirements.txt`, and installs the app server (gunicorn for WSGI, uvicorn for ASGI) if `requirements.txt` did not already. A first install of a real project can take several minutes, so the build runs in the background (GH #357). The app shows **building** until it finishes, then **running**, or **failed** with the reason on hover.

- A failed `pip install` shows the packages pip was working on and the last lines of its output. The panel retries it after 15 minutes, or straight away when `requirements.txt` changes.
- Each pip step stops after 8 minutes. A build that never finishes in that time shows **failed** with `timed out`.
- Restarting the agent stops a build that is running; the next pass starts it again, and pip keeps what it already installed.
- The build runs in the account's slice, under its package's CPU, memory and task limits, like the app itself (see [Resource limits](./resource-limits.md)). Each step (`python -m venv`, each `pip install`, and each step of a framework's first setup) runs in a scope of its own, `jabali-app-build-<app id>-<n>.scope`, which is stopped when the step ends: nothing a build starts keeps running after it.

## Python apps — sic

The Python Apps catalog includes **sic** (GH #2156), a community link
aggregator in the style of Lobsters or Hacker News, built on Django. Unlike the
framework entries it installs a complete app: the code of one pinned upstream
commit, checked against a pinned sha256 before it is unpacked. It needs
Python 3.12 or newer.

- Install it into a new, empty app directory. The install stops with an
  error if the directory already holds files.
- sic resizes avatars with ImageMagick. The first sic install on a server
  installs the distro's `libmagickwand` package; servers without a sic app
  never get it.
- The install makes the first admin account. Sign in as `admin` (or the
  address in `SIC_ADMIN_EMAIL`) with the initial password in
  `SIC_ADMIN_PASSWORD` (Env drawer), then change the password in sic.
  Changing either variable later does not change the account.
- sic sends sign-up and password-reset mail through `SIC_EMAIL_HOST`
  (default `localhost`, port 25), from `SIC_DEFAULT_FROM_EMAIL` (default
  `noreply@<domain>`).

Known limits: sic writes `http://` into the absolute links in its mail. Its
job that saves a text copy of each submitted page (for search) needs a helper
binary sic does not ship, so that job fails; stories, comments, tags and
accounts are not affected.

## Python apps — media directory

A Python app can serve a per-app **media directory straight from nginx** (GH
#878), parallel to the static-files mapping: `media_url` → `media_root` gets its
own nginx `alias` rule (1-hour cache, vs 30-day for static assets), so
user-uploaded media is served without going through the WSGI/ASGI worker.

## Database admin tooling

MariaDB databases open in **phpMyAdmin**; PostgreSQL databases open in
**Adminer** (6.1.1, with a ported single-sign-on plugin, GH #1405).
See [Databases](./databases.md).

## CLI

```bash
jabali app list
jabali app registry                                        # the app types you can install
jabali app install --domain <fqdn> --app-type wordpress --user <id> --wait
jabali app delete <install-id>
```

See [platform/cli.md#applications](./platform/cli.md#applications).
