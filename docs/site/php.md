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

Each Domain row has a `php_pool_id` foreign key into `php_pools`. The vhost `fastcgi_pass` line is rendered from that pool's socket path. Change the version per-domain via Domains → Edit → PHP Version. One-click apps with their own nginx rules (subdirectory installs, osTicket, OpenEMR, InvoiceShelf, Flarum, Nextcloud) pass PHP to the same pool, and follow the domain when its version changes.

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
  While the profile is in complain mode (it only logs) or not loaded, those
  functions show **Can start any program** instead: they can run anything the
  site's user can, and PHP Defense lifts its exec ban for packages that allow
  them, so nothing else limits them. The agent reports `exec_confinement`
  (`enforce`, `complain`, `none` for AppArmor off or the profile not loaded,
  `unknown` when `aa-status` exits 4/42 or cannot run); `exec_confined` stays
  true exactly for `enforce`. The package editor reads the same mode from the
  admin AppArmor status and warns when a package allows an exec function.
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

## Composer runs (GH #2276)

A user whose hosting package includes Composer (`composer_enabled`, off by default) can run Composer for a domain from the panel (ADR-0176). The commands are install, update, require, remove, audit, outdated, show, validate, dump-autoload and diagnose. `validate` skips the checks that only matter for publishing a package (`--no-check-publish`). The API is `/api/v1/domains/{id}/composer`; an API token limited to scopes needs `read:ssh` to read runs and `write:ssh` to start or cancel them.

