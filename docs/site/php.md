# PHP

Multi-version PHP via Sury + per-user FPM pools.

## Per-version installation

`/jabali-admin/php-pools` lists all PHP versions installed on the host. The installer puts Sury's PHP repo on the system; subsequent versions can be added with `apt install php8.x-fpm` and friends — once installed they're picked up by the panel.

## Per-user pools

Each panel user gets a private PHP-FPM pool socket at `/run/php/jabali-<user>/fpm.sock`. The user must be a member of `www-data` (handled by `useradd`). Pool config lives at `/etc/php/<version>/fpm/pool.d/jabali-<user>.conf`, written by the agent.

Per-user pools mean:

- Per-user OPcache (no cross-tenant cache poisoning).
- Per-user `memory_limit`, `upload_max_filesize`, etc. (managed in the panel UI under PHP Settings).
- Per-user worker counts (computed from the user's package).

## Per-version extensions (M9.6)

`/jabali-admin/php-pools` → Extensions tab. Server-wide enable/disable for each extension on each installed PHP version. The agent's `phpext` package handles install/remove via `phpenmod`/`phpdismod`, then issues a graceful FPM reload.

`phpext` lives at `internal/phpext/` (repo root, Go internal rule, ADR-0031).

## Per-domain version

Each Domain row has a `php_pool_id` foreign key into `php_pools`. The vhost `fastcgi_pass` line is rendered from that pool's socket path. Change the version per-domain via Domains → Edit → PHP Version.

## User-facing PHP settings

`/jabali-panel/php-settings` exposes:

- `memory_limit`
- `upload_max_filesize`
- `post_max_size`
- `max_execution_time`
- `max_input_time`
- `max_input_vars`
- `display_errors` (off by default)
- `date.timezone`

The package the user is on caps each of these to a maximum the admin chose. Attempts to exceed the cap are clamped on save with a UI warning.

### Performance presets (GH #1332)

PHP Settings offers **per-version Performance tuning presets** — pick a profile
(e.g. balanced / high-traffic) and the Advanced view shows exactly what each
preset applies plus a live preview before you save, so a preset is never a black
box. Domain-scoped limits are labelled as such.

### Per-domain overrides (GH #1332)

Some values are set **per domain** rather than per user, with an **override
badge** on any domain that differs from the account default:

- `display_errors`, `error_reporting`, and `date.timezone` per domain (GH #1332).
- Per-domain environment variables passed to the FPM pool.
- Per-domain log + cron shortcuts and an **OPcache reset** button.

## OPcache + JIT (GH #1332)

Recent PHP versions (8.3+) ship JIT. Jabali leaves JIT off by default (silent CPU
spikes on some shared workloads). PHP Settings gives **per-version OPcache / JIT
controls** so you can enable JIT and tune OPcache only on the versions where your
app benefits, plus a one-click **OPcache reset**.

## Xdebug, Composer, extra extensions (GH #1332)

- **Per-version Xdebug toggle** with safe modes — turn Xdebug on for a single PHP
  version without slowing every pool (Xdebug is a Zend extension loaded per-slug
  via a scan dir).
- **Per-user Composer version selector** — choose which Composer major version
  the account uses.
- **Per-version opt-in extra extensions** — a tenant can opt a version into an
  allow-listed set of extra extensions beyond the server-wide defaults.

## FPM slow log (GH #1332)

Each pool can emit a **per-pool FPM slow log** for requests over a threshold, so a
slow endpoint is traceable per account without touching the global config.

## Snuffleupagus

PHP hardening (no-eval, no-include-from-uploads, taint tracking) is on by default — see [security.md](./security.md#snuffleupagus).

## CLI

```bash
jabali php list                       # installed PHP versions and pool counts
jabali php install <version>          # install a new PHP version
jabali php enable-ext <version> <ext>
jabali php disable-ext <version> <ext>
```
