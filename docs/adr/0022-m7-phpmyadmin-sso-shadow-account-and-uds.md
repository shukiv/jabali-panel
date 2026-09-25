# ADR-0022: M7 phpMyAdmin SSO — shadow admin account + UDS validate transport

**Date:** 2026-04-17
**Status:** Accepted — supersedes parts of ADR-0020
**Deciders:** Shuki

## Context

ADR-0020 accepted a signon-proxy approach for phpMyAdmin SSO: panel mints a
single-use token, user is redirected to a PHP bridge inside phpMyAdmin, bridge
exchanges the token for DB credentials over loopback, populates phpMyAdmin's
session, redirects in. The high-level flow is right, but three specifics were
wrong or under-specified and have bitten us:

1. **Which credentials go in the session?** ADR-0021 stores `database_users.password_hash`
   as bcrypt (reveal-once on create). The panel cannot reproduce the plaintext
   at SSO time, so feeding the user's actual DB-user credentials into the
   phpMyAdmin session is physically impossible.
2. **What "loopback" means.** ADR-0020 said "over Unix socket" without
   specifying which listener. The obvious reading — reuse the 8443 router
   behind a `127.0.0.1` binding — mixes a sensitive validate endpoint into
   the public mux and forces the bridge to be IP-trust aware.
3. **How phpMyAdmin gets on the box.** Jabali ships a single `install.sh`.
   phpMyAdmin was never added. Neither the Debian package (too old, apache2
   deps) nor an unreviewed tarball install are acceptable without a pinned
   version + checksum.

We also folded in findings from an adversarial review of `plans/phpmyadmin-sso.md`
(see review log at the end of that file): AES-GCM nonce-reuse safety, TOCTOU
on shadow-account ensure, nginx log redaction, reconciler crash-safety, and
key-rotation runbook. The decisions here are the design answers; the plan
carries the implementation steps.

## Amendment 2026-09-26 — exact per-database grants, not a `<user>\_%` pattern

§1's pattern grant, and the claim under Risks that "a user can only ever
reach their own databases", were wrong. Panel usernames may contain `_`, so
`<panel_username>\_%` for tenant `alice` also matches `alice_x_shop`, a
database of tenant `alice_x`. The shadow account had ALL privileges on the
sibling's data. The Adminer (PostgreSQL) shadow `<user>_pgadmin` had the same
flaw at database level (`datname LIKE '<user>\_%'`).

Now:

- `db.mysqladmin.ensure` creates or rotates the account and grants **no**
  database.
