# jabali2 — Development Handoff

> Single-file orientation for a fresh session with zero prior context.
> Pairs with: [BLUEPRINT.md](BLUEPRINT.md) (what ships, milestone log),
> [CONVENTIONS.md](CONVENTIONS.md) (code patterns + anti-patterns),
> [adr/README.md](adr/README.md) (ADRs through 0124), [RUNBOOK.md](RUNBOOK.md)
> (ops), [ENV.md](ENV.md) (env vars), [KNOWN_ISSUES.md](KNOWN_ISSUES.md).
> Last refreshed against `main` @ `7085d502` (2026-07-31).

## 1. What this is

jabali2 is a self-hosted **web-hosting control panel** (a cPanel/Plesk
replacement) for a single Debian server. One operator installs it with
`curl … | bash`; it then manages domains, DNS, SSL, email, databases,
PHP, cron, WordPress/apps, backups, file/SFTP/SSH access, security
(CrowdSec/AppSec/malware), and per-user resource limits — all from a
React SPA + REST API, with a privileged Go agent doing the root-level
system mutations.

Single-box model: panel-api, panel-agent, MariaDB, nginx, PowerDNS,
Stalwart (mail), Kratos (identity), CrowdSec all run on the same host.

## 2. Architecture

Three first-party components + a shared wire package:

| Component | Lang | Runs as | Role |
|-----------|------|---------|------|
| **panel-api** (`panel-api/cmd/server`) | Go / Gin / GORM | `jabali` user, systemd `jabali-panel.service` | REST API + reconciler + all CLI subcommands (`jabali <cmd>`). The control plane. |
| **panel-agent** (`panel-agent/cmd/jabali-agent`) | Go | **root**, `jabali-agent.service` | Executes privileged system ops (write nginx vhosts, run certbot, useradd, systemd units, Stalwart JMAP, etc.) over a **Unix-socket NDJSON** RPC. |
| **panel-ui** (`panel-ui/`) | React 18 + TanStack Query + AntD 6 + Vite | static, served by panel-api/nginx | The SPA. No Refine (dropped M21). react-router 7. |
| **agentwire** (`agentwire/`) | Go | — | Shared request/response types for the panel↔agent socket. |
| **jabali-ssh-shell** (`panel-agent/cmd/jabali-ssh-shell`) | Go | per-user login shell | M13 bubblewrap SSH sandbox. |

### Control-plane model (load-bearing — ADR-0002/0003/0004)

1. **The database is the single source of truth.** Filesystem/service
   state (nginx files, systemd timers, DNS zones, htpasswd, etc.) is
   *derivative* and rebuilt from the DB.
2. **One write path: the API.** CLI subcommands (`jabali …`) are thin
   HTTP/in-process clients, not peers. The SPA and the CLI both go
   through panel-api.
3. **The reconciler owns convergence.** A loop in panel-api reads DB
   desired-state and calls agent verbs to make the host match. It must
   be **idempotent per tick** — gate side-effects (reload, setquota,
   SIGHUP) behind a no-change compare, or you get reload storms.
4. **No PHP agent, ever (ADR-0001).** All privileged ops go through the
   Go agent over the socket. Agent commands are registered in `init()`
   via `Default.Register("verb", handler)` in
   `panel-agent/internal/commands/*.go` (224 command files).

### panel-api internal packages (`panel-api/internal/`)

`api` (HTTP handlers) · `reconciler` · `repository` (GORM data access) ·
`models` (GORM structs) · `db/migrations` (golang-migrate SQL) ·
`cronops` · `dnscompile`/`dnsverify` · `dockerapp` · `apps` (M19
registry) · `backupmetadata`/`backupscheduler`/`backupfinalizer` ·
`migrate/cpanel` (importer) · `nginxrules` · `notifications` ·
`sso`/`ssoadmin`/`webmailsso` · `mailscan` · `auth` (Kratos) ·
`eventsources` · `stalwartadmin` · `config`.

Repo-root `internal/` holds cross-binary shared packages (Go "internal"
rule): `appseccfg`, `phpext`, `cronvalidate`, `backup`, `kratosclient`.

## 3. Key decisions & why (ADR digest)

Full set in `docs/adr/` (numbered through 0124). The load-bearing ones:

