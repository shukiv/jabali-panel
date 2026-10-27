# Applications (User)

`/jabali-panel/applications`. One-click installs for WordPress and 14 other popular apps.

## Available apps

WordPress, Moodle, Drupal, Joomla, NextCloud, MediaWiki, PrestaShop, OpenCart, phpBB, Matomo, MyBB, Pixelfed, Mautic, Mahara, SuiteCRM.

The subset your package allows may be smaller. See [Applications Framework](../applications.md) for the full registry.

## Installing

1. Click **Install new** → pick the app.
2. Pick the destination — one of your domains, root or a subdirectory.
3. Fill in app-specific fields (admin username, admin password — or generate one, admin email, site title, locale).
4. Submit.

The agent runs a 6-stage install pipeline (pre-flight, DB provision, download, configure, install, magic SSO write). Typical completion: ~25 seconds for WordPress; longer for larger apps (Moodle, SuiteCRM).

The success page displays the admin URL and credentials (once). Save them.

## Installed apps

The list shows every install: app, version, install path, last update, install size on disk.

Per-row actions:

- **Open Admin** — single-click sign-in to the app's admin panel via the self-deleting magic SSO file (60-second TTL).
- **Update** — manual update to the latest upstream version. Pause your traffic-sensitive work first; some updates run migrations that briefly degrade the site.
- **Clone** — currently WordPress-only. Other apps support clone via manual file copy + DB dump + URL search-replace.
- **Delete** — destructive. Removes the install directory, drops the DB and DB user, removes the install record. Asks twice.

## Auto-updates

Per-install toggle. When on, a weekly systemd-user timer runs `wp core update && wp plugin update --all && wp theme update --all` (or the app's equivalent). Off by default; opt in per install once you trust the upstream release cadence.

## What if my app is not in the list

The application registry is fixed at panel-release time (extending it requires a code change and a release). For apps not in the list, install manually:

1. Use [Databases](./databases.md) and [Database Users](./db-users.md) to provision the DB.
2. Use [Files](./files.md) (or SFTP) to upload the app's source.
3. Use a [Cron Job](./cron-jobs.md) for any scheduled tasks the app needs.

This is exactly what the one-click install does, just step by step.

## Open-source projects only

The registry is curated to open-source apps; commercial CMSes (Kentico, Sitecore, etc.) are not included.
