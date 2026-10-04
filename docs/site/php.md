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
- `log_errors`, `file_uploads`, and `short_open_tag` per domain (GH #1701). The
  agent sets all three on every request of every PHP domain: the domain's value,
  else the value it inherits (the pool's flag override, else the box's FPM
  `php.ini` for that PHP version). A value sent this way stays on the reused
  PHP-FPM worker, so without that a value set on one domain would carry over to
  a sibling domain on the same pool (checked on PHP 8.4 and 8.5).
  `display_errors` is pinned the same way (always Off unless the domain turns
  it on). A domain's own value takes precedence over a pool flag; use the
  package's PHP settings policy to stop a tenant from changing one.
- The value settings (`memory_limit`, `upload_max_filesize`, `post_max_size`,
  `max_input_vars`, `max_execution_time`, `max_input_time`, `error_reporting`,
  `date.timezone`) are set on every PHP domain the same way. A domain that
  leaves one unset gets the value it inherits: the pool's ini override, else
  the pool template's shared-hosting default (`memory_limit` 512M, upload and
  post size 512M, `max_execution_time` and `max_input_time` 300 s,
  `max_input_vars` 10000), else the box's FPM `php.ini`. An empty
  `date.timezone` is pinned as `UTC`, which is what PHP runs with. A value the
  agent cannot read with confidence is not pinned. The **(Default)** label next
  to each setting shows the same inherited value.
- `open_basedir` and `allow_url_fopen` per domain (GH #1701 Slice 3). Both are
  `PHP_INI_SYSTEM`, so the agent sends them through `fastcgi_param
  PHP_ADMIN_VALUE`, which replaces the pool's value for that request. They are
  pinned on every PHP domain like the settings above: a domain that sets none
  gets the `open_basedir` in its pool's own file and the box `php.ini`'s
  `allow_url_fopen` (a pool cannot override it). A domain's `open_basedir` is a
  list of `{DOCROOT}`, `{WEBSPACEROOT}`, `{TMP}` and absolute paths; the agent
  expands it and always adds `/run/mysqld/mysqld.sock` and
  `/run/jabali-wp-purge`, as the pool default does. A tenant may list only
  folders inside their home; an admin may add other paths but never `/`,
  another user's home, or `/root`, `/proc` or `/sys`. Both are admin only unless
  the package grants `tenant_privileged`.
- `disable_functions` stays per pool (per account and PHP version, set through
  the package): a function PHP disables stays disabled for the worker's
  lifetime, so it cannot differ between domains that share a pool. The
  package's **Disabled PHP functions** list (`php_disabled_functions`, GH
  #1701) is that list; by default it disables every command-execution
  function. When it leaves `system`, `exec`, `shell_exec`, `passthru`, `popen`,
  `proc_open` or `pcntl_exec` enabled, the agent gives the pool its own copy of
  the PHP Defense rules without the ban on those functions
  (`/etc/jabali/snuffleupagus/pools/<pool>.rules`, loaded through
  `/etc/php/<version>/jabali-ext/<pool>/90-jabali-php-defense.ini`). Every
  other PHP Defense rule still applies, and command-line PHP (cron, SSH) keeps
  the server-wide rules.
- The page's read-only **Disabled functions and paths** section shows what the
  pool serving the domain really runs with: each command-execution function
  (and anything else disabled or banned) as disabled by the hosting package,
  disabled server-wide in `php.ini`, blocked or logged by PHP Defense, or
  allowed; and the pool's `include_path` and `session.save_path`, from the
  pool's ini overrides or `php.ini`. The agent reads these from the files the
  pool loads (`GET /domains/:id/php-settings/effective`).
  An allowed function that the version's PHP-FPM build does not provide shows
  as **Allowed, not in this PHP build** (GH #1701). The agent lists the
  modules PHP-FPM loads (`php-fpm<version> -m` with the pool's ini layering)
  and maps each function to its extension with the CLI. A function is missing
  when PHP-FPM does not load its extension (Debian builds `pcntl` into the
  CLI only, so `pcntl_exec` / `pcntl_fork`), or when only the command-line
  SAPI registers it (`dl`, `cli_set_process_title`). Disabled and blocked
  functions keep those statuses. Once PHP-FPM loads the extension, the same
  package shows the function as allowed.
  When the `jabali-fpm-app` AppArmor profile is in enforce mode, each allowed
  function that starts a program (`exec`, `passthru`, `shell_exec`, `system`,
  `proc_open`, `popen`, `pcntl_exec`) also shows **Starts only the shell and
  cat** (GH #2001). The profile lets PHP-FPM start `sh`/`bash`/`dash`, `cat`
  and the `mail()` shim and nothing else, so `df`, `ls`, `grep`, `id` or
  `uname` fail with "Permission denied", and `cat` reads only the `/proc`
  files the base abstraction grants (`meminfo`, `cpuinfo`, `stat`; not
  `uptime` or `loadavg`). The agent reads the mode from `aa-status --json`.
  This is deliberate: `df` alone would expose the host mount table, including
  the per-user SFTP jail mounts. PHP's own `disk_free_space()`,
  `disk_total_space()`, `sys_getloadavg()` and `php_uname()` work under the
  enforced profile.
  `include_path` and `session.save_path` are not per-domain settings either: an
  admin value for them locks `ini_set()` on the shared worker for every domain
  on the pool. `mail.force_extra_parameters` is not offered: Jabali's mail shim
  always sends from the domain's relay identity, ignores `-f`, and would treat
  any other argument as an extra recipient.
- App installs with their own PHP location (a Drupal, Joomla, OpenEMR,
  InvoiceShelf or Flarum install in a subfolder, and osTicket's PATH_INFO
  handler) get the same per-domain values: the agent keeps
  `/etc/nginx/jabali/<domain>/php-pins.params` with the vhost's `PHP_VALUE`,
  `PHP_ADMIN_VALUE` and environment lines, and every app snippet's PHP location
  includes it. Before, a request there ran with whatever the previous request on
  the worker left behind.
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

PHP runtime hardening for every PHP version. It is off by default; an administrator turns on simulation or enforce mode under Security → PHP Defense. See [security.md](./security.md#snuffleupagus).

## CLI

```bash
jabali php version list                         # installed PHP versions
jabali php version install <version>            # install a PHP version, e.g. 8.4
jabali php ext list --version <version>         # extensions and their state
jabali php ext enable <ext> --version <version>
jabali php ext disable <ext> --version <version>
```
