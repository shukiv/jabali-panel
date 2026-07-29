# ADR-0161 — ionCube Loader: server-wide per PHP version, composed into the PHP Extensions surface

**Status:** Accepted. **Amends** [ADR-0031](0031-php-extensions-management.md)
(PHP extensions — server-wide per version, live from dpkg, fixed allowlist).
Shipped in serial, individually-reviewed slices: delivery (#757), a first
per-user/per-domain cut (#755/#758) that was then **reverted** in favour of this
server-wide design (#761), CLI parity + this ADR. Blueprint at
`plans/gh-ioncube-loader.md`; mechanics in the memory note
`reference_ioncube_loader_mechanics`.

## Context

Restoring/hosting WordPress sites that ship ionCube-encoded PHP (e.g. the
`woocommerce-rivhit-sync` plugin on arizot-e.com) failed with the ionCube
"Loader needs to be installed" notice: jabali had no way to install or enable the
ionCube Loader `zend_extension`. Doing it by hand is a box-wide change on a shared
host and never survives `jabali update`.

Two things make ionCube different from the ADR-0031 extension catalogue:

1. **It is not a dpkg package.** apcu/redis/intl/… are `php<v>-<ext>` Debian
   packages managed by `apt install` + `phpenmod`. The ionCube Loader is a
   closed-source blob downloaded from ionCube. So it cannot ride the ADR-0031
   apt/phpenmod path.
2. **Encoded files are PHP-version-locked**, so the loader is inherently a
   per-version concern.

ADR-0031 deliberately made extensions **server-wide per version** and rejected
per-pool, reasoning "PHP-FPM shares a single module loader per master; per-pool
extensions aren't a thing the runtime supports." Since then jabali runs a
**per-user FPM master** (`jabali-fpm@<user>`), which *does* make per-tenant
`zend_extension` loading technically possible (a per-user `PHP_INI_SCAN_DIR`).
A first cut of this feature used exactly that. It was reverted (see below).

## Decision

**ionCube is managed server-wide per PHP version**, exactly like the rest of the
extension catalogue, and is **surfaced as a row in the PHP Extensions tab** —
not a separate page, not per-domain.

- **Delivery** (kept from the first cut): panel-api fetches the official loaders
  tarball, verifies each `.so` against a **pinned per-version sha256** catalogue
  (`panel-api/internal/ioncube`, loader v15.5.0, PHP 7.4/8.1/8.2/8.3/8.4/8.5),
  and hands the verified bytes to the agent — the agent has no outbound HTTPS
  (ADR-0050). The agent re-verifies and writes to `/usr/lib/php/ioncube/<ver>/`
  (already permitted by the `jabali-fpm-app` AppArmor `/usr/lib/php/** mr` rule —
  no profile change).
- **Enable** = agent `php.ioncube.server_set {version, enabled}` writes
  `/etc/php/<ver>/fpm/conf.d/00-ioncube.ini` (named `00-` so it sorts before
  `10-opcache.ini` — ionCube fatals unless its loader is the first
  `zend_extension`) and reloads **every** FPM master on that version
  (`reloadVersionFPMUnits`). conf.d is loaded natively by all pools of the
  version — no per-user scan dir.
- **State is live** (mirrors ADR-0031): loader-installed = the `.so` exists;
  version-enabled = the conf.d ini exists. `php.ioncube.status` returns
  `{installed_versions, enabled_versions}`. No DB row, no reconciler, no drift.
- **The agent's dpkg `php.ext.*` path stays pure.** ionCube is composed in at the
  **panel-api layer**: `GET /admin/php/versions/:v/extensions` appends an
  `ioncube` pseudo-row from `php.ioncube.status`, and
  `POST .../extensions/ioncube/apply` routes install→fetch+install,
  enable/disable→`server_set`, remove→uninstall. The SPA renders the composed
  row with the same Install/Enable/Disable/Remove controls as any extension —
  zero SPA change. The CLI (`jabali php ext {list,install,remove,enable,disable}`)
  intercepts `ioncube` the same way for parity.

## Alternatives Considered

### Per-user / per-domain isolation (the reverted first cut)
- **What**: a per-user `PHP_INI_SCAN_DIR` in `fpm-exec` + a per-domain toggle, so
  one tenant could load ionCube without its neighbours on the same version.
- **Why not**: the ionCube Loader is a benign, trusted decoder — not
  tenant-supplied code — so per-tenant isolation of it buys almost nothing, while
  the version dimension alone already solves the encoded-app case (a site sets a
  supported PHP version; the loader is enabled for that version). cPanel/Plesk
  install ionCube server-wide too. The per-user machinery (scan-dir in
  `fpm-exec`, `pool_set`, tenant `/domains/:id/php-ioncube`) was extra surface for
  marginal value, so it was removed (#761).

### A real dpkg row inside the ADR-0031 `php.ext.*` handlers
- **Why not**: it would special-case a non-apt entry inside the agent's
  dpkg-driven `ext.list`/`ext.apply`, breaking ADR-0031's "live from dpkg"
  invariant. Composing at the panel-api layer keeps the agent path pure while
  still presenting ionCube as a row.

## Consequences

### Positive
- Solves the arizote case: set the site's PHP version, then Install + Enable
  ionCube for that version in PHP Manager → PHP Extensions (or `jabali php ext`).
- No AppArmor change, no migration, no reconciler, no drift.
- ADR-0031's dpkg mechanism is untouched; all ionCube specialness is isolated to
  `panel-api/internal/api/php_extensions.go` + `panel-api/internal/ioncube` +
  three agent RPCs.

### Negative
- The loader catalogue (URL + per-version sha256) is pinned in Go, so a loader
  bump upstream requires a panel release — same cadence contract as the ADR-0031
  extension catalogue. A mismatched sha hard-fails install rather than installing
  an unverified blob.
- Enabling reloads every master on the version (a few seconds of USR2 reloads on
  a busy box), the same blast radius as any server-wide ADR-0031 extension.

## Implementation

- `panel-api/internal/ioncube` — pinned catalogue + `Fetcher` (download, extract,
  verify).
- Agent `php.ioncube.install` / `uninstall` / `server_set` / `status`
  (`panel-agent/internal/commands/php_ioncube_{install,serverwide}.go`).
- panel-api row composition + apply routing in `php_extensions.go`; CLI parity in
  `panel-api/cmd/server/php_cmd.go`.
- Box-verified on testserver under `jabali-fpm-app` ENFORCE: install real 8.4
  loader → enable → a site on 8.4 serves ionCube v15.5 with no per-user config →
  uninstall blocked while enabled → disable → uninstall.
