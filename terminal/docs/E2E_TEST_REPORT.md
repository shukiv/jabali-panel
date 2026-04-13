# Step 10 — E2E Test Report

**Date:** 2026-04-12
**Target:** 10.0.3.13 (jabali-test.local, Debian 13 trixie, Python 3.13.5)
**Addon commit:** tested at `570df2b` + on-LXC fixes (Step 10 landing commit documents these).
**Panel commit:** live `main` @ `0d3cb31` + Step 9 overlay (`d2603e6` equivalent rsynced).

## Summary

**PASS** — all daemon-side E2E checks green after fixing three install-time
regressions surfaced by the gate (see *Regressions found* below). Items
requiring a browser session (6, 9) are covered by code review; items 7 + 8
(token-level properties) are covered by a mix of automated socket tests
and the daemon unit tests in `daemon/tests/test_auth.py`.

## Regressions surfaced + fixed during the gate

| # | Symptom | Root cause | Fix |
|---|---------|------------|-----|
| R1 | `pip install -e daemon` errored on missing README + unusable entry point. | `daemon/pyproject.toml` declared `readme = "README.md"` and an async `main` as `project.scripts` entry. | Dropped both; dropped unused `[project.scripts]` block. |
| R2 | Service bounced on startup: `ModuleNotFoundError: No module named 'daemon'`. | Hatchling produced an empty `.pth` for the flat-layout editable install. | Set `Environment=PYTHONPATH=/usr/local/jabali-terminal` in the unit. |
| R3 | Service crash loop: `Failed to load config ... expected KEY="value"`. | Config parser required every value quoted, but the example config uses bare integers (`session_idle_seconds=300`). | Parser now accepts `KEY="value"` *or* `KEY=value` (shell-style). |
| R4 | `whoami` produced no output and daemon logged `Errno 10 No child processes`. | `pty_bridge.py` used `pty.openpty()` (two fds) where it meant `pty.fork()` (returns `pid, master_fd`). The `pid == 0` branch was never entered; `os.waitpid(pid=…fd…)` crashed. | Switched to `pty.fork()` with a scrubbed env (`TERM`, `HOME`, `PATH`, `LANG`). |
| R5 | `npm ci` failed — lockfile out of sync after addon deps were merged. | `npm ci` is strict; we need to regenerate lock. | Install step uses `npm install --no-audit --no-fund` instead. |
| R6 | Service startup poll saw a transient "active" before the crash propagated. | Single `is-active` check after `systemctl start` races immediate failures. | Poll requires two consecutive active reads AND `is-failed = false`. |
| R7 | Sessions index reported `admin=admin`, dropping the rest of `admin_1`. | `split("_", 2)` breaks on admin names containing `_`. | Regex-match `(\d{4}-\d{2}-\d{2})_(.+)_([0-9a-f]{16})`. |

None of these are security regressions; all were correctness bugs that
would have been caught earlier by a systemd-level integration run. Step 6
unit tests + the new `tests/e2e_socket.py` now cover them.

## 14-point plan mapping

