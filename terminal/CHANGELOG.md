# Changelog

All notable changes to jabali-terminal are recorded here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
uses semantic versioning for tags.

## [Unreleased]

## [0.1.1] — 2026-04-13

Post-v0.1.0 stabilisation. Nothing here is breaking for existing
installs — the reverse-proxy migration from nginx → Caddy matches
what the panel was actually running on, and every new security
control is additive.

### Added

- **Root Terminal in the main nav** — moved out of the `Tools` group,
  positioned right after `Services` as a top-level entry.
- **Conditional 2FA re-auth** — admins with Fortify 2FA provisioned
  still must submit a fresh TOTP every session; admins without 2FA
  configured skip that field. Password re-check, rate limit (3/min per
  admin+ip), and 15-minute lockout after 5 failed attempts are unchanged.

### Changed

- **Sessions merged into the Terminal page as a tab.** The old separate
  `Pages\Sessions` Filament page (slug `/jabali-admin/terminal/sessions`)
  is gone. Session transcripts now live as a second tab inside
  `Pages\Terminal` — the page has `Terminal` and `Sessions` tabs,
  switched client-side via Alpine (no server round-trip on tab change).
  Upside: one less route + nav entry, audit tab inherits the Terminal
  page's framebusting + CSP + `canAccess()` gate by construction, and
  the re-auth modal remains tab-scoped (still required for the PTY side,
  never required for browsing transcripts — same posture as the old
  Sessions page). Livewire methods `refreshSessions` / `viewTranscript`
  / `closeTranscript` on `Pages\Terminal` mirror `canAccess()` for
  defense-in-depth on XHRs.
