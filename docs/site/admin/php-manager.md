# PHP Manager

`/jabali-admin/php-pools`. The list of installed PHP versions on the host, with per-version FPM tuning and an extension manager (M9.6).

## Versions list

Each installed PHP version (Sury packages, currently 8.1 through 8.5+) shows:

- Version number
- Number of user pools currently using this version
- Default tunables (`pm`, `pm.max_children`, `pm.start_servers`, `pm.min_spare_servers`, `pm.max_spare_servers`)
- Per-version OPcache settings (`opcache.memory_consumption`, `opcache.max_accelerated_files`)
- JIT toggle (default off)

Editing a default propagates to every user pool of that version through the reconciler.

## Adding a new PHP version

PHP versions are installed at the OS level via `apt`:

```bash
apt install php8.x-fpm php8.x-cli php8.x-mbstring php8.x-mysql …
```

The reconciler detects the new version on the next tick and adds it to this list. The version is then available for selection in [Domains](./domains.md) → Edit → PHP Version.

## Removing a PHP version

Forbidden if any user pool is using the version. Reassign affected domains first, then `apt purge php8.x-fpm` removes the version from the host; the reconciler removes it from the list on the next tick.

## Extensions tab

`/jabali-admin/php-pools` → Extensions. Server-wide enable / disable for each extension on each installed PHP version. Backed by the `internal/phpext/` Go package (M9.6, ADR-0031).

- Enable: runs `phpenmod <ext>` for the chosen version, then issues a graceful FPM reload (no in-flight request loss).
- Disable: runs `phpdismod <ext>`, then reload.
- Some extensions require a corresponding `apt install` first; the row indicates the missing package.

## Per-pool view

Click a version row to drill into the per-user pools currently using that version. Each row links to the user's edit page; from there the admin can move the user to a different PHP version, or to a different per-user tuning override.

## CLI

```bash
jabali php version list                         # installed PHP versions
jabali php version install <version>            # install a PHP version, e.g. 8.4
jabali php ext list --version <version>         # extensions and their state
jabali php ext enable <ext> --version <version>
jabali php ext disable <ext> --version <version>
```
