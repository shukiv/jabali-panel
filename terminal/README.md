# Jabali Terminal

Browser-based root shell for [Jabali Panel](https://github.com/shukiv/jabali-panel),
powered by [xterm.js](https://xtermjs.org/).

> **Security notice**: this addon hands a **root** PTY to an authenticated
> admin through the browser. Every session requires fresh re-auth + 2FA, is
> IP-bound, expires in ≤60s token TTL + 1hr hard cap, and is fully audited.
> See [docs/SECURITY.md](docs/SECURITY.md) before enabling on production.

## Install (via Jabali Panel)

Server Settings → Addons → **Terminal** → Install.

## Install (manual)

```bash
curl -fsSL https://raw.githubusercontent.com/shukiv/jabali-terminal/main/install.sh | sudo bash
```

## Uninstall

```bash
curl -fsSL https://raw.githubusercontent.com/shukiv/jabali-terminal/main/install.sh | sudo bash -s -- --uninstall
```

## Architecture

- **Daemon** (`daemon/`, Python 3.12+, asyncio) — runs as root, listens on
  `/run/jabali-terminal/jabali-terminal.sock` (`root:www-data` 0660). Routes:
  - `GET  /health`
  - `POST /api/v1/session` — HMAC-signed mint request, returns a 60s
    single-use token (SEC-REV-5 strict-order verification, SEC-REV-1
    SQLite nonce store).
  - `GET  /api/v1/sessions` + `/api/v1/sessions/{name}/transcript` —
    read-only audit index for the Sessions page.
  - `GET  /terminal/ws` — Caddy proxies `wss://host:8443/terminal-ws` here
    via a marker-guarded block injected into `/etc/jabali/Caddyfile`.
    Handshake-challenge auth (SEC-REV-2): challenge → HMAC response →
    token → PTY attach. No token ever in URL.
- **Panel plugin** (`panel/`) — Filament plugin autodiscovered by
  `class_exists()` in `AdminPanelProvider`. Single page `Pages\Terminal`
  with two Alpine-switched tabs: **Terminal** (xterm.js view + re-auth
  modal → live root PTY) and **Sessions** (read-only browser over the
  last 100 HMAC-sealed transcripts). Dedicated `POST
  /jabali-admin/terminal/session` route does the re-auth; rate limited
  at 3/min per (admin, ip) and locked out after 5 failed attempts for
  15 minutes.

## Configuration

All runtime config lives in `/etc/jabali-terminal/jabali-terminal.conf`
(`0640 root:www-data` — PHP-FPM needs to read `hmac_secret`). Full example
in [`configs/jabali-terminal.conf.example`](configs/jabali-terminal.conf.example).

Commonly tweaked knobs:

| Key                        | Default                                           | Purpose                                                                 |
|----------------------------|---------------------------------------------------|-------------------------------------------------------------------------|
| `session_idle_seconds`     | `300`                                             | Idle gate — fires when both stdin AND stdout have been quiet this long. |
| `session_hard_seconds`     | `3600`                                            | Hard cap — force-close regardless of activity.                          |
| `max_concurrent_sessions`  | `4`                                               | Daemon-wide concurrency ceiling (not per-admin).                        |
| `allowed_ips`              | empty (local-only)                                | Comma-separated allow-list for the panel client that POSTs to the mint. |
| `shell`                    | `/bin/bash`                                       | Shell exec'd under the PTY (must be interactive login-capable).         |

After editing the conf, `systemctl restart jabali-terminal` to pick up
daemon-side changes.

## Status

v0.1.0 tagged 2026-04-12. Steps 1–10 of `~/projects/jabali/plans/jabali-terminal-addon.md`
complete, end-to-end E2E pass documented in
[`docs/E2E_TEST_REPORT.md`](docs/E2E_TEST_REPORT.md).

Post-0.1.0 work tracked in [`CHANGELOG.md`](CHANGELOG.md) under
`[Unreleased]` — notably the Caddy migration, `@xterm/*` package
migration + canvas renderer fix, Filament-native UI, and the
Sessions-page → tab-inside-Terminal consolidation.