- **Reverse proxy: Caddy instead of nginx.** `install.sh` now writes a
  marker-guarded block into `/etc/jabali/Caddyfile`. The panel has
  always been FrankenPHP/Caddy on `:8443` — the previous nginx include
  would have landed the terminal proxy inside a user vhost. The nginx
  code path is gone. `header_up X-Real-IP {remote_host}` is set
  explicitly (SEC-REV-6; Caddy doesn't set it by default).
- **xterm.js → `@xterm/*` scoped packages.** The legacy `xterm` /
  `xterm-addon-fit` / `xterm-addon-web-links` packages were deprecated
  and their internal APIs drifted out of sync with modern addons. The
  bundle now pins:
  - `@xterm/xterm` `^5.5.0`
  - `@xterm/addon-fit` `^0.10.0`
  - `@xterm/addon-web-links` `^0.11.0`
  - `@xterm/addon-canvas` `^0.7.0` (new — see Fixed below)
- **Filament-native UI primitives.** Re-auth, connection status, and
  session-closed panels now use `x-filament::section`, `x-filament::button`,
  `x-filament::badge`, `x-filament::input` instead of hand-rolled Tailwind
  chrome. Matches the rest of the panel's look and feel.
- **Route registration moved to `JabaliTerminalServiceProvider::boot`.**
  `POST /jabali-admin/terminal/session` + its named RateLimiter used to
  live in the plugin's `boot()`, which fires too late (after middleware)
  and not at all during CLI commands like `route:cache`. The
  ServiceProvider is wired via the parent panel's `bootstrap/providers.php`
  with a `class_exists()` guard so the panel still boots when the addon
  is uninstalled.

### Security

- **Defense-in-depth canAccess() on Sessions Livewire methods.**
  `refreshSessions` and `viewTranscript` on `Pages\Terminal` now re-run
  `abort_unless(static::canAccess(), 403)` on every Livewire XHR, not
  just on initial mount. Filament 4's `canAccess()` is not
  automatically re-invoked on Livewire method calls; without this, a
  session whose admin role was revoked mid-flight (cookie still valid,
  `isAdmin()` now false) could keep reading transcripts until the
  cookie expired.
- **Livewire rate limit on transcript calls.** 60/min per (admin, ip)
  on `refreshSessions` and `viewTranscript`. The `POST /terminal/session`
  mint route was already bounded at 3/min; this closes the corresponding
  gap on the audit surface so a compromised admin session can't use
  unlimited Livewire XHRs to flood the daemon's unix socket. 429s bubble
  up as "too many transcript requests, wait a minute".
- **Panel-side transcript-name check mirrors the daemon's `".."`
  substring rejection.** No traversal risk existed before — the charset
  already excluded `/`, and the daemon's `realpath`+`startswith` chroot
  was authoritative — but matching the daemon's idiom 1:1 (`str_contains
  $name, '..'`) keeps the posture consistent between panel and daemon
  and quiets static-analysis scanners.

### Fixed

- **Blank xterm.js on every connect.** xterm.js 5.x does not ship a
  renderer in the core package — a renderer addon must be loaded and
  activated to populate `_renderService._renderer.value`. Without one,
  the first `fit()` / `resize()` / `write()` schedules an internal
  `_refreshAnimationFrame` that reads `_renderer.value.dimensions` and
  throws async with `"can't access property 'dimensions',
  this._renderer.value is undefined"` from xterm's own rAF — not
  catchable by any user-level `try/catch`. `@xterm/addon-canvas` now
  loads after `term.open()` and installs the canvas renderer
  synchronously via `setRenderer()` in `activate()`. Paired blade-side:
  mount deferred via `$nextTick + requestAnimationFrame` with a
  zero-size retry guard, PTY bytes arriving before mount completes are
  buffered and flushed on mount, and every `fit()` call site is wrapped
  in a `safeFit()` helper.
- **`wss://localhost/terminal-ws` when the browser wasn't on localhost.**
  The daemon's `ws_url` field contains `request.host` from inside the
  unix socket — which is always `"localhost"`. The panel now ignores
  that field and builds the URL from `$request->getHttpHost()` +
  `isSecure()`.
- **"daemon unavailable" after fresh install.** The conf file was seeded
  `0640 root:root`, so PHP-FPM under `www-data` couldn't read
  `hmac_secret` and `JabaliTerminalClient::isAvailable()` silently
  returned false. `install.sh` now seeds (and re-seeds, on idempotent
  re-install) as `0640 root:www-data`.
- **Stray `@vite` in the Blade JavaScript comment.** Blade compiles
  `@vite` even inside `//` comments because Blade parses before JS
  lexing. Wrapped the references in `{{-- ... --}}`.
- **Filament 4 `navigationGroup` type** must be `UnitEnum|string|null`,
  not `?string`. Parent panel's Filament 4 upgrade exposed the mismatch.
- **`make test` on a clean checkout.** The daemon Makefile now
  bootstraps `.venv/` via `pyproject.toml` dev extras on first use
  instead of assuming the operator has one set up.
- **pytest async warnings** — CI config moved to `asyncio_mode=auto`
  and runs from `daemon/` so the test root matches the package root.

## [0.1.0] — 2026-04-12

First release. Browser-based root shell for Jabali Panel. Architecture
mirrors jabali-security: Python daemon over a unix socket behind nginx,
Filament plugin with `class_exists()` autodiscovery, marker-guarded
install.sh that never touches `AdminPanelProvider.php`.

### Added

- **Daemon** (`daemon/`, Python 3.12+, asyncio)
  - `GET /health`, `POST /api/v1/session`, `GET /api/v1/sessions[/{name}/transcript]`,
    `GET /terminal/ws` — all served over `root:www-data` 0660 unix socket
  - 104-byte tokens (admin_id, ip, issued_at, expires_at, nonce, HMAC-SHA256)
    with 60s TTL, single-use via SQLite-backed nonce store (SEC-REV-1)
  - Handshake-challenge WS auth (SEC-REV-2): token never in URL
  - Strict verification order: expiry → nonce → IP → HMAC (SEC-REV-5)
  - Request-HMAC verification on `POST /api/v1/session` with 30s clock-skew
    window (defense-in-depth over socket filesystem ACL)
  - Per-session audit log `<date>_<admin>_<session_id>.log`, HMAC-sealed
    on close (SEC-REV-4), unclosed logs sealed on daemon restart (SEC-REV-10)
  - Idle timeout fires only when both stdin AND stdout are quiet (SEC-REV-3),
    hard 1h cap enforced daemon-side (SEC-REV-9)
  - 4KB paste cap enforced server-side (defense-in-depth over the UI cap)
- **Panel plugin** (`panel/`, Filament)
  - `JabaliTerminalPlugin` autodiscovered via `class_exists()` — no
    `AdminPanelProvider.php` sed
  - `Pages/Terminal` renders the xterm.js UI; `Pages/Sessions` is a
    read-only audit index of the last 100 transcripts
  - `Http/Controllers/TerminalSessionController` handles re-auth at
    `POST /jabali-admin/terminal/session`. Fresh password + 2FA required
    per session (no opt-out). Rate limit 3/min per (admin, ip); 5 failed
    attempts in 15 min trigger a hard lockout
  - `PreventFramingMiddleware` pins `X-Frame-Options: DENY` and merges
    `frame-ancestors 'none'` into any existing CSP (SEC-REV-8)
  - xterm.js bundle computes the challenge HMAC with Web Crypto (no
    third-party JS crypto), scrubs the token from closure memory after
    the auth frame is sent
- **install.sh**
  - Python venv + pip install, systemd unit, sudoers, logrotate
  - Marker-guarded nginx include (`# === JABALI-TERMINAL NGINX {BEGIN,END} ===`)
    with nginx -t validation + rollback on failure
  - `jq`-based npm dep merge into the panel's `package.json`, triggers
    `npm install && npm run build` only when deps actually changed
  - Symmetrical uninstall that removes every installed artifact without
    touching `AdminPanelProvider.php`
- **Tests**
  - Python unit tests for auth, audit, config, nonce store
  - PHP feature test `TerminalAuthTest` covering the auth surface of
    the dedicated session route
  - `tests/e2e_socket.py` drives the daemon end-to-end over the unix
    socket (handshake, PTY, IP binding, replay, transcript seal)

### Documentation

- `docs/SECURITY.md` — threat model and control catalogue
- `docs/E2E_TEST_REPORT.md` — Step 10 results on 10.0.3.13

### Known limitations

- One-session-per-user enforcement is described in `docs/SECURITY.md`
  but not enforced yet in `ws_handler`.
- Idle-timeout E2E is verified by code review + unit tests; no live
  5-minute gate test ran.
- `install.sh` uses `npm install` rather than `npm ci` (addon dep merge
  mutates the panel's lockfile); long term the addon JS should ship as
  its own npm package.
