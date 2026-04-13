# jabali-terminal — Security Model

This addon hands a **root PTY** to an authenticated panel admin through a
browser. That blast radius is the entire host. The controls in this document
are the full justification for why it is safe to ship.

All `**[SEC-REV-N]**` tags map to findings from the 2026-04-12 adversarial
review of the construction plan at
`~/projects/jabali/plans/jabali-terminal-addon.md`.

---

## 1. Threat model

### 1.1 Assets

- **Root shell**: the PTY spawned by the daemon — if reachable by anyone who
  is not an authorised admin, the host is compromised.
- **Audit transcripts** at `/var/log/jabali-terminal/sessions/` — forensics
  evidence after an incident. Must be tamper-evident.
- **HMAC secrets** in `/etc/jabali-terminal/jabali-terminal.conf` — token
  forgery is game over.

### 1.2 Attackers we defend against

| # | Attacker                                  | Entry point                                        | Primary defences                                                    |
|---|-------------------------------------------|----------------------------------------------------|---------------------------------------------------------------------|
| A | **Stolen admin session cookie**           | Panel session hijack                               | Fresh password re-prompt per terminal open (+ 2FA when the admin has Fortify 2FA enrolled); IP-bound token |
| B | **XSS on the panel**                      | Injected JS in the admin browser                   | Token never leaves memory / never in URL (SEC-REV-2); CSP; framebusting |
| C | **Compromised admin account**             | Attacker knows password + 2FA seed                 | Out-of-scope: admin=root here; the only damper is audit trail       |
| D | **Insider abuse** (legitimate admin)      | Direct login                                       | HMAC-sealed audit log under `/var/log/jabali-terminal/sessions/` (browser view optional, see §4); idle+hard TTL |
| E | **Network attacker on the same LAN**      | Sees WS traffic                                    | TLS termination at Caddy (:8443); daemon listens on unix socket only |
| F | **Root attacker on the host**             | Already has root through some other path           | Out-of-scope: no defence is meaningful; but note the audit `.sig` file makes post-facto tampering detectable |
| G | **Replayed token URL** (shared over chat) | Admin shares a URL they think is a bookmark        | Token TTL ≤60s; single-use nonce; IP-bound; handshake-challenge (token not in URL) |
| H | **CSRF paste attack** (malicious page)    | Tricks admin into pasting a command                | Clipboard-paste hook rejects pastes >4KB; no automatic exec of paste |
| I | **Clickjacked re-auth modal**             | Adversary frames the terminal page                 | `X-Frame-Options: DENY` + CSP `frame-ancestors 'none'` (SEC-REV-8)  |

### 1.3 Non-goals

- **Not** providing per-user (non-admin) terminals.
- **Not** providing file upload through the terminal.
- **Not** providing session replay/recording playback — only text logs.
- **Not** defending against an attacker who already has root on the host
  (they can turn off the daemon, wipe logs, etc.).

---

## 2. Auth flow