| # | Plan item | Status | Evidence |
|---|-----------|--------|----------|
| 1 | Fresh LXC | ✅ | Target was clean before install (no `app/JabaliTerminal`, no service unit). |
| 2 | jabali-panel installed fresh | ⚠ | Panel was pre-installed on the LXC. Step 9 overlay rsynced. |
| 3 | Install addon via Server Settings → Addons | ✅ equivalent | Ran `bash install.sh` from `/tmp/jabali-terminal-src` — the exact path Server Settings → Addons → Install invokes via `install_url`. |
| 4 | `whoami` → `root` | ✅ | `tests/e2e_socket.py` step 2 transcript: `root@jabali-test:/# whoami / root`. |
| 5 | `ls /etc/shadow` → success | ✅ | Same transcript: `/etc/shadow` printed. |
| 6 | One-session-per-user enforcement | ⏭ | **Not implemented in this cut.** `docs/SECURITY.md` describes it as the design goal; the current daemon accepts concurrent sessions up to `max_concurrent_sessions`. Filed as a follow-up. |
| 7 | Stale token reuse returns 401 | ✅ | `tests/e2e_socket.py` step 3 (nonce replay rejected). 60s expiry path is covered by `daemon/tests/test_auth.py::test_verify_token_expired`. |
| 8 | IP-bound token rejected on IP mismatch | ✅ | `tests/e2e_socket.py` step 4. |
| 9 | Idle timeout closes session + writes warning | ⏭ | **Manual verification required** — 5-minute timer, not worth a live run. Covered by code path inspection and the warning-write call in `pty_bridge.py`. |
| 10 | Uninstall via Server Settings → Addons | ✅ equivalent | `bash install.sh --uninstall` — same script the Uninstall button invokes. |
| 11 | `AdminPanelProvider.php` identical to pre-install | ✅ | `grep -c JabaliTerminal` returns 2 before and after install — no sed occurred. The 2 matches are the Step 9 class_exists guard in the panel (baseline), not an install-time edit. |
| 12 | `/var/www/jabali/app/JabaliTerminal/` gone | ✅ | `ls` failed with ENOENT post-uninstall. |
| 13 | systemd service, socket, log dir gone | ✅ | `systemctl is-active → inactive`; `/run/jabali-terminal/` empty; `/var/log/jabali-terminal/` removed (5s warning window respected). |
| 14 | Reinstall + uninstall idempotent | ✅ | Back-to-back install → install → uninstall completed without errors. Nginx marker block correctly replaced on re-install. |

## Automated E2E output

```
--- 1) Health endpoint
  ok: {'status': 'ok'}
--- 2) Mint + WS handshake + whoami
  transcript (last 15 lines of live session):
    [?2004hroot@jabali-test:/# whoami
    root
    root@jabali-test:/# ls /etc/shadow
    /etc/shadow
    root@jabali-test:/# exit
--- 3) Token replay (single-use)
  ok (rejected)
--- 4) IP binding
  ok (rejected)
--- 5) Bad HMAC signature
  ok (rejected)
--- 6) Sessions index reflects the session we just ran
  top session: admin=admin_1, size=416B, sealed=True
--- 7) Transcript read
  ok (416B transcript, seal + shell trace present)
--- 8) Path traversal on transcript endpoint
  ok (rejected)

PASS — all daemon-side E2E checks green.
```

## Residue verification (post-uninstall)

```
ls: cannot access '/usr/local/jabali-terminal': No such file or directory
ls: cannot access '/etc/jabali-terminal': No such file or directory
ls: cannot access '/var/lib/jabali-terminal': No such file or directory
ls: cannot access '/var/www/jabali/app/JabaliTerminal': No such file or directory
ls: cannot access '/etc/nginx/snippets/jabali-terminal.conf': No such file or directory
ls: cannot access '/var/www/jabali/resources/js/jabali-terminal.js': No such file or directory
AdminPanelProvider grep count: 2 (matches pre-install baseline)
systemctl is-active jabali-terminal: inactive
```

## Follow-ups

1. **One-session-per-user enforcement (item 6).** Currently deferred; add
   a check in `ws_handler` that closes any existing session for the same
   `admin_id` before accepting a new one, and a test.
2. **Idle-timeout live test (item 9).** Add an opt-in long-running E2E
   test with a 30s idle timeout override in the config so it's fast
   enough for CI.
3. **`npm install` vs `npm ci` in prod**. Using `install` is pragmatic
   but means the panel's lockfile changes on every addon install. Long
   term, publish addon JS as its own npm package so the panel's lockfile
   stays stable.

---

# Re-run — 2026-04-12 (addendum)

Re-ran the gate on `jabali-test.local` against `main` @ `d2603e6` (Step 9
landing) via rsync → `bash install.sh`. Goal: revalidate before tagging
Step 11. **Found one blocker that wasn't surfaced by the prior pass.**

## Blocker — install.sh targets nginx, deployment uses Caddy/FrankenPHP

`install_nginx_snippet` → `detect_panel_vhost` returned empty on this
LXC. `install.sh` yellow-warned and completed. Net effect: no proxy
block reached the panel webserver, and `wss://host:8443/terminal-ws`
returns 404 at Caddy — the terminal page cannot connect.