- **Where it runs:** each run is a transient unit, `jabali-composer-<run id>.service`, in the user's slice, as the user. It runs in their SSH sandbox, on the PHP version the domain is served by, with the Composer version their channel selects. `COMPOSER_HOME` is `~/.composer`.
- **Limits:** a run stops after 30 minutes, at 2 GB of memory, or at 256 tasks, on top of the package's limits. Only one run per domain is in progress at a time.
- **Rules:** PHP Defense lifts the command-execution and `phpinfo` bans for the run (see [Snuffleupagus](admin/snuffleupagus.md#composer-runs-from-the-panel)). A run that PHP Defense blocks anyway is marked failed.
- **Results:** the panel keeps each run's state, exit code and the last 256 KiB of its log.
- **Cancelling:** a run can be cancelled while it is in progress. Cancelling stops Composer but does not undo what it has already written: `composer.json`, `composer.lock` and `vendor/` may be partly changed.

## Git deployment (GH #2277)

A user whose hosting package includes Git deploy (`git_deploy_enabled`, off by default; the **Git deploy** switch in the package editor) can connect a domain to a Git repository and deploy it (ADR-0178), from the domain's **Git** tab ([Domains](user/domains.md#git)) or the API. The API is `/api/v1/domains/{id}/git`; an API token limited to scopes needs `read:ssh` to read it and `write:ssh` to change it.

- **Repository:** an `https://` URL from GitHub, GitLab, Bitbucket or any Git host, with no username or token in it. SSH URLs (`git@…`) are not accepted.
- **Private repositories:** give a read-only access token, and the username your Git host expects with it: for GitHub, your GitHub username; for a GitLab personal or project token, any name (GitLab ignores it); for a GitLab deploy token, the token's own username; for a Bitbucket repository access token, `x-token-auth`. Left empty, the panel sends `x-access-token`, the name GitHub App installation tokens use.
- **Saving** reads the repository's branches with the settings first and stores nothing if that fails. An empty branch picks the repository's default branch.
- **The token** is stored encrypted and is never shown again. To change the repository's host, enter the token again (or remove it for a public repository). When the domain moves to another account, its repository settings and token are removed.
- **Where git runs:** as the user, in their SSH sandbox, with no config of their own, over https only. The server won't connect to a host that resolves to a private or local address, and it doesn't follow redirects: a repository that has moved needs its new URL.
- **Backups** don't carry the repository settings or the token; enter them again after a restore.

### Deploying

`POST /api/v1/domains/{id}/git/deployments` deploys the saved branch. Each deployment builds a new release in `~/domains/<domain>/releases/<id>` and only then makes it live, so a deployment that fails leaves the site as it was:

1. git fetches the branch (the latest commit only). The token is removed from the server before any of the repository's code runs.
2. **Shared paths** are linked in from `~/domains/<domain>/shared`: files and directories that stay the same across releases, such as `.env` or `storage`. A shared path that isn't in `shared/` yet is taken from the release; one missing from both stops the deployment. A hidden path (`.env`) can't be inside the directory the site serves, where anyone could download it.
3. `composer install` runs when the project has a `composer.lock` and no `vendor/`, if the hosting package also includes Composer runs. Without them, such a project can't be deployed: commit `vendor/`, or ask for Composer runs.
4. The **deploy commands** run in the new release, one per line, at most 20: `php <file> [args]` (such as `php artisan migrate --force`), `php8.3 <file> [args]` for another installed PHP version, or `wp [args]`. Paths are relative to the release. No shell runs them, so pipes, `&&`, redirections and variables don't work, and no PHP option may come before the file.
5. `~/domains/<domain>/current` switches to the new release. After the first deployment the domain's document root becomes `~/domains/<domain>/current/<web directory>` (the **web directory** is the folder the site serves from, `public` for Laravel; empty serves the repository's root).

The five newest releases are kept. `POST /api/v1/domains/{id}/git/releases/{release}/activate` makes an earlier one live again (a rollback); `GET .../git/releases` lists them. A deployment in progress can be cancelled; until its release goes live, the site is unchanged.

Anything the site writes inside a release, outside its shared paths, is gone after the next deployment: make uploads and caches shared paths. Submodules are not fetched.

### Deploying on push

A webhook deploys the saved branch whenever it is pushed. `POST /api/v1/domains/{id}/git/webhook` creates it and answers with its **URL** and a **secret**. The secret is shown this once: posting again gives a new secret (the URL stays, and the old secret stops working). To use a secret the Git host made instead, send it as `{"secret": "..."}`: GitLab's signing token (`whsec_...`) or a Bitbucket generated secret.

Add the webhook on the Git host, for push events, with the URL and the secret:

| Host | Where | Secret |
|---|---|---|
| GitHub | Repository Settings → Webhooks | Content type **application/json** (GitHub's default, form-encoded, is refused); the secret in **Secret** |
| GitLab | Settings → Webhooks | Either GitLab's **signing token**, sent to the panel as `secret`, or the panel's secret in **Secret token** (older GitLab) |
| Bitbucket | Repository settings → Webhooks, trigger *Repository push* | The panel's secret, or Bitbucket's generated one sent to the panel |
| Gitea, Forgejo | Settings → Webhooks | POST, content type **application/json**, the secret in **Secret** |

Any other sender can post `{"ref": "refs/heads/<branch>", "after": "<commit>"}` as JSON with an `X-Hub-Signature-256: sha256=<hex HMAC-SHA256 of the body>` header. The body must differ from one push to the next (the commit does that): a body the panel has seen before is a duplicate.

What a delivery does:

- A push to the saved branch, with a valid signature, deploys it exactly as `POST .../git/deployments` does. The deployment fetches the branch as it is then; nothing in the delivery is deployed.
- A push during a deployment waits, and one more deployment starts within a minute of that one ending. Pushes meanwhile wait together, so the site ends on the newest commit. A webhook starts at most six deployments a minute; more pushes wait the same way.
- Another branch, a tag, a deleted branch or the host's test ping changes nothing.
- A delivery the panel has seen in the last seven days changes nothing, so a retry deploys once. That includes the host's **Redeliver**, and a second press of GitLab's **Test → Push events**, which sends the same last push again; use the Deploy button to deploy again.

`GET .../git/webhook` shows the URL, whether a push is waiting, and what the last delivery did: `started`, `queued`, `ignored`, `duplicate`, `failed`, or `refused:<reason>` (for example `refused:git_deploy_not_in_package`). `DELETE .../git/webhook` removes the webhook; removing the Git settings removes it too.

The Git host has to reach the panel at the webhook's URL and accept its TLS certificate: a panel that only admits certain addresses, or uses a self-signed certificate, receives nothing.

## FPM slow log (GH #1332)

Each pool can emit a **per-pool FPM slow log** for requests over a threshold, so a
slow endpoint is traceable per account without touching the global config.

## PHP error log

PHP warnings, notices and errors are written to files in the account's `~/logs`:

- `~/logs/<domain>.php-error.log`, one per domain (the vhost sets `error_log` in its `PHP_VALUE`);
- `~/logs/php-error.log`, the pool's fallback for anything not tied to a domain.

The Logs page shows a domain's file live under **PHP Errors**; the agent reads it (`php.error_log.read`) without following links, because the tenant owns `~/logs`. PHP no longer sends them to nginx. Under PHP-FPM, Debian's `php.ini` sends logged errors to FastCGI stderr, which nginx reads into the buffer that holds the response headers. A page with many warnings then failed with `502` and `upstream sent too big header while reading response header from upstream` in the domain's nginx error log, even when the same site works on another server.

Both settings are `php_value`, so a tenant can still choose another file with `error_log` in `.user.ini` or with `ini_set` (WordPress `WP_DEBUG_LOG` does this). The agent creates `~/logs` (tenant-owned, `0750`) on every pool apply and PHP vhost write; if `~/logs` is missing or not writable, PHP falls back to stderr as before. The agent also writes `/etc/logrotate.d/jabali-phperr-<user>`, which rotates these files daily, keeps 7 and compresses the older ones. logrotate runs it as the tenant (`su <user> <user>`), never as root, because the tenant owns the directory. The file is removed with the account.

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