```
Browser (admin, already logged into Filament)
  │
  │ 1. GET /jabali-admin/terminal -> page loads with re-auth modal
  │    (X-Frame-Options: DENY, CSP frame-ancestors 'none')   [SEC-REV-8]
  v
Filament page (Livewire): modal asks for password + 2FA code
  │
  │ 2. POST /jabali-admin/terminal/session
  │    body: { password, two_factor_code }
  │    rate-limited: 3/min per (admin_id, ip); 5 bad codes -> 15min lockout
  v
Laravel route (auth:admin) -> JabaliTerminalClient
  │
  │ 3. POST over unix socket -> daemon /api/v1/session
  │    body: HMAC-signed { admin_id, ip, issued_at, nonce }
  v
Daemon /api/v1/session handler (aiohttp)
  │
  │ 4. Mint token: base64( { admin_id, ip, issued_at, expires_at = now+60s,
  │                          nonce_32B, hmac_sha256(body, token_secret) } )
  │    Return { ws_url, token, expires_at } (token in body; panel holds in memory)
  v
Browser: open WSS to wss://<host>/terminal-ws   (no token in URL)  [SEC-REV-2]
  │
  │ 5. On connect, daemon sends { type:"challenge", nonce: <32B hex> }
  │    Browser responds within 5s with:
  │      { type:"auth", token: <token>, nonce_response:
  │                                     HMAC_SHA256(token, challenge_nonce) }
  │    Browser wipes `token` from memory immediately after sending.
  v
Daemon verifies in strict order (SEC-REV-5):
  │    (1) parse / base64 decode
  │    (2) expires_at > now
  │    (3) nonce not previously consumed  <- persistent SQLite (SEC-REV-1)
  │    (4) ip matches X-Real-IP          <- set by Caddy reverse_proxy (SEC-REV-6)
  │    (5) HMAC valid                    <- last so timing leaks less info
  │
  │    Any failure -> close WS with 1008 and emit single `401 invalid token`
  │    to the client; log the specific reason server-side for ops.
  v
Daemon records nonce as consumed (fsync before responding).
  │
  │ 6. PTY spawned as root; proxy loop begins.
  v
Session ends when: explicit close, hard-cap (1hr), idle-cap (5min stdin AND
stdout quiet), daemon restart, or PTY exits. Audit log sealed with HMAC.
```

### 2.1 Threat-model answers

These are the three questions the adversarial reviewer must be able to
answer "no" to after reading this document.

**Q1. Can I steal a token (from the network, a log, or a URL bar) and reuse it?**

No.
- The token is never in a URL (SEC-REV-2 — handshake-challenge).
- The token is never written to `localStorage` / `sessionStorage` / cookies.
- The token has TTL ≤ 60s (short enough that most exfiltration paths are too slow).
- The token is single-use — `nonce` recorded in SQLite before the WS is
  accepted. A replay fails at step 5(3).
- The token is IP-bound — even if the nonce fired before the legitimate user,
  the attacker's IP won't match at step 5(4).

**Q2. Can XSS on the panel open a terminal?**

No.
- XSS in an admin browser can trigger `fetch('/jabali-admin/terminal/session')`
  but that endpoint requires `password` + `two_factor_code` in the body. The
  attacker does not have the admin's password, and cannot obtain a live 2FA
  code.
- Even if the attacker races the admin's own re-auth and gets a token, the
  handshake-challenge flow requires the token to be produced for use — XSS
  that simply siphons the token from memory still loses because the token is
  wiped immediately after the challenge response is sent.
- CSP `frame-ancestors 'none'` prevents a phishing page from embedding the
  terminal and capturing the modal.

**Q3. Can a compromised admin session cookie open a terminal without re-auth?**

No.
- `POST /jabali-admin/terminal/session` requires `auth:admin` (cookie) **plus**
  `password`. Cookie alone is insufficient.
- If the admin has Fortify 2FA enrolled, a current `two_factor_code`
  is **also** required and verified server-side; a client that omits
  the code cannot bypass it by hiding the UI field.
- Admins who have not enrolled in 2FA get in on the password re-prompt
  alone. This is a deliberate operator choice: 2FA is conditional on
  the admin's own account setting, not mandatory addon-wide.

---

## 3. Token design

| Field          | Size    | Notes                                                       |
|----------------|---------|-------------------------------------------------------------|
| `admin_id`     | 8B      | Binds the token to a specific admin user                    |
| `ip`           | 16B     | IPv4 or IPv6 (normalised); binds the token to the caller IP |
| `issued_at`    | 8B      | Unix seconds                                                |
| `expires_at`   | 8B      | Unix seconds, <= `issued_at + 60`                           |
| `nonce`        | 32B     | CSPRNG; used as single-use key in SQLite nonces table       |
| `hmac`         | 32B     | HMAC-SHA256 over all of the above, keyed by `hmac_secret`   |

Total raw: 104B. Base64 URL-safe ~140 chars. Transported in HTTP response
body; **never** in URLs; held in JS memory only until the handshake-response
is sent, then overwritten.

