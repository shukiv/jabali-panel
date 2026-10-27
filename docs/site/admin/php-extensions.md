# PHP Extensions

PHP Manager (`/jabali-admin/php-pools`) → **PHP Extensions** tab. Server-wide PHP extension manager. Enables and installs PHP extensions per managed PHP version.

## Scope

Server-wide, not per-user. An extension enabled here is available to every domain running that PHP version. Per-user PHP runtime tuning (memory_limit, upload_max_filesize, etc.) lives in [PHP Settings](../user/php-settings.md); this tab is the install/enable plane.

## What it lists

The tab is scoped to one PHP version at a time (the versions installed on the host — default `8.4`; others added at install time via `JABALI_PHP_VERSIONS`). For that version the table shows each extension in the registry (`internal/phpext`) with:

- Extension name (`imagick`, `redis`, `gnupg`, `intl`, …).
- **Installed** state — whether the backing distro package is present.
- **Enabled** state — whether the module is loaded (a `mods-available` ini is symlinked into the version's `conf.d`).
- Whether it is **built in** — bundled into `php<v>-common`/`php<v>-cli`. Built-in extensions (`opcache`, `ctype`, `fileinfo`, `pdo`, …) have no install/remove action; only enable/disable.

## Actions

Each action targets one extension on one PHP version:

- **Install** — `apt-get install php<v>-<ext>` (Sury packages), then `phpenmod` to load it.
- **Remove** — `apt-get remove` the backing package.
- **Enable** — `phpenmod` an already-installed extension.
- **Disable** — `phpdismod`.

After any action the affected version's FPM pools are reloaded so `phpinfo()` reflects the change. Built-in extensions reject install/remove ("use enable/disable"); `mysql` is ambiguous on enable/disable (use `mysqli` or `pdo_mysql`).

## Behind the scenes

- Extensions come from the **Sury** distro packages (`packages.sury.org/php`) as `php<v>-<ext>`. There is no PECL build step — if Sury ships no package for a name, it is not offered.
- A "group" package can back several extensions: e.g. the `xml` package provides `dom`, `simplexml`, `xml`, etc. — install/remove acts on the package, enable/disable acts on the individual module name.
- Enable/disable uses Debian's `phpenmod`/`phpdismod`, which symlink `mods-available/<ext>.ini` into the per-version `conf.d`.
- The agent reads fresh filesystem state as the verdict (dpkg DB + conf.d symlinks), not the apt exit code, and reloads FPM once per action — reloads are not storm-prone because the tab applies one extension at a time.

## Common operations

| Goal | Action |
|---|---|
| Install `imagick` for PHP 8.4 | Select PHP 8.4 → **Install** on the `imagick` row. |
| Turn on a built-in like `opcache` | Select the version → **Enable** on the `opcache` row (no install). |
| Roll back `xdebug` from a staging version | Select that version → **Remove** on the `xdebug` row. |

## Caveats

- Only extensions packaged by Sury are available; there is no source/PECL build path.
- Enabling Xdebug on a PHP version slows every site on that version. Enable it only on a version you can scope to staging.
- Removing a "group" package (e.g. `xml`) takes every extension it provides with it.

## CLI

The same operations are available from `jabali php ext` (each requires `--version`):

```bash
jabali php ext list --version 8.4
jabali php ext install <ext> --version 8.4
jabali php ext enable  <ext> --version 8.4
jabali php ext disable <ext> --version 8.4
jabali php ext remove  <ext> --version 8.4
```

See `internal/phpext/` in the panel-api repo for the extension registry and `panel-agent/internal/commands/php_ext_*.go` for the apply/list logic.