- On every phpMyAdmin open, the panel sends the explicit list of the
  tenant's own MariaDB databases (`databases WHERE user_id = ? AND
  engine = 'mariadb'`). `db.mysqladmin.sync_grants` makes the account's
  `mysql.db` grants exactly that list. It revokes everything else, including
  the old wildcard, and grants each database by name with `_` and `%`
  escaped. A failed sync refuses the open.
- A user rename clears the renamed account's database grants; the next open
  re-grants the renamed databases.
- The Adminer shadow follows the same rule through
  `db.postgres.shadowadmin.grant_schema`.

The boundary remains the MariaDB grant (§7's `only_db` stays
defense-in-depth), but it is now the tenant's own database list from the
control plane, never a name pattern.

## Decision

### 1. Shadow admin account per panel user

Each panel user gets **one MariaDB user** named `<panel_username>_mysqladmin`
with a single grant:

```sql
GRANT ALL PRIVILEGES ON `<panel_username>\_%`.* TO '<panel_username>_mysqladmin'@'localhost';
```

No `WITH GRANT OPTION`. No global privileges. The underscore in the database
pattern is escaped so `alice_wp` matches but `alice%other` does not.

SSO always logs the user into phpMyAdmin as this shadow account, regardless
of which individual `database_users` row they manage through the panel. The
`database_users` entity remains the panel's abstraction for
connect-from-application credentials; the shadow account is only for
administering schemas through phpMyAdmin.

#### Alternatives considered

- **Store an encrypted mirror of each `database_users` password.** Adds
  plaintext-equivalent storage next to bcrypt, doesn't help existing users
  whose plaintext is already lost, doubles the credential rotation surface.
  Rejected.
- **Mint a transient MariaDB user per SSO click and drop it on session
  expiry.** Works but trips MariaDB's `max_connections` under churn, and
  phpMyAdmin has no hook to "session ended, drop my user now." Viable as a
  **Plan B** if `only_db` turns out to be cosmetic (see decision 7).
- **Skip SSO, tell users to use an external MySQL client.** Loses feature
  parity with every competitor. Rejected.

#### Consequences

- Works for all pre-existing users; reconciler backfill pass can provision
  them lazily without prompting for passwords.
- One shadow account per panel user means phpMyAdmin sessions can see every
  database that user owns, bounded by `only_db` (decision 7) for the
  specific DB they clicked.
- If the shadow password leaks from the DB, blast radius is one panel
  user's databases, not every user's.

### 2. phpMyAdmin install method: pinned tarball

`install.sh` downloads the phpMyAdmin tarball to `/opt/phpmyadmin/<version>/`,
verifies against a SHA-256 checksum vendored in `install/phpmyadmin.sha256`,
and symlinks `/opt/phpmyadmin/current`. Upgrades replace the symlink; old
versions stay on disk for rollback.

phpMyAdmin runs under a dedicated system user `jabali-pma` via a dedicated
`php-fpm` pool `jabali-pma` on `/run/php/jabali-pma.sock`. That pool's user
is added to `www-data` supplementary group so it can connect to
`/run/jabali/sso.sock` (decision 4).

#### Alternatives considered

- **Debian `phpmyadmin` package.** Pulls apache2 and libapache2-mod-php; we
  run nginx. Version is years behind upstream. Rejected.
- **Docker container.** Adds a Docker dependency to a panel that prides
  itself on single-host bare-metal installs. Rejected.
- **Serve phpMyAdmin from panel-api's Go mux.** phpMyAdmin is PHP; we'd
  have to bundle a PHP interpreter. Rejected.

#### Consequences

- Install path is fully reproducible and auditable.
- phpMyAdmin upgrades are a two-line change in `install.sh` (bump the
  version constant and checksum).
- Separate `jabali-pma` user means a compromised phpMyAdmin process cannot
  read `jabali`-owned files (including `/etc/jabali-panel/sso.key`).

### 3. SSO token: 32 bytes, SHA-256-hashed, 5-minute TTL, single-use

Tokens are 32 bytes from `crypto/rand`, base64url-encoded for URL transit.
The database stores only `SHA-256(token)`, never the raw token. TTL is 5
minutes. Consume deletes the row atomically (single-use enforced in the
same SQL transaction that returns the credentials). No IP binding.

#### Alternatives considered

- **Store the raw token.** DB leak = replayable tokens. Rejected — ADR-0020
  already settled this.
- **Bind to issuer IP.** Breaks mobile/roaming users who change networks
  between issue and validate. UDS transport (decision 4) eliminates the
  IP-trust problem, so IP binding buys nothing. Rejected.
- **Longer TTL (15–30 minutes).** Widens the replay window for leaked URLs.
  5 minutes is enough for a browser to follow two redirects. Rejected.

#### Consequences

- A leaked URL grants at most one login within 5 minutes.
- DB leak does not produce usable tokens.
- Users with aggressive clock skew or laggy networks may see occasional
  "token expired" errors — acceptable given the security win.

### 4. Validate transport: dedicated Unix domain socket listener

Panel-api opens a **second `http.Server`** on
`/run/jabali/sso.sock` (mode 0660, owner `jabali:www-data`) serving one
route: `POST /sso/phpmyadmin/validate`. The public 8443 listener never
handles this route. Loopback TCP (`127.0.0.1:<port>`) is explicitly
rejected.

#### Alternatives considered

- **Loopback TCP.** Anything on the box binding to localhost could
  impersonate phpMyAdmin's bridge. UDS ACL is the boundary we want. Rejected.
- **Same 8443 mux, authenticated with a shared secret.** Moves the trust
  question to secret management inside sso.php's environment. Larger attack
  surface. Rejected.
- **gRPC over UDS.** Overkill for one endpoint with two fields. Rejected.

#### Consequences

- Filesystem ACL is the authentication boundary; no IP lists, no bearer
  tokens, no headers to trust.
- Panel-api needs a second `http.Server` goroutine and clean shutdown to
  unlink the socket file.
- Upgrades must remove the stale socket file before the new panel-api
  creates its own (`install.sh` handles this).

### 5. Password encryption: AES-256-GCM, key at `/etc/jabali-panel/sso.key`

The shadow account's plaintext password is encrypted in Go with AES-256-GCM
before persisting to `users.mysqladmin_password_enc`. Envelope format:

```
nonce (12 bytes) || ciphertext || auth_tag (16 bytes)
```

Key is 32 random bytes at `/etc/jabali-panel/sso.key`, mode 0600, owner
`jabali:jabali`. Generated by `install.sh` on first run.

Every `Seal` call generates a fresh random nonce via `crypto/rand`. The
helper package includes a nonce-uniqueness test (two Seals of the same
plaintext must produce envelopes whose first 12 bytes differ) to prevent
accidental regressions that would catastrophically break AES-GCM
confidentiality.

#### Alternatives considered

- **Store the plaintext password.** A DB dump would hand out phpMyAdmin
  admin access to every user's databases. Rejected.
- **Derive the password from the panel user's login password with PBKDF2.**
  Panel passwords are bcrypt'd — no plaintext to derive from. Rejected.
- **External KMS (HashiCorp Vault, AWS KMS).** Single-node self-host
  panel; adding a KMS dependency is out of scope. Rejected.

#### Consequences

- Key rotation requires re-encrypting every `mysqladmin_password_enc`
  column in one transaction. Runbook procedure in
  `docs/RUNBOOK.md`: generate new key, pause reconciler, run
  `jabali-panel sso rotate-key` CLI, swap key file, SIGHUP panel-api,
  resume reconciler.
- Losing the key means every shadow account must be re-provisioned
  (agent's ensure command rotates on exist; reconciler re-populates all
  rows on next tick).

### 6. Provisioning: on-demand at first SSO click + reconciler backfill

The API handler provisions the shadow account lazily on the first SSO
click for a user who doesn't have one. The reconciler runs a separate
pass every 30s (`reconcileMysqlAdminShadow`) that calls the exact same
`ssoService.EnsureShadow(ctx, userID)` method for any user with
`mysqladmin_username IS NULL`, so first-click latency stays low for
pre-existing users.

Ensure-and-persist is wrapped in a single SQL transaction using
`SELECT ... FOR UPDATE` on the users row, preventing the TOCTOU where
two concurrent clicks double-provision.

If the panel-api process crashes between agent-ensure and DB-UPDATE, the
MariaDB user exists but the `users` row is still NULL. Next reconciler
tick calls `EnsureShadow` again; the agent detects the existing user and
rotates its password, producing a fresh plaintext the panel can persist.
Divergence cannot last longer than one tick.

#### Alternatives considered

- **Provision every user on `install.sh` run.** Doesn't handle users
  created after install. Reconciler pattern already solves this. Rejected.
- **Explicit "Enable phpMyAdmin" button in the UI.** Extra click for zero
  functional gain. Rejected.

### 7. phpMyAdmin session scope: `only_db` on the clicked database

The signon session sets `only_db = <final_db_name>` in phpMyAdmin's
`cfgupdate`. The shadow account can see every database matching
`<panel_username>_%`, but phpMyAdmin's UI will only show the clicked one.

**This is defense-in-depth, not a privilege boundary.** The MariaDB server
enforces the actual boundary via the shadow account's grant pattern. An
integration test in step 9 of `plans/phpmyadmin-sso.md` verifies that
phpMyAdmin honors `only_db` end-to-end. If that test fails (i.e.,
`only_db` turns out to be a cosmetic filter that can be bypassed by a
URL-crafted request), the plan falls back to **Plan B: transient per-DB
MariaDB users** minted at SSO time and dropped on session expiry.

#### Alternatives considered

- **No scoping.** User sees every one of their databases in the left
  nav. Confusing; the "Open phpMyAdmin for `alice_wp`" link should land
  you looking at `alice_wp`. Rejected.
- **Per-database shadow account.** One row per database instead of one
  per user. More rotation surface, same blast-radius ceiling given all
  DBs belong to one user anyway. Rejected; keep the simpler model.

### 8. Session-key contract: transcribed from the pinned tarball

**This ADR does not hard-code the PHP session key names that sso.php
writes.** phpMyAdmin's signon session structure has changed across 4.9,
5.0, 5.1, and 5.2. Step 6 of `plans/phpmyadmin-sso.md` requires the
implementer to open `/opt/phpmyadmin/current/examples/signon-script.php`
from the pinned tarball and copy the exact `$_SESSION[...]` layout used
there. The tarball's example script is the single source of truth.

Each phpMyAdmin version bump in `install.sh` must re-verify the session
key layout. If a future version changes the contract, sso.php changes
with it in the same commit.

#### Consequences

- ADR-0022 never goes stale when phpMyAdmin changes its session format.
- Upgrades are a two-file change (install.sh version constant + sso.php
  session writes), both reviewable in one diff.

## Supersedes (partial)

ADR-0020 remains accepted for these decisions:

- Server-side signon proxy is the correct approach.
- Tokens are single-use and short-lived.
- Validate over Unix socket, not network.
- Audit log every issue + validate.

ADR-0020 is superseded by ADR-0022 for these:

- **Which credentials go in the session** (shadow account, not the panel's
  `database_users` row).
- **Which listener hosts the validate endpoint** (dedicated UDS listener,
  not the public 8443 mux).
- **How phpMyAdmin is installed** (pinned tarball + dedicated fpm pool).
- **AES-GCM envelope format** for at-rest credentials (ADR-0020 was silent).
- **TOCTOU protection** and reconciler crash-safety (ADR-0020 was silent).
- **Session key contract** (ADR-0020 named specific `PMA_single_signon_*`
  keys; ADR-0022 defers to the pinned tarball).

## Consequences (combined)

### Positive

- One credential model works for every existing and future user.
- Filesystem ACL is the only trust boundary; no IP allowlists, no
  shared secrets in sso.php's environment, no JWT in URLs.
- DB leak exposes hashes and encrypted blobs only.
- phpMyAdmin upgrades are isolated to `install.sh` and `sso.php`.
- Reconciler convergence model prevents divergence from outliving one
  tick, even across panel-api crashes.

### Negative

- New operational surface: `/etc/jabali-panel/sso.key` must be backed
  up alongside the DB (losing it invalidates every shadow account).
- Key rotation requires a short downtime window (pause reconciler,
  re-encrypt rows, SIGHUP, resume) — documented in the runbook.
- `jabali-pma` system user is one more account to audit.

### Risks

- **phpMyAdmin `only_db` bypass.** Mitigated by (a) the integration test
  in step 9, (b) the Plan-B fallback to transient per-DB users, (c) the
  shadow account's grant being `<prefix>_%` not `*.*` — even if scoping
  is cosmetic, a user can only ever reach their own databases.
- **UDS permission drift.** An admin who tightens `/run/jabali/sso.sock`
  to 0600 would break SSO. The runbook documents the 0660 `jabali:www-data`
  requirement.
- **Tarball supply chain.** Mitigated by vendored SHA-256 checksum and
  pinned version; bump checksums explicitly, never auto-fetch latest.