- **0001** Go agent over NDJSON Unix socket — no PHP, ever.
- **0002/0003/0004** DB is truth · one write path (API) · reconciler-driven convergence.
- **0005** GORM + golang-migrate (SQL migrations, **not** AutoMigrate).
- **0014** panel on :8443, user sites on :443.
- **0017** SSL: try ACME → fall back to self-signed → retry every 3h.
- **0020/0034** Kratos is the **only** auth source (M20 removed the legacy stack entirely — no provider flag, no refresh tokens, no panel-side 2FA; 2FA is Kratos-native per M20.1).
- **0023/0025** Per-user PHP-FPM: global `php<v>-fpm.service` is **masked**; real workers are `jabali-fpm@<user>.service`, version pinned per user.
- **0036→0038** M16 Hydra/OIDC **rolled back** (PKCE incompat); replaced by M22.
- **0040** M22 panel→WP-admin SSO = self-deleting `jabali-sso-<43>.php` file (Installatron pattern), NOT magic-link/HMAC.
- **0042/0045** Mail: Stalwart v0.16, **SQL directory** — `jabali_panel.mailboxes` rows ARE the principals (Stalwart re-reads on auth/SMTP). This is why mail accounts restore from DB rows alone.
- **0050** M25 localhost hardening: Kratos/panel/Stalwart admin on Unix sockets; MariaDB `skip-networking`; TCP :3306 closed.
- **0060/0102** CrowdSec/AppSec; panel API (`/api/v1/`) + webmail vhosts allow-listed out of the WAF (Kratos-gated control plane, not public attack surface).
- **0066/0105** Let's Encrypt cert for the panel hostname; split hostname/mail certs.
- **0122** Account-restore does **NOT** run `stalwart-cli apply` (redundant with the DB path + a destroy-all blast radius). Mail accounts restore via DB; messages via the Maildir path below.
- **0123** Per-user mail **message** backup/restore via JMAP Maildir export/import (replaces the whole-store `bodies.tar`).
- **0124** jabali CRS "before" plugin for surgical AppSec false-positive exclusions.

## 4. Schema

- **166 SQL migrations** in `panel-api/internal/db/migrations/`
  (`NNNNNN_name.{up,down}.sql`). **Next free number: 000167.** Applied by
  golang-migrate on every panel-api boot. Migrations = **schema only**,
  never data seeds (a seed from an app-populated table bricks fresh
  installs — see Gotchas). Latest: mail_certificate, docker_apps,
  update_center, username_login, nginx_settings, add_mail_provider.
- Core tables: `users`, `domains`, `dns_records`, `ssl_certificates`,
  `panel_certificate` (singleton), `databases`/`database_users`/grants,
  `php_pools`, `cron_jobs`, `mailboxes`/`email_forwarders`/
  `email_autoresponders`/`mailbox_shares`, `application_installs` (M19,
  one row per app), `docker_apps` (M48), `ssh_keys`, `backup_*`,
  `notification_*`, `audit_events`, `server_settings` (app-populated
  first-boot), `managed_ips` (M24), `user_limit_overrides`,
  `user_egress_policies`.
- Live inspection: the `mcp__jabali-db__mysql_query` MCP (local DB) — use
  it to verify a migration ran / check live data instead of guessing.

## 5. Services jabali ships (systemd)

`jabali-panel.service` (api) · `jabali-agent.service` (root agent) ·
`jabali-kratos.service` · plus timers: backup-retention, freshclam,
maldet (scan/monitor/update), malware-quarantine-purge,
crowdsec-hub-refresh, firehol-blocklists, goaccess, aide-check,
per-user-egress, pmf-update, signature-base-update,
migration-secrets-reap. Per-user PHP: `jabali-fpm@<user>.service`.
`install.sh` is the **source of truth** for required services/perms/
group membership — NOT a separate runbook (cutover CLIs don't run on
fresh hosts).

## 6. What's done (milestone log — see BLUEPRINT.md for detail)