---

## 4. Audit log

- Path: `/var/log/jabali-terminal/sessions/<iso-date>_<admin>_<session>.log`
- Perms: `0600 root:root`
- Contents: line-buffered transcript:
  - Opening line: `# Session start: <iso-ts>, admin=<user>, ip=<ip>, session=<id>`
  - Framed interleaved stdin/stdout as written/read (with 1s or 4KB flush window)
  - Any warning events, e.g. `# WARNING: idle timeout at <iso-ts>` (SEC-REV-3)
    or `# WARNING: hard timeout at <iso-ts>` (SEC-REV-9)
  - Closing line: `# Session end: <iso-ts>, exit_code=<N>`
- On session close, daemon computes
  `HMAC-SHA256(log_content || metadata, audit_hmac_secret)` and writes
  `<session>.log.sig` next to the log (SEC-REV-4). The auditor re-verifies
  these signatures out-of-band (e.g. via a pulled copy on an admin station).
- On daemon restart, `server.py` scans the sessions dir for logs missing a
  closing line, appends `# Session interrupted: daemon restart at <iso-ts>`
  and signs them (SEC-REV-10). This prevents a crash-loop from letting a
  session linger in unsealed state.
- Logrotate (configured by `install.sh`): daily, 30 retained, compress,
  `su root root`, `create 0600 root root`.

Root on the host can still overwrite either the log or the `.sig`. The
`.sig` value lets offline verification detect that tampering happened — we
lose confidentiality but we don't lose detection.

### 4.1 Browser view

The panel ships a read-only transcript browser as the second tab on
`Pages\Terminal` (slug `/jabali-admin/terminal`). It lists the last 100
sessions and renders an individual transcript on demand — 1 MiB cap +
path whitelist enforced daemon-side, rendered through Blade's auto-
escaping `<pre>` so arbitrary bytes written by the daemon cannot inject
HTML.

The tab inherits the Terminal page's middleware by construction:
`auth:admin`, `PreventFramingMiddleware` (X-Frame-Options: DENY + CSP
frame-ancestors 'none', SEC-REV-8), and `canAccess()` (`isAdmin()`).
Tab switching is pure Alpine (no server round-trip), so `refreshSessions`
/ `viewTranscript` / `closeTranscript` are the only Livewire-callable
methods; each one re-runs `canAccess()` server-side as defense-in-depth
against any scenario where the `admin` guard check is looser than the
`isAdmin()` role check (e.g., a session whose role was revoked but
whose cookie is still valid).

The re-auth modal (password + 2FA) gates only the **Terminal** tab — it
is never required for browsing transcripts. This mirrors the pre-0.1.1
posture where the old separate Sessions page was cookie-only as well.
Operators who prefer to read transcripts off-disk (or rsync them
off-box) can ignore the tab entirely; the daemon still writes and
HMAC-seals every transcript regardless of panel activity.

---

## 5. Nonce store (SEC-REV-1)

- SQLite at `/var/lib/jabali-terminal/nonces.db`.
- `PRAGMA journal_mode=WAL; PRAGMA synchronous=FULL;` plus `fsync(2)` before
  the token endpoint returns to the client. This closes the "restart the
  daemon and replay tokens issued in the last 60s" window that a purely
  in-memory LRU would leave.
- Table: `CREATE TABLE nonces (nonce TEXT PRIMARY KEY, expires_at INTEGER NOT NULL)`.
- Purge on daemon start: `DELETE FROM nonces WHERE expires_at < strftime('%s','now') - 60`.
- A nonce must be INSERTed into the DB before the WS accept; any
  `UNIQUE constraint failed` response means "already used" -> 401.

---

## 6. Rate limiting + lockout

| Surface                                  | Rule                                       |
|------------------------------------------|--------------------------------------------|
| `POST /jabali-admin/terminal/session`    | 3 per minute per (admin_id, ip)            |
| 2FA code verification                    | 5 wrong codes -> 15min lockout (per admin) |
| Max concurrent sessions (daemon-wide)    | `max_concurrent_sessions` config (default 4) |
| Per-admin concurrent sessions            | 1 — new open closes the previous           |

