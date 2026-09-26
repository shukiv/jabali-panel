# Secret rotation runbook (JAB-357 crit-7)

## When to run this

Run this after the JAB-351/357 isolation fix (`#1208`/`#1217`/`#1222`/`#1246`) has been deployed to a host, to invalidate secrets that the internet-facing webmail service could read **before** the fix. The isolation stops future reads; rotation invalidates what an attacker may already have. Both are required to fully close the critical.

This is an **operator ceremony**, run as **root**, on **one host at a time**:

```
sudo jabali secrets rotate <name> --dry-run   # always preview first
sudo jabali secrets rotate <name>
```

It is deliberately **not** automated. It is never wired into `install.sh`'s converger or the 04:30 fleet auto-update — auto-rotating fleet secrets unprompted is the outage this ticket exists to prevent. Every rotation restarts the affected service; do it in a maintenance window.

Every command:
- previews with `--dry-run` and touches nothing;
- applies the privileged change, then rewrites the on-disk secret **atomically** (temp + rename), preserving the file's exact owner and mode;
- keeps a **root-only `.rotate.bak`** snapshot, restores from it if a post-rotation health probe fails, and **purges it on success** (a lingering `.bak` is a fresh copy of the old credential);
- writes an audit event (`secrets.rotate.*`) that records the secret name, never its value.

## Scope — what webmail could read (and what it could not)

JAB-351 proved `jabali-webmail` (broad `jabali` group) could read `0640 root:jabali` secrets. Scope is **exactly that exposure class**, not "every secret on the box".

