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
| `JWT_SECRET` (`panel.env`) | `jabali secrets rotate jwt` ✅ | Vestigial post-M20 (panel auth is Kratos; webmail SSO uses the separate `bulwark-jwt-auth.secret`). Rotating it is a safe near-noop; included only because it lived in the exposed file. |
| `postgres.password` | `jabali db root-password --engine postgres` (existing) | Same exposure class (`root:jabali 0640`). Only if the Postgres engine is enabled on the host. |
| MariaDB root password | `jabali db root-password --engine mariadb` (existing) | Break-glass root credential; socket auth preserved (ADR-0097). |
| Panel TLS key (`/etc/jabali/tls/panel.key`) | `jabali panel-cert …` reissue (existing) | Regenerate/reissue the panel cert; verify `/login` 200 after. |
| Panel-mail TLS key (`/etc/jabali/tls/panel-mail.key`) | `jabali mail-cert …` reissue (existing) | Reconciler-managed (ADR-0105); reissue and let the mail-cert cascade settle. |
| `JABALI_REDIS_PANEL_TOKEN` (`panel.env` + redis aclfile) | **planned** `jabali secrets rotate redis-panel-token` | Must rotate **live**: `ACL SETUSER jabali_panel resetpass >…` on the running Redis, then rewrite `panel.env` + the aclfile `user jabali_panel` line, then restart panel. **Never `ACL LOAD` / restart Redis** — that wipes the runtime `wp_<osuser>` tenant ACL users until the reconciler recreates them. |
| `PDNS_DB_PASSWORD` (`pdns.env`) | **planned** `jabali secrets rotate pdns` | Rotate the `jabali_pdns` DB user + rewrite `pdns.env` (gmysql backend) + restart PowerDNS. Only if PowerDNS is installed. |
| `migration-secrets/<job>.env` | **purge, don't rotate** | Transient Plesk/cPanel/SSH **source-host** credentials. Any file lingering from before the fix exposed a third party's password — delete lingering files; the operator re-enters source credentials on the next migration. Not our secret to rotate. |
| `JABALI_WP_CACHE_HMAC_SECRET` (`panel.env`) | **deferred** (documented risk) | See below. |

### Deferred: WP-cache HMAC secret

`JABALI_WP_CACHE_HMAC_SECRET` derives a per-tenant Redis-cache ACL token that is baked into **every** WordPress tenant's `wp-config.php`. Rotating it therefore is **not** a one-file swap: it requires re-deriving and rewriting every tenant's `wp-config.php` via the agent — a fleet-wide, per-tenant fan-out that must be throttled (the fleet's 2 GB VMs can fall into a kswapd death-spiral under unbounded fan-out). It is deferred to a dedicated, throttled reconciler task rather than bolted onto this tool. Until then, treat the value as exposed: it grants access only to the shared tenant object cache, not to panel/DB/TLS material.

### Out of scope

Kratos session/cookie/`cipher` secrets (not `jabali`-group-readable — under Kratos config, which webmail could not read), DKIM keys (`root:jabali-sftp 0600`), tenant DB passwords, tenant Let's Encrypt lineage, and the Stalwart admin token (`jabali-mail`). Because Kratos secrets are out of scope, **no fleet-wide forced logout and no `password_enc`/`secrets.cipher` re-encryption is required.**

> Before the first live rotation on a host, confirm this by checking that the Kratos secrets file is not group-`jabali`-readable. If it is, it enters scope and needs a separate multi-secret Kratos rotation (new secret first, old retained, cipher re-encrypt) — a migration, not a swap.

## Recommended order (per host)

1. `db-app-user` — highest value; verify `/login` still works after.
2. `postgres` / MariaDB root — if those engines are present.
3. `redis-panel-token` — once tooled.
4. `pdns` — if PowerDNS present.
5. Panel + panel-mail TLS reissue.
6. `jwt` — cheap, do it alongside any panel restart.
7. Purge lingering `migration-secrets/*.env`.
8. Note the deferred WP-cache HMAC as an open item.

## Verify after each rotation

- Panel: `systemctl is-active jabali-panel` and `curl -sk https://<host>/login -o /dev/null -w '%{http_code}'` → 200.
- Dispatcher/Redis: the panel's Redis-backed queues still process (no ACL errors in `journalctl -u jabali-panel`).
- Mail (after TLS): send a test message.

## Rollback

Each command rolls itself back automatically if its post-rotation probe fails (restores the file from `.rotate.bak` and reverts the privileged change). If you need to roll back manually after the fact, the `.rotate.bak` is gone on success by design — recover from your host backup.

## Closing the ticket

JAB-357 crit-7 closes when the operator has run the rotation across the affected hosts **and** the external auditor (`codex-security-test`, who filed JAB-351/357) has re-confirmed the exposed secrets are invalidated — not when this tooling merges.