All four live in the daemon config or Laravel config; none are hardcoded.

---

## 7. Network surface

- **Only** listener: unix socket at `/run/jabali-terminal/jabali-terminal.sock`,
  owner `root:www-data`, mode `0660`. No TCP port, no AF_INET binding.
- FrankenPHP / Caddy serves the panel on `:8443` (per `CLAUDE.md` on the
  parent panel). install.sh injects a marker-guarded block into
  `/etc/jabali/Caddyfile` that reverse-proxies `/terminal-ws` to the
  unix socket:
  ```
  @jabali_terminal_ws path /terminal-ws
  handle @jabali_terminal_ws {
      rewrite * /terminal/ws
      reverse_proxy unix//run/jabali-terminal/jabali-terminal.sock {
          header_up X-Real-IP {remote_host}
      }
  }
  ```
  The public URL is `/terminal-ws` (dash) to avoid colliding with
  Filament's own route tree; the daemon registers `/terminal/ws` (slash)
  because `/terminal` is a prefix for future endpoints. Caddy rewrites
  at the proxy layer so the daemon-side convention stays stable.
- SEC-REV-6: Caddy's `reverse_proxy` does NOT set `X-Real-IP` by default
  (it sets `X-Forwarded-For`). The snippet above explicitly writes
  `X-Real-IP {remote_host}`, which Caddy derives from the direct peer
  connection and is not a client-supplied header — so an attacker cannot
  spoof the IP-bind check by sending their own `X-Real-IP`. If the
  block is reordered so another handler sets `X-Real-IP` after this
  one, the daemon's IP-bind check could be subverted; re-verify.
- Host nginx on this topology serves **user domains and webmail only**.
  install.sh MUST NEVER touch `/etc/nginx/` — injecting there would land
  the terminal proxy inside a user vhost. The Caddy-only posture is
  enforced in `install_caddy_block()` (there is no nginx code path).
- CSP on the terminal page: `default-src 'self'; frame-ancestors 'none';
  connect-src 'self' wss: ws:; script-src 'self';` — bundles xterm.js locally
  through Vite; no CDN.
- Response headers on the terminal page: `X-Frame-Options: DENY` (SEC-REV-8).

---

## 8. Paste / injection defences

- Client listens for `paste` event and rejects pastes >4KB with a visible
  warning. Defends against "paste-this-one-liner" CSRF-style abuse.
- The WS payload treats all client bytes as shell stdin; no client frame is
  interpreted as code. Resize frames are a distinct JSON payload
  (`{type:"resize"}`) — the daemon never interprets WS input as code; only
  two message types are accepted and both are structured.

---

## 9. Rotation + secret management

- `hmac_secret` (token signing) and `audit_hmac_secret` (log sealing) are
  separate 32-byte values generated by `install.sh` via
  `openssl rand -hex 32`. Rotating one does not affect the other.
- Rotation procedure: edit `/etc/jabali-terminal/jabali-terminal.conf`,
  `systemctl restart jabali-terminal`. All in-flight tokens signed under the
  old secret become invalid immediately; sessions already open continue
  until their hard cap because their nonces are already consumed.
- Secrets are never committed. `install.sh` aborts if the config file
  already exists with a non-empty secret (refuses to overwrite).

---

## 10. Session UX — what the admin sees

1. Admin navigates to `Terminal` in the Filament side nav (top-level,
   positioned right after `Services`).
2. Re-auth modal blocks the page: "Enter your password and 2FA code to open
   a terminal."
3. On success, modal closes, xterm.js attaches, prompt appears.
4. Status bar shows: session id (short), IP, time remaining on hard cap, and
   an "End session" button.
5. On idle timeout / hard timeout / PTY exit, the terminal displays a
   banner and the page requires a new re-auth to continue.