SHIPPED: M1 foundations · M2 domains/nginx/redirects · M3 server
settings/hostname/IPs · M4 DNS (+secondary NS) · M5 SSL/LE · M5c+M20.1
2FA (Kratos) · M6 Email (Stalwart+Bulwark) + M6.3 recursor + M6.4 panel
hostname mail + M6.5 mail features + M6.6 per-domain mail TLS · M7
MariaDB · M8 Cron · M9 PHP-FPM + M9.6 extensions · M10 WordPress
(generalised by M19) · M11 File Manager (AntD-native) · M12 SFTP · M13
SSH sandbox (bubblewrap) · M14 Notifications · M18 resource limits · M19
Applications Framework (15-app registry) · M20 Kratos (legacy removed) ·
M21 drop Refine · M22 SSO-file · M23 responsive UI · M24 IP manager ·
M25/M25.1 unix-socket hardening · M26 Security tab (CrowdSec/UFW; ModSec
removed M27) · M27 CrowdSec extensions · M29 admin updates+support · M30
backups (+destinations/schedules) · M31 server status · M32 panel LE
cert · M33 malware (ClamAV/LMD/YARA) · M34 per-user egress firewall ·
M43 unified trust · M47 email deliverability · M48 docker apps · M49
audit log · M53 updates center · M54 username login · M181 per-domain
mail provider.

ROLLED BACK: **M16** (Hydra/OIDC identity federation).
PLANNED/not built: **M15** migration importers (cPanel partial exists),
**M17** diagnostic reports.

## 7. In progress / most recent (this session, 2026-07-31)

Shipped this session, all on `main` + `stable` @ `7085d502`, deployed to
testserver (`sudo jabali update`) + verified:

