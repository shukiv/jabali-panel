# GH #1053 — FTP / SFTP accounts for tenants

**Status:** planned (blueprint only, nothing built)
**Issue:** GH #1053 "Feature: Create a normal ftp user"
**Decided:** 2026-08-15 — extend the native OpenSSH path + add vsftpd as an
opt-in module. SFTPGo evaluated and rejected (see "Why not SFTPGo" below).

## Goal

Tenants can create additional file-transfer accounts scoped to a directory
(typically a docroot), usable over SFTP always, and over FTPS when the admin
has opted the server in. Accounts are self-service within a per-package cap.

## Product requirements

1. **Tenant self-service.** Users create/manage their own FTP/SFTP accounts
   from the user shell — username, password, home directory (picker scoped to
   their own tree), enable/disable, delete. No admin involvement per account.
2. **Package-gated.** `hosting_packages.max_ftp_accounts` (int, default 0)
   caps how many accounts each user may create. 0 = feature invisible for
   that user (tenant caps default OFF — see memory
   `feedback_tenant_caps_default_off`).
3. **FTP is opt-in at the server level.** `server_settings.ftp_enabled`
   (bool, **default false**). Admin flips it in Server Settings → the panel
   installs/starts the vsftpd module (M353 install-on-enable pattern, gated
   on DB truth per GH #727). While off: no FTP daemon installed, port 21
   closed, accounts still work over SFTP. The reconciler masks vsftpd when
   the flag is off (same pattern as pdns masking, GH #447).
4. **SFTP always available** for these accounts (no server-level toggle —
   sshd is already there; M12 hardening applies).
5. **FTPS-only by default.** When FTP is enabled, TLS is required
   (`force_local_logins_ssl=YES`). A separate explicit admin toggle
   `ftp_allow_plaintext` (default false) exists for legacy clients, with a
   warning in the UI. Never relax this silently.

## Core design

### Same-uid alias accounts

Each FTP/SFTP account is a REAL system user created with the TENANT's uid
(`useradd --non-unique --uid <tenant-uid> --gid <tenant-gid>`), its own
username + password (shadow), home = the chosen directory. Consequences, all
load-bearing:

- Files created via any protocol are owned by the tenant uid → per-user
  POSIX quotas, per-user PHP-FPM, AppArmor docroot profiles, setgid
  www-data docroots, and backups all behave identically to files the tenant
  created themselves. Zero ownership drift.
- Auth is per-username (PAM/shadow), so disable/delete/password-change is
  per-account without touching the tenant's own login.
- Known cosmetic quirk: `who`/`last` may display the first passwd entry for
  the uid. Accepted.
- HARD RULE: alias uid must never be 0 and never a non-tenant system uid —
  validate against the tenant row, mirror the panel-side allowlist on the
  agent side (M14 defense-in-depth). Scar: `feedback_no_root_in_sftp_group`.

### SFTP half (no new daemon)

sshd `Match User` blocks (rendered into a dedicated include file, e.g.
`/etc/ssh/sshd_config.d/jabali-xfer.conf`): `ChrootDirectory` +
`ForceCommand internal-sftp -u 0007` + no TCP/agent/X11 forwarding. Reuse
M12's chroot layout solution for the root-owned-path requirement
(ChrootDirectory demands root-owned, non-group-writable ancestors — the M12
jail layout already solved this; do NOT invent a second layout). Membership
in `jabali-sftp` group as per M12. `sshd -t` before reload, always
(a bad include = fleet-wide SSH lockout; see `feedback_ssh_dual_enable_lockout`).

### FTP half (vsftpd, optional module)

- vsftpd chosen over ProFTPD/pure-ftpd: Debian-native (memory:
  `feedback_nginx_debian_native_not_sury` — same reasoning), smallest attack
  surface, PAM auth so the SAME accounts/credentials work for both
  protocols, forks + setuid per session (kernel-enforced ownership),
  `chroot_local_user=YES`.
- TLS: serve the panel hostname certificate (FTPS has no workable SNI story
  across clients; document that the FTP endpoint is `ftp.<panel-hostname>` /
  panel hostname, not the tenant's domain).
- Passive mode: fixed port range (e.g. 40000–40100) + `pasv_address` from
  server settings when behind NAT; UFW opens 21 + the passive range ONLY
  when the module is enabled, closes them when disabled.
- PAM: restrict to users in a `jabali-ftp` group (accounts get the group
  only when their `ftp_access` flag is on) so tenant primary logins and
  system users can never FTP in.
- CrowdSec: enable the vsftpd collection (log parser + brute-force
  scenario) when the module installs.
- Fail mode: module install failure must not abort panel install/update
  (memory: `feedback_optional_component_cant_abort_panel`).

### Why not SFTPGo

Evaluated 2026-08-15 (user suggestion). Solid project, wrong tenancy fit:
(1) isolation is userspace path-normalization, not kernel chroot+uid — its
own SECURITY.md says so; one path bug = cross-tenant access with daemon
privileges, and adopting it would downgrade the kernel-enforced model we
already ship (security-over-functionality doctrine forbids that trade);
(2) single-process daemon → uploaded-file ownership depends on app-level
chown, while our quotas/FPM/AppArmor assume tenant-uid ownership natively;
(3) second user store + REST + admin UI to reconcile and CVE-track, next to
the OpenSSH path we keep anyway. Revisit only if S3/WebDAV/HTTP-share
backends become requirements.

## Data model

```
ftp_accounts
  id            CHAR(26) ULID PK
  user_id       CHAR(26) FK -> users (tenant owner)
  username      VARCHAR(64) UNIQUE  -- full system username, e.g. deploy@tenant or tenant_deploy
  home_path     VARCHAR(512)        -- absolute, must be inside the tenant's home
  ftp_access    BOOL default 0      -- member of jabali-ftp group
  sftp_access   BOOL default 1
  is_enabled    BOOL default 1
  created_at / updated_at
```

- Password: NEVER stored in the panel DB. Set via agent (`chpasswd`) at
  create/reset; DB stores no hash. (`json:"-"` discipline is moot — there is
  no column.)
- Username namespace: prefix with the tenant username
  (`<tenant>_<label>`, validated `[a-z0-9_]`, total ≤ 32 chars) so accounts
  can't collide with real users or other tenants' accounts.
- server_settings: `ftp_enabled` BOOL default 0, `ftp_allow_plaintext` BOOL
  default 0, `ftp_pasv_address` TEXT NOT NULL default '' (TEXT, not
  VARCHAR — server_settings is at the InnoDB 65535-byte row-size ceiling;
  a utf8mb4 VARCHAR(64) fails with ERROR 1118 on real boxes).
- hosting_packages: `max_ftp_accounts` INT default 0.
- GORM scars to respect: no `default:1` on bools that must accept false
  (`feedback_gorm_default1_bool_zero_value`), Select-allowlist on updates
  (`feedback_domain_update_allowlist_silent_drop`), MariaDB FK needs
  explicit COLLATE (`feedback_mariadb_collation_fk`).

## Steps (each = one branch, one PR)

### Step 1 — schema + models + repository (`gh1053-ftp-schema`)
Migration (next free number — grep `panel-api/internal/db/migrations/`),
models, `FtpAccountRepository` (interface + gorm impl + sqlmock tests),
package field + server-settings fields. Schema-only migration, no seeding
(`feedback_migration_data_seed_ordering`).

### Step 2 — agent commands (`gh1053-ftp-agent`)
`ftpaccount.create|delete|set_password|set_access|list` in
`panel-agent/internal/commands/ftp_account.go`. Same-uid alias creation,
home-path validation (must resolve inside tenant home, no symlink escape —
use the filesafe helpers), `jabali-ftp`/`jabali-sftp` group membership,
chpasswd via stdin (never argv). Env-gate destructive tests
(`feedback_gh994_destructive_tests_root_prompt`). Wire types in `agentwire/`.

### Step 3 — sshd Match rendering + reconciler (`gh1053-ftp-sshd`)
Renderer for `/etc/ssh/sshd_config.d/jabali-xfer.conf` from DB rows;
reconciler tick converges system users + Match blocks (idempotent,
per-tick — `feedback_per_tick_idempotent_loops`); `sshd -t` gate before
reload; drift heal (account row deleted → system user removed). SFTP half
fully live after this step.

### Step 4 — vsftpd module (`gh1053-ftp-vsftpd`)
**PAM note (from step 3 review):** subaccounts ship with shell
`/usr/sbin/nologin` (blocks the no-Match-block shell path; internal-sftp is
in-process so SFTP is unaffected). Debian's stock vsftpd PAM includes
`pam_shells.so`, which rejects nologin — the custom PAM file MUST omit it
(do not add nologin to /etc/shells instead; that widens every other
pam_shells consumer).
`install.sh`: `install_module_ftp` (M353 install-on-enable; modular gate on
DB truth per #727), vsftpd config template, PAM file, UFW rules (21 +
passive range, added/removed with the toggle), CrowdSec collection,
`provision_new_software` convergence for existing hosts, reconciler
mask/unmask on `ftp_enabled` (GH #447 pattern). Every system dep declared in
install.sh (`feedback_deps_in_installer`). Logger: `_log/_ok/_warn/_err`
only.

### Step 5 — panel API (`gh1053-ftp-api`)
**Latency note (from step 3 review):** the create path must call
`ssh.user.home_chown` + `ftpaccount.sshd_sync` SYNCHRONOUSLY after
`ftpaccount.create` — waiting for the reconciler tick leaves a fresh
account unable to log in for up to 60s.
`RegisterFtpAccountRoutes` per route-family convention: tenant CRUD under
`/me/ftp-accounts` (cap-checked against package), admin list/override under
`/admin/ftp-accounts`, server-settings PATCH gains the ftp fields (allowlist
update). 422 on cap exceeded, rate limits, OpenAPI coverage
(`feedback_openapi_coverage_golden`), list envelope
`{data,total,page,page_size}`.

### Step 6 — UI (`gh1053-ftp-ui`)
User shell: "FTP Accounts" page (SearchableTable + shared create/edit
Drawer, `@icons`, `scroll={{x:"max-content"}}`), password-reset flow,
connection-info panel (host, port, protocol matrix, FTPS caveat). Page/nav
hidden when `max_ftp_accounts == 0`. Admin shell: Server Settings card with
the **FTP opt-in toggle** (+ plaintext toggle behind it, warning copy) and
passive-address field; Hosting Packages drawer gains `max_ftp_accounts`.
Verify wire contract against handler (`feedback_verify_wire_contract`).

### Step 7 — E2E + docs + closeout (`gh1053-ftp-e2e`)
**E2E additions (from step 3 review):** a REAL `sftp` login against a
rendered Match User block (proves nologin + in-process internal-sftp end to
end) and a NEGATIVE check that an ftp-only alias cannot open an SSH shell
session. **Runbook note:** a tenant's first SFTP subaccount flips
/home/<tenant> to root:tenant 0751 (M12 layout) even when the tenant never
enabled SSH — top-level $HOME becomes non-writable for the tenant
(established M12 trade-off, new for this population).
Playwright: create account → connect SFTP (sshpass loopback) → upload →
file owned by tenant uid → FTP toggle on → FTPS login (curl
--ftp-ssl) → cap enforcement → delete cleans system user. Box-test on
testserver via the user-facing path (`feedback_verify_via_user_facing_path`).
Runbook `docs/runbooks/ftp-accounts.md` (NAT/pasv, firewall, legacy
plaintext). CLI reference golden regen if CLI verbs added
(`feedback_cli_reference_golden`). Comment on GH #1053 (never close —
`feedback_never_close_issues`).

## Security checklist (gate for every step)

- No uid-0 / non-tenant uid aliases; agent re-validates what panel sends.
- Chroot ancestors root-owned; `sshd -t` before every reload.
- FTPS required by default; plaintext = explicit admin opt-in with warning.
- Passwords via chpasswd stdin only; never logged, never in DB, never in
  list/get responses.
- UFW passive range opened only while module enabled.
- `/etc/jabali` stays 0755 (`feedback_etc_jabali_must_be_0755`); no
  hardening directive removed from sshd/vsftpd units to make a feature work.
- CrowdSec brute-force coverage from day one.
- Home-path validation: absolute, inside tenant home, symlink-resolved
  server-side (path traversal = cross-tenant read).

## Open questions (resolve before Step 4)

1. Passive-range size + default (40000–40100 proposed) and IPv6 posture.
2. `pasv_address` auto-detect from server IP vs manual setting only.
3. Per-account bandwidth limits — vsftpd supports; ship later unless asked.
4. Quota display: accounts share the tenant quota (same uid) — UI copy
   should say so.
