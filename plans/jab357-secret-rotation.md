# JAB-357 crit-7 — Rotate the secrets that internet-facing webmail could read

**Ticket:** JAB-357 (Security/Critical). Isolation (peercred UID gate + no-broad-group + upgrade migration + regression test) shipped in #1208/#1217/#1222/#1246. The **only open acceptance criterion** is #7:

> Rotate all panel, database, TLS, mail, and session secrets after remediation because the same account already has broad secret-read access (JAB-351).

This is the **exposure-remediation** half: the isolation stops *future* reads; rotation invalidates what an attacker *already* read before the fix landed. Both are needed to actually close a critical.

## Scope = the exposure class, not a hand-picked list

JAB-351 (completed 2026-08-23) proved `jabali-webmail` (in the broad `jabali` group) could read `0640 root:jabali` secrets under `/etc/jabali`. The regression test (`install/tests/test_webmail_group_isolation.sh`, 69 lines) only spot-checks representative files (`db-password`, agent socket) — it is **not** an exhaustive inventory. So scope is defined structurally:

> **Every secret file that was group-`jabali`-readable before the isolation fix.**

**Implementation step 1 is to enumerate that class mechanically** (walk `/etc/jabali` + `/etc/jabali-panel` for group `jabali` + group-readable mode, cross-checked against the `chown …:jabali`/`chown root:"$SERVICE_USER"` lines in install.sh git history), not to trust the table below. Enumerated so far:

| Exposed secret | Path | Consumer | Rotation exists? | Handling |
|---|---|---|---|---|
| **Panel DB password** | `/etc/jabali/db-password` + DSN in `panel.env` | panel-api as **`jabali_panel_app@localhost`** (its own app user, scoped to `jabali_panel` DB — NOT root) | **GAP** — existing `database root_password_rotate` rotates the **root/superuser**, a different credential | New rotate: `ALTER USER jabali_panel_app` → rewrite `db-password` **and** the `@unix(...)` DSN in `panel.env` → restart panel. Order: ALTER first, file after, restart last |
| `JWT_SECRET` | `panel.env` | **Vestigial** — `auth/claims.go` notes the legacy JWT surface was removed in M20; no `jwt.Sign/Parse` in panel-api; `config.go:210` marks it required-in-production (presence only). Webmail SSO uses a **separate** `/etc/jabali-panel/bulwark-jwt-auth.secret` (`BULWARK_JWT_AUTH_SECRET`), not this | trivial | Rotate = regenerate value + restart. Near-noop (no live signer) but included because it lived in the exposed file. Verify `config.go:210` only checks presence |
| Panel TLS key | `/etc/jabali/tls/panel.key`(+`.crt`) | nginx→panel-api | Yes — `ssl.panel.selfsign` verb (#1351) / LE reissue | Reissue via existing verb; verify `/login` 200 after |
| Panel-mail TLS key | `/etc/jabali/tls/panel-mail.key`(+`.crt`) | webmail/mail TLS; reconciler-managed (`panel_certificate_repository.go` ADR-0105, `webmail_reconcile.go`) | Yes — trigger the panel-cert reconciler reissue | LE-on-real-hostname vs self-signed fallback; mail-cert cascade |
| `JABALI_REDIS_PANEL_TOKEN` | `panel.env` + redis `aclfile` | panel-api ↔ Redis ACL user `jabali_panel` | **GAP** | install.sh deliberately never auto-rotates it. **Live** rotation: `ACL SETUSER jabali_panel resetpass >newtoken` on the running Redis, then rewrite `panel.env` + the aclfile `user jabali_panel` line for persistence, then restart panel. **Never `ACL LOAD` / restart Redis** — that wipes the runtime `wp_<osuser>` tenant ACL users the panel appends via `ACL SETUSER` until the reconciler recreates them |
| `JABALI_WP_CACHE_HMAC_SECRET` | `panel.env` | derives **per-tenant** wp-config cache ACL tokens baked into every tenant `wp-config.php` | **GAP** | **Defer with documented rationale**: rotation is a mass agent-driven rewrite of every WP tenant's `wp-config.php` + reconciler re-derivation — a fan-out that must be throttled (2 GB fleet VMs, kswapd death-spiral risk). Not a v1 secret swap |
| `postgres.password` | `/etc/jabali-panel/postgres.password` (`root:jabali 0640`) | panel-api ↔ Postgres superuser | Yes — `database root_password_rotate` (engine=postgres) | Same exposure class. In scope **iff** the PG engine is enabled (gated — confirm per-server flag vs fleet gate). Verify the rotate also rewrites the file + restarts |
| `migration-secrets/*.env` | `/etc/jabali-panel/migration-secrets/<job>.env` (`root:jabali 0640`) | transient Plesk/cPanel/SSH **source-host** creds per migration job | n/a — **purge, don't rotate** | These are one-shot source creds; any lingering pre-fix file exposed a third-party host's password. **Delete lingering files**; the operator re-enters source creds on the next migration. Cannot be "rotated" (not our secret) |
| `pdns.env` (`PDNS_DB_PASSWORD`) | `/etc/jabali-panel/pdns.env` | PowerDNS ↔ its MariaDB user | GAP (verify exposure) | Confirm owner/mode first; if `*:jabali`-readable it's in class → rotate the `jabali_pdns` DB user + rewrite pdns.env + gmysql backend + restart pdns |
| `config.toml` | `/etc/jabali/config.toml` | panel-api **non-secret** config | n/a | install.sh: "Non-secret config goes in config.toml." No credential to rotate |

**Explicitly out of scope (webmail could NOT read):** Kratos session/cookie/`cipher` secrets (not `*:jabali`-readable — under kratos config), DKIM keys (`root:jabali-sftp 0600`), tenant DB passwords, tenant LE lineage, Stalwart admin token (`jabali-mail`). This is the key relief: **no fleet-wide forced logout and no `password_enc`/`secrets.cipher` re-encryption migration.** Implementation step 1 must confirm kratos.yml is not `jabali`-readable; if it is, Kratos enters scope and this plan is re-estimated (multi-secret rotate + cipher re-encrypt is a migration, not a swap).

## Deliverable — tooling + runbook; the operator runs it on the fleet

Rotation *execution* on live hosts is the operator's ceremony ([[feedback_never_touch_fleet_unprompted]]). This PR ships the mechanism, not the act:

1. **`docs/runbooks/secret-rotation.md`** — ordered post-incident procedure per secret: command, expected downtime, verification (`/login` 200, dispatcher alive, test mail send), rollback, and **`.bak` purge**.
2. **`jabali secrets rotate <name>` CLI** for the gaps (`db-app-user`, `jwt`, `redis-panel-token`; `pdns` if in class). Reuse existing tools for DB root/PG (`database root_password_rotate`) and TLS (panel/mail cert reissue verbs). `secrets rotate all` orchestrates in a lockout-safe order.
3. Every subcommand:
   - `--dry-run` — prints the plan, touches nothing (**unit-testable with no box**).
   - **Atomic write** (tmp + `rename`) so a mid-rotation crash never leaves an empty secret that blocks service start.
   - **Back up old value** to a `root:root 0600` `.bak`, and **purge it after a verified health probe** (`--purge-backups` in the runbook) — a lingering `.bak` is a *new* copy of the old credential the next audit will flag.
   - **Preserve exact owner/group/mode** on the rewritten file (assert in test; never loosen — [[feedback_security_highest_priority]]).
   - Post-step **health probe with rollback-on-failure** (restore from `.bak`).

The ticket **closes when the operator runs the rotation on the fleet + an auditor re-confirms**, not when this merges. Say so in the ticket comment.

## Hard constraints
- **Explicit-invoke only.** Never wire rotation into install.sh's converger / `provision_new_software` / the 04:30 auto-update — that rotates every box unprompted. [[feedback_never_touch_fleet_unprompted]]
- **Preserve owner/group/mode**; assert it. [[feedback_docroot_setgid_www_data]]
- **Per-secret order/restart discipline** (DB: ALTER→file→restart; redis: live `resetpass`→file→restart, no `ACL LOAD`). A wrong order is a self-lockout. [[feedback_long_agent_call_502]]
- **No schema change, no DML, no migration-ahead.**

## Tests (unit layer needs no box)
- `--dry-run` per subcommand: prints plan, mtimes unchanged.
- Atomic-write + mode-preserve: rotate a temp fixture → new value written, `.bak` holds old, mode/owner identical, tmp gone, `.bak` purged on success.
- DB app-user rotate: fake executor records `ALTER USER jabali_panel_app` **before** the DSN file rewrite (order guard).
- Redis token rotate: asserts `panel.env` line **and** aclfile `user jabali_panel` line both become the same new token; asserts **no** `ACL LOAD`/redis restart is issued.
- Rollback: injected health-probe failure → old value restored from `.bak`, mode intact.
- Orchestrator: `rotate all` emits steps in the lockout-safe order.

## Box drill — AFTER the operator names the target
Rotation on the wrong box locks the operator out of production. Memory conflicts on `.60` (index: "PROD .60"; #391 file: ".60 is the Stalwart **test** box, .86 GONE"). Confirm the target in one line before any live rotation. The unit layer + `--dry-run` ship without a box.

## Operator questions (only what they alone decide)
1. **Drill target** — which box is safe to drill live rotation on?
2. **WP-cache HMAC** — ship a guarded/throttled rotate now, or defer as a documented risk (recommend defer)?
3. **Auditor sign-off** — JAB-351/357 were externally filed (`external_source: codex-security-test`). Does closing crit-7 need the auditor to re-confirm the rotation, or is operator execution enough?