- **Download folder/file 500** (#786 / GH #756): the archive was staged
  in `/tmp`, but `jabali-panel` runs `PrivateTmp=yes` so it can't see a
  file `jabali-agent` wrote there → `os.Open` ENOENT. Now staged under
  `/var/lib/jabali-uploads` (shared, outside the tmp sandbox). This is a
  RECURRING class — any agent→panel file handoff by path must avoid
  `/tmp` (see §9 + `feedback_agent_panel_file_handoff_not_tmp`).
- **Plesk/SSH migration wizard** (#785 / GH #429): Test Connection now
  uploads the operator's credential BEFORE testing (it reads the on-disk
  secret); the test-connection handler clears its write deadline + is
  bound to 45s so a slow source handshake returns clean JSON instead of a
  Cloudflare 52x; passphrase-protected keys give an actionable error.
- **Admin sidebar fixed while scrolling** (#784): `AdminLayout` switched
  from `position:sticky` to a fixed-shell (`height:100vh; overflow:hidden`
  outer, only `Content` scrolls).

Earlier the same day (already on stable): #760 lean default install +
2GB-min/4GB-recommended RAM guidance, JAB-174 PHP-pool determinism,
JAB-180 WP core version+language, #779 legible failed-rebuild, #746 Plesk
files/DBs.

## 8. Open TODOs

**Current (2026-07-31 — pick up here):**

- **GH #787 — reporter's own PR** (`patch-2`, lxsdevcode): adds
  `net.SplitHostPort(host)` to `plesk/discover.go` so a `host:port`
  string overrides `d.Port`. It's a WORKAROUND for `d.Port==0` in their
  env. Recommend **don't merge** — redundant vs the dedicated
  `source_port` field (already in stable since #463/#444, 2026-07-18) +
  fragile (breaks IPv6). **Awaiting operator decision to reply/close.**
- **GH #429 — custom SSH port** replied with a diagnostic (asked for
  `jabali version`; a job created before the fix keeps its old
  `source_port`; create a FRESH job). Code proven correct end-to-end
  (create persists `source_port`, no reset path, `pull-source` reads it).
  Awaiting reporter's version. Worth adding: a `source_port` persistence
  regression test.
- **GH #756** fix shipped + replied; awaiting reporter confirmation.
- **GH #731 — low-RAM install NEEDS DIRECTION:** 2GB hosts still OOM on
  the on-box vite/go build; `ensure_swap` (install.sh) soft-returns 0
  when `swapon` is blocked (containerized VPS). Decide: prebuilt-artifact
  install (no on-box compile — `plans/installer-release-resolution.md`)
  vs. a small `ensure_swap` fail-loud-and-early hardening.
- **Box cleanup (session artifacts):** `.150` has `demolab`/`demolab_test`
  MariaDB users from a diagnostic import; `.88` (Plesk repro source) has a
  throwaway key `~/.ssh/plesk88_repro`, sshd `Port 2222`, and a disabled
  `ssh.socket`. Remove when the #429/#787 thread closes.

**Older (pre-2026-07, verify still open before acting):**

- **Prod data cleanup (cron):** existing imported cron rows are
  `enabled=1` (the #388 bug). One-off:
  `UPDATE cron_jobs SET enabled=0 WHERE command NOT REGEXP '^(wp|php)[[:space:]]'`
  per affected account, or disable curl/wget crons in the UI. #388 only
  fixes future imports.
- **Mail message replay — backup side already done**, but legacy
  pre-ADR-0123 snapshots carry the old whole-store `bodies.tar` (manual
  restore only). New snapshots carry per-user Maildir.
- **Admin allowlist:** decide whether to remove `84.108.108.166` from
  the CrowdSec allowlist now that the 933120 FP is fixed at the rule
  level (restores WAF coverage on that IP). Prod-box decision.
- **`uid_at_source` on cross-host restore** (ADR-0123 out-of-scope):
  account-restore re-chowns by name (internally consistent); only the
  system-backup `os_users` stage persists raw uids — align there if/when
  needed, with uid-already-taken handling.
- **Sieve scripts / JMAP per-mailbox settings** are not backed up
  per-user (out of ADR-0123 scope).
- **M15** migration importers, **M17** diagnostic reports — planned, not
  built.
- **Live-VM validation pending** on several recent milestones (check
  each milestone's memory/BLUEPRINT note before assuming prod-proven).

## 9. Gotchas (hard-won; ignore at your peril)

**Process / workflow**
- **Verify on a box.** build+vet+unit green ≠ working, especially for
  reconciler/agent/restore/wire features. The test boxes:
  `ssh root@10.0.3.14` (KVM, crowdsec, per-user FPM, no docker; panel
  `admin@jabali-panel.local`) and `ssh root@testserver` = mx
  (mx.jabali-panel.com, Docker host). Many bugs this codebase shipped
  were caught ONLY by a live round-trip.
- **Merge before commenting "fixed";** push to **both** remotes after
  every commit (Gitea `origin` + `github`). Gitea merge: `POST
  /repos/shukivaknin/jabali2/pulls/N/merge {"Do":"merge"}`; a 405 is
  combined-status lag → retry `{"force_merge":true}`. github sync:
  `git push github refs/remotes/origin/main:refs/heads/main`.
- **`jabali update` does NOT refresh systemd units** by itself; the
  repo checkout on a box is `/opt/jabali-panel` (not `/opt/jabali2`).
- VM local mods are often byte-identical to origin (validate-on-VM
  pattern) — `git diff origin/main` before any stash/reset.
- **NEVER add Co-Authored-By** to commits (global rule); use `trash` not
  `rm`.

**GORM / DB**
- **`default:N` tag substitutes for zero values on INSERT, even under
  `Select`** — `Enabled:false` on a `default:1` field silently writes 1.
  Drop the gorm default *tag* (keep the DB column DEFAULT). (#388)
- **`Select`-allowlist on Update silently drops** fields not listed —
  use dedicated `UpdateXxx` methods per column, not a generic allowlist.
- **GORM column tags for initialisms/digit-suffixes:** `TargetPID` →
  `target_p_id`; pin `column:` tags.
- **Migrations are schema-only.** A migration that SELECTs an
  app-populated table (e.g. `server_settings`) bricks fresh installs
  with "Dirty database version N" — seed from the app, not the
  migration. Audit migration numbering after every merge (collisions +
  ALTERs referencing not-yet-created tables).
- `json:"...,omitempty"` for server-assigned fields (Stalwart JMAP
  rejects `{"id":""}`).

**Mail / Stalwart (v0.16, SQL directory)**
- The `jabali_panel.mailboxes` row IS the account (auth + SMTP routing).
  No separate principal store to restore.
- `Email/import` does **not** dedup on Message-ID — dedup yourself.
- `x:Account/set` returns `primaryKeyViolation` (not `alreadyExists`)
  for an existing account email.
- Reserved/`.local` TLDs crash-spin Stalwart Domain/set (RFC 6761 guard
  in place).

**AppSec / CrowdSec**
- Fix a CRS false-positive with a jabali CRS before-plugin
  (`/var/lib/crowdsec/data/crs-plugins/jabali/jabali-before.conf`,
  written by `jabali appsec render-config`) using narrow
  `ctl:ruleRemoveTargetById=<rule>;ARGS:<arg>` scoped by URI — never
  `ruleRemoveById` or a path-allow. **Verify the real banning rule**
  (init rules like 901340 are `pass` non-blocking decoys).
- `appsec-rules/` is FLAT (no `crowdsecurity/` subdir); wrong path
  re-downloads 170 vpatch rules every update.

**Reconciler / agent**
- Gate every per-tick side-effect (reload/setquota/SIGHUP) behind a
  no-change compare, or you get reload storms.
- `pdns_control purge <zone>$` after any DNS backend write (PowerDNS
  caches survive SQL writes).
- A typed-nil pointer boxed in an interface is `!= nil` → guards pass →
  SIGSEGV on call. Match CLI `PreRunE` to the call graph
  (`requireDBAndAgent` if the command calls the agent) + reflect-IsNil
  guard in deps validators.
- `systemd User=X Group=<other>` drops X's primary group — pair with
  `SupplementaryGroups=X` if reading `root:X 0640` files.
- Account-restore re-chowns the whole home to `<user>:<user>`, which
  clobbers the `www-data` docroot group → 403; restore re-applies it
  (symlink-safe Lchown walk).
- **Agent→panel file handoff can't go through `/tmp`** (GH #756):
  `jabali-panel` runs `PrivateTmp=yes`, so a file `jabali-agent` writes
  to `/tmp` is invisible to panel-api (`os.Open` → ENOENT, not a perms
  error). Stage such files in a shared `/var/lib/jabali-*` dir that's in
  panel-api's `ReadWritePaths` + AppArmor (e.g. `/var/lib/jabali-uploads`);
  agent writes + chowns to `jabali:jabali`, panel-api reads + unlinks.

**Install / build**
- Every system package must be added to `install.sh` (never assume).
  `install.sh` logger has exactly `_log/_ok/_warn/_err`.
- nginx is **Debian-native** (not sury); sury PHP still used.
- vite OOMs on small VMs — cap `NODE_OPTIONS=--max-old-space-size`.
- `panel-ui`: use `npx tsc -b` / `npm run build` before push (`--noEmit`
  misses TS6133/TS2724 in this project-references setup).
- Don't use `--legacy-peer-deps` with npm `overrides` (lockfile `npm ci`
  rejects in prod).

## 10. Dev workflow

- **Build/test:** `make build` (both Go binaries), `make test` (race),
  `make test-ui` (vitest), `make test-e2e` (Playwright, builds SPA),
  `make lint` (golangci-lint + `tools/lint-install-sh.sh`). Integration
  tests need `JABALI_TEST_DATABASE_URL` (real MariaDB).
- **Patterns:** read [CONVENTIONS.md](CONVENTIONS.md) before a new
  handler/page/hook (route families, SearchableTable, Drawer for
  create+edit, list envelope `{data,total,page,page_size}`, rate limits).
  Wire contracts: read the real `panel-api/internal/api/*.go` envelope,
  don't trust a blueprint schema.
- **Git:** branch per change (never commit to `main` from an agent),
  conventional commits, PR via Gitea API, merge on green, sync github,
  delete branch. Rebase onto `origin/main` + re-test before any final
  report.
- **Code discovery:** `codebase-memory-mcp` graph
  (`search_graph`/`trace_path`/`get_code_snippet`, project
  `home-shuki-projects-jabali2`) before grep. Re-index after structural
  changes to `main`.
- **MCP first:** Context7 for library APIs, `jabali-db` for live schema,
  chrome-devtools/claude-in-chrome for UI verification, antd/shadcn for
  components.

## 11. Environments

- **10.0.3.14** — `jabali-panel.local`, KVM. CrowdSec, per-user FPM, no
  Docker. Panel `admin@jabali-panel.local`. Primary functional test box.
- **mx / `ssh testserver`** — mx.jabali-panel.com (182.54.236.60),
  Docker host (29.x). Use for docker-app validation.
- **Repo:** Gitea `git.jabali-panel.com/shukivaknin/jabali2` (origin)
  + GitHub `shukiv/jabali-panel` (github). Both kept in sync. CI:
  `.gitea/workflows/ci.yml` (Go + vitest + E2E), act_runner host-mode.