| Secret | Tooling | Notes |
|---|---|---|
| Panel DB app-user password (`db-password` + `DATABASE_URL`) | `jabali secrets rotate db-app-user` ✅ | `jabali_panel_app@localhost` (the panel's own user, not root). ALTER USER → rewrite `db-password` + the `DATABASE_URL` line in `panel.env` → restart → verify the new credential. |
| Kratos DB password + `secrets.cookie` / `secrets.default` (inlined in `kratos.yml`, `root:jabali 0640`) | `jabali secrets rotate kratos` ✅ | Replaces all three with no rollover (old values are exposed) and revokes every session — **signs every user out once**. See "Kratos — IN scope" below. |
| `JWT_SECRET` (`panel.env`) | `jabali secrets rotate jwt` ✅ | Vestigial post-M20 (panel auth is Kratos; webmail SSO uses the separate `bulwark-jwt-auth.secret`). Rotating it is a safe near-noop; included only because it lived in the exposed file. |
| `postgres.password` | `jabali db root-password --engine postgres` (existing) | Same exposure class (`root:jabali 0640`). Only if the Postgres engine is enabled on the host. |
| MariaDB root password | `jabali db root-password --engine mariadb` (existing) | Break-glass root credential; socket auth preserved (ADR-0097). |
| Panel TLS key (`/etc/jabali/tls/panel.key`) | `jabali panel-cert …` reissue (existing) | Regenerate/reissue the panel cert; verify `/login` 200 after. |
| Panel-mail TLS key (`/etc/jabali/tls/panel-mail.key`) | `jabali mail-cert …` reissue (existing) | Reconciler-managed (ADR-0105); reissue and let the mail-cert cascade settle. |
| `JABALI_REDIS_PANEL_TOKEN` (`panel.env` + redis aclfile) | `jabali secrets rotate redis-panel-token` ✅ | Rotates **live**: `ACL SETUSER jabali_panel resetpass >…` on the running Redis (authing as jabali_panel with the old token), then rewrites `panel.env` + the aclfile `user jabali_panel` line (default + tenant `wp_*` lines preserved), then restarts panel and verifies. **Never `ACL LOAD` / restart Redis** — that wipes the runtime `wp_<osuser>` tenant ACL users until the reconciler recreates them. Rolls back the live password too on failure. |
| `PDNS_DB_PASSWORD` (`pdns.env` + gmysql conf) | `jabali secrets rotate pdns` ✅ | ALTER the `jabali_pdns` DB user, rewrite `PDNS_DB_PASSWORD` in `pdns.env` **and** `gmysql-password` in `/etc/powerdns/pdns.d/01-jabali-mysql.conf`, restart pdns, verify + rollback. Cleanly no-ops with a "not provisioned" message if PowerDNS is absent. |
| `migration-secrets/<job>.env` | **purge, don't rotate** | Transient Plesk/cPanel/SSH **source-host** credentials. Any file lingering from before the fix exposed a third party's password — delete lingering files; the operator re-enters source credentials on the next migration. Not our secret to rotate. |
| `JABALI_WP_CACHE_HMAC_SECRET` (`panel.env`) | **deferred** (documented risk) | See below. |

### Deferred: WP-cache HMAC secret

`JABALI_WP_CACHE_HMAC_SECRET` derives a per-tenant Redis-cache ACL token that is baked into **every** WordPress tenant's `wp-config.php`. Rotating it therefore is **not** a one-file swap: it requires re-deriving and rewriting every tenant's `wp-config.php` via the agent — a fleet-wide, per-tenant fan-out that must be throttled (the fleet's 2 GB VMs can fall into a kswapd death-spiral under unbounded fan-out). It is deferred to a dedicated, throttled reconciler task rather than bolted onto this tool. Until then, treat the value as exposed: it grants access only to the shared tenant object cache, not to panel/DB/TLS material.

### Out of scope

DKIM keys (`root:jabali-sftp 0600`), tenant DB passwords, tenant Let's Encrypt lineage, and the Stalwart admin token (`jabali-mail`).

### Kratos — IN scope (correction, 2026-09-26)

An earlier version of this runbook put Kratos out of scope because the root-only `kratos-secrets/` files are not `jabali`-readable. **That was wrong.** `/etc/jabali-panel/kratos.yml` is `root:jabali 0640` on every install — Kratos runs as `jabali` and must read it — and it inlines three secrets that webmail could therefore read before the fix:

| Secret | What an attacker could do with the old value |
|---|---|
| Kratos DB password (`dsn`) | Read the Kratos database: identities, password hashes, TOTP secrets and **live session tokens**. |
| `secrets.cookie` / `secrets.default` | Mint valid CSRF tokens and open captured Kratos cookies. |

`jabali secrets rotate kratos` replaces all three:

- It runs `ALTER USER jabali_kratos` and rewrites four files atomically: `kratos-db-password`, `kratos-secrets/{default,cookie}` and the dsn plus secrets in `kratos.yml`. The next `jabali update` re-renders from those files, so it keeps the new values.
- It restarts `jabali-kratos` and verifies both the service and the new DB password. On failure it rolls back.
- It then **revokes every active session**.

**Old values are deliberately not kept.** Kratos's usual rollover (prepend the new secret, keep the old one for verification) would leave the exposed secret valid.

- The cost: **every panel user is signed out once** and must sign in again. Run it in a maintenance window, and expect your own session to end.
- Nothing at rest depends on these secrets, since `kratos.yml` has no `ciphers:` block, so nothing needs re-encrypting.
- If the command reports that session revocation failed, the secrets **stay rotated**; rolling back would bring the exposed values back. Revoke the rest with `jabali session list` / `jabali session revoke-user`.
- If it reports that `kratos.yml` does not match its source files, run `jabali update` to re-render it, then retry.

**Still open after rotating Kratos.** The old DB password could have been used to copy password hashes and TOTP secrets. Rotating the password stops further reads but does not invalidate data already copied. On hosts where compromise is plausible, have administrators reset their password and re-enroll TOTP. This is an operator decision; the tool does not force it.

## Recommended order (per host)

`jabali secrets rotate all` runs the built rotations (`db-app-user` → `redis-panel-token` → `jwt`) in this lockout-safe order, each with its own verify+rollback, stopping at the first failure (already-rotated secrets stay rotated; re-run to continue). The remaining items below are run individually.

1. `db-app-user` — highest value; verify `/login` still works after.
2. `postgres` / MariaDB root — if those engines are present.
3. `redis-panel-token` — once tooled.
4. `pdns` — if PowerDNS present.
5. Panel + panel-mail TLS reissue.
6. `jwt` — cheap, do it alongside any panel restart.
7. `kratos` — last, in a maintenance window: it signs every user out. Deliberately NOT part of `rotate all`, so the forced sign-out is always an explicit choice.
8. Purge lingering `migration-secrets/*.env` (the daily reaper now also removes orphans with no job row).
9. Note the deferred WP-cache HMAC as an open item.

## Verify after each rotation

- Panel: `systemctl is-active jabali-panel` and `curl -sk https://<host>/login -o /dev/null -w '%{http_code}'` → 200. **Allow a few seconds after the restart:** the panel is marked `active` within ~1s but takes a moment to bind its socket, so an immediate curl can return a transient **502** that self-clears (observed on the .60 drill). The rotate command itself waits for a stable-active panel before reporting success, so trust its exit status; if you poll manually, retry for ~10s before treating a 502 as a failure.
- Dispatcher/Redis: the panel's Redis-backed queues still process (no ACL errors in `journalctl -u jabali-panel`).
- Mail (after TLS): send a test message.

## Rollback

Each command rolls itself back automatically if its post-rotation probe fails (restores the file from `.rotate.bak` and reverts the privileged change). If you need to roll back manually after the fact, the `.rotate.bak` is gone on success by design — recover from your host backup.

## Closing the ticket

JAB-357 crit-7 closes when the operator has run the rotation across the affected hosts **and** the external auditor (`codex-security-test`, who filed JAB-351/357) has re-confirmed the exposed secrets are invalidated — not when this tooling merges.