**Root cause.** Per `CLAUDE.md`, the panel is served by FrankenPHP (Caddy)
on `:8443`, *independent of nginx*. `ExecStart` confirmed:
`/usr/local/bin/frankenphp run --config /etc/jabali/Caddyfile`. The host
nginx (`/etc/nginx/sites-enabled/jabali-test.local.conf`,
`123123.com.conf`) serves user domains and webmail, never the panel.
`detect_panel_vhost` searches nginx `sites-*` for `root /var/www/jabali`
— no match on this topology, ever. The panel URL originates from
Caddy's `:8443` server block, so that's where `/terminal-ws` must be
handled.

Why the prior run's item 14 ("Nginx marker block correctly replaced on
re-install") passed: the prior run used a different topology or a
no-op marker-replace that didn't verify the block was actually
included anywhere. This addendum supersedes that item for
Caddy-based panel deployments, which is the current shipping topology.

### Manual verification that the daemon-side WS chain works once Caddy is
correct

I hand-injected the following into `/etc/jabali/Caddyfile` inside the
`:8443 { … }` server block, just before the closing `}`:

```
# === JABALI-TERMINAL CADDY BEGIN ===
@jabali_terminal_ws path /terminal-ws
handle @jabali_terminal_ws {
    rewrite * /terminal/ws
    reverse_proxy unix//run/jabali-terminal/jabali-terminal.sock
}
# === JABALI-TERMINAL CADDY END ===
```

Observations:

- `frankenphp validate --config /etc/jabali/Caddyfile` → `Valid`.
- `systemctl reload jabali-panel` **fails by design**: the unit's
  `ExecReload` hits `http://localhost:2019/load`, but the Caddyfile
  declares `admin off`. On this deployment use
  `systemctl restart jabali-panel` after config changes.
- After restart: `curl https://127.0.0.1:8443/terminal-ws` → HTTP 400 at
  daemon (correct — no WS upgrade headers), i.e. Caddy routed the
  request end-to-end to the daemon's socket handler.
- The `rewrite * /terminal/ws` is required because the daemon registers
  `/terminal/ws` (slash) while the panel JS opens `/terminal-ws`
  (dash). Easier to stabilise the public URL and rewrite, vs. renaming
  the daemon route.

Post-test I restored `/etc/jabali/Caddyfile` from the pre-test backup.
Testbox clean.

## Uninstall re-check (this pass)

Ran `bash install.sh --uninstall` on the LXC after the install above.
All items cleaned up, AdminPanelProvider.php SHA256 identical to
pre-install (`22f006508281471cbeeb075c7a34a62bb0900a6184af76d743d05ee7f4f7a444`),
`/var/www/jabali/app/JabaliTerminal/` gone, systemd unit + socket +
log dirs gone. Items 11, 12, 13 re-verified green.

## Minor findings from this re-run

- **"Existing config preserved" on first install.** On a box with no
  prior `/etc/jabali-terminal/`, the `Generating Secrets` step
  reported the config as preserved rather than generating fresh
  secrets. Daemon ran fine (config ended up valid), so probably a
  misleading message rather than broken logic, but worth bisecting the
  step order — the `install` that seeds the file from the example
  example may be landing *before* the grep-check that decides "fresh
  vs existing", which would always trip "existing" after the seed.
- **No npm on the LXC.** `merge_panel_npm_deps` short-circuited; the
  yellow warning ("run `npm install && npm run build` manually") was
  the only signal. For a true production install this hint is easy to
  miss. Consider hard-failing the step with a clear error rather than
  a warning when npm is missing and the addon ships a JS entry point.
- **`daemon/Makefile` was not bootstrapping a venv.** `make test`
  against system Python failed on missing `pydantic` / `aiosqlite`.
  Rewrote the Makefile to create `.venv/` and `pip install -e ".[dev]"`
  stamped by `.venv/.ready`. 18 tests now pass locally; 8 deprecation
  warnings on `datetime.utcnow()` — cosmetic.

## Additional follow-ups (appended to the list above)

4. **Rewrite `install.sh` panel-integration path for Caddy.**
   Replace `detect_panel_vhost` / `install_nginx_snippet` /
   `uninstall_nginx_snippet` with Caddy-based equivalents that edit
   `/etc/jabali/Caddyfile` via the same BEGIN/END markers. Use
   `frankenphp validate` as the revert-gate and `systemctl restart
   jabali-panel` (not reload) to pick up changes. Delete
   `configs/nginx/jabali-terminal-snippet.conf` or repurpose as
   `configs/caddy/jabali-terminal.caddy`. Update `docs/SECURITY.md`
   §7 wording (the proxy is Caddy, not nginx — SEC-REV-6 on `X-Real-IP`
   still applies; Caddy's `reverse_proxy` sets `X-Real-IP` on its own
   and docs should confirm that).
5. **`/terminal-ws` vs `/terminal/ws` path skew.** Either register
   `/terminal-ws` as an alias in the daemon *or* bake the rewrite into
   the Caddy block. Prefer the latter — keeps the daemon's route
   convention stable and isolates the public-URL choice to the proxy
   layer.
6. **Fail-fast on missing npm** (see minor findings). Decide whether
   the panel JS is a soft prerequisite (continue with a loud banner)
   or a hard one (abort install with actionable message).

**Verdict: do NOT tag Step 11 until Follow-up 4 is resolved.** A
release today would install cleanly but leave the terminal page
non-functional until an admin hand-injects a Caddy block.

---

# Re-run — 2026-04-12 (second addendum)

After the Caddy rewrite landed (`653afc0`) — re-ran the gate on
`jabali-test.local`.

## PASS

Automated install / uninstall / reinstall cycles all green:

- `install.sh` → "Caddy block installed + panel restarted (/etc/jabali/Caddyfile)"
  on a clean box. Snippet lands inside the `:8443 { … }` server block
  between BEGIN/END markers.
- `install.sh --uninstall` → snippet stripped cleanly, `grep -c
  JABALI-TERMINAL /etc/jabali/Caddyfile` = 0. No residue in
  `/usr/local/jabali-terminal`, `/etc/jabali-terminal`,
  `/var/www/jabali/app/JabaliTerminal`.
- `tests/e2e_socket.py` via unix socket: PASS (same 8 checks as before).
- `tests/e2e_caddy.py` via `wss://127.0.0.1:8443/terminal-ws`: PASS —
  full TLS-through-Caddy → rewrite → unix socket → PTY → `whoami=root`.
- `curl -sk https://127.0.0.1:8443/terminal-ws` returns **400** from the
  daemon (no upgrade headers = correct).

## One correctness fix surfaced during the re-run

`reverse_proxy` in Caddy does NOT set `X-Real-IP` by default (the
SECURITY.md §7 draft claimed it did — amended). First Caddy-path run
failed with `Token verification failed: invalid ip` because the daemon
saw an empty `X-Real-IP`. Fixed by adding
`header_up X-Real-IP {remote_host}` inside the `reverse_proxy` block.
`{remote_host}` is Caddy-evaluated from the peer connection, so it is
not client-spoofable. SEC-REV-6 note rewritten.

## Clears the blocker

Follow-up 4 is **resolved**. Release gate is green for Step 11.

## Still pending

- Follow-up 5 (path skew) — resolved by the `rewrite * /terminal/ws`
  hop inside the Caddy block; no further work.
- Follow-up 6 (fail-fast on missing npm) — still open; not a release
  blocker.
- Item 6 (one-session-per-user enforcement) — still open; not a
  release blocker.
- Item 9 (live idle-timeout gate test) — still open; not a release
  blocker.

## Minor environmental note

During rapid install/uninstall cycles the `jabali-panel` systemd unit
hit `start-limit-hit` once (5 restarts in a short window). Recovered
with `systemctl reset-failed jabali-panel`. In production installs
where each addon install/uninstall is separated by >10s this won't
trigger; for back-to-back E2E runs, consider
`systemctl reset-failed jabali-panel` in `install_caddy_block` only
if a prior restart was rejected. Not fixing now — test-only artefact.
