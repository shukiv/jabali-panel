# Code Review — Bugs & Security Findings (2026-08-08)

- **Scope:** read-only static review of the full repository by 8 parallel area reviewers: (1) panel-agent privileged command handlers, (2) panel-api auth & middleware, (3) panel-api REST handlers, (4) reconciler/jobs/backup/restore, (5) SSO/tokens/crypto, (6) install.sh + installer + install/ assets, (7) panel-ui frontend, (8) supporting services (hostedsvc, support-claim, ops, agentwire, internal/).
- **Commit reviewed:** `fda13d89` — "fix(db): apply Postgres memory tunables with an explicit byte unit (GH #968)"
- **Method:** manual source tracing of sinks from wire input to privileged operation; all Critical/High findings re-verified against source at the cited commit during consolidation.

## Summary

| Severity | Count |
|----------|-------|
| Critical | 0 |
| High     | 3 |
| Medium   | 10 |
| Low      | 16 |

- The three High findings are one root cause: the M30.2.x per-destination restic-password plumbing is bridged into `backup.create` but dropped on the async stage re-marshal, deleted before the async job finishes, and absent from the finalizer / repo-probe / system-backup / download paths. Rotating a destination to a per-destination password silently breaks backups and can seal "partial" manifests that later restore empty. Consider filing as a single issue.
- The installer fetches and executes/downloads four artifacts without integrity checks (Adminer, Go toolchain, Composer installer) — a deviation from the repo's own pin-everything discipline — and writes unvalidated hosted-API values into a root-`source`d env file.
- hostedsvc and support-claim (internet-facing, unauthenticated endpoints) have rate-limit/resource-bound gaps: spoofable X-Forwarded-For keys, unbounded in-memory maps, no per-IP register throttle.
- The core multi-tenant privilege boundaries are in good shape: filesafe openat2 scoping, ownership checks on user-scope routes, token single-use/entropy, HMAC construction, and symlink-safe restore all verified clean. The one real IDOR found is mailbox-share delete.
- Several Low findings are verified-not-reachable-today defense-in-depth gaps (flagged individually); they become live only if a future caller change lets untrusted input reach them.

## Findings

### Critical

None.

### High

#### HI-1: Per-destination restic passwords dropped on the backup write path
- **Area:** reconciler/jobs/backup/restore
- **Location:** `panel-agent/internal/commands/backup_create.go:266-273` (`runHomeStage`), `:292-304` (`runDatabaseStage`), `:438-445` (`runMailStage`); `panel-api/internal/backupscheduler/scheduler.go:610-645` (`dispatchSystem`); `panel-agent/internal/commands/backup_system.go:99-103`
- **Description:** M30.2.x gives each destination its own sealed restic password, bridged to the agent as a `/run/jabali/restic-pw/` tempfile via `WithDestPasswordFile`. But the async orchestrator re-marshals the home/db/mail stage params **without `PasswordFile`** (the stage param structs have the field — `backup_home.go:59`, `backup_databases.go:30` — it is just never populated), so those stages fall back to the legacy `/etc/jabali-panel/restic-repo.password`. System backups are worse: `dispatchSystem` never wraps `WithDestPasswordFile`, and `runSystemBackupOrchestrator` builds its config with `bkResticConfig` (no password parameter, `backup_system.go:103`).
- **Trigger/impact:** Rotate any destination to a per-destination password (the intended M30.2.x flow). Every subsequent account backup fails its home/db/mail stages against that repo; every system backup fails outright. Where the manifest snapshot does land, the finalizer can seal a "partial" backup that a tenant/operator later trusts for restore and finds empty — silent data loss at restore time.
- **Suggested fix:** Thread `PasswordFile` through the `runHomeStage`/`runDatabaseStage`/`runMailStage` param structs; add password bridging to `dispatchSystem` + `system.backup`; add a live rotated-destination round-trip test.

#### HI-2: Per-destination password tempfile deleted before the async backup finishes
- **Area:** reconciler/jobs/backup/restore
- **Location:** `panel-api/internal/backupwrapperhelpers/dest_password.go:73-82` (deferred cleanup), called from `panel-api/internal/backupscheduler/scheduler.go:588-595`; async consumer at `panel-agent/internal/commands/backup_create.go:149-165`
- **Description:** `WithDestPasswordFile` unlinks the tempfile via `defer` when the `backup.create` agent call returns. But `backup.create` is fire-and-forget: it spawns the orchestrator goroutine (up to 90-minute timeout) and replies immediately. The deferred `backup.repo.password.cleanup_temp` removes the password file seconds after the job starts, while the long-running orchestrator still needs it for the meta/manifest restic invocations.
- **Trigger/impact:** Any scheduled/on-demand account backup to a per-destination-password destination: the manifest snapshot fails with "unable to open password file" and (combined with HI-1) nothing lands. The helper is only safe for *synchronous* agent calls (restore), not the async create path.
- **Suggested fix:** For async jobs, have the agent copy the tempfile into the job's own lifecycle (e.g. `/run/jabali/restic-pw/<job_id>` cleaned by the orchestrator's `defer`), or make cleanup conditional on job completion (finalizer-driven).

#### HI-3: Finalizer, repo-probe, and download paths hardcode the legacy password file
- **Area:** reconciler/jobs/backup/restore
- **Location:** `panel-api/internal/backupfinalizer/finalizer.go:127-140` (no `password_file` in `backup.status` params); `panel-agent/internal/commands/backup_helpers.go:186,220` (`bkEnsureRepoReady` hardcodes `backup.DefaultPasswordFile`); purge at `panel-api/internal/reconciler/restic_legacy_password.go:44-49`; download path `panel-api/internal/api/backups.go:328-337` (`materializeDestParams`/`destWireParams` never send a password file)
- **Description:** Three independent spots assume the legacy shared password file: (a) the finalizer's `backup.status` poll can't open a per-destination repo, so a successful backup is never sealed — it sits "running" until the 4h stall timeout marks it failed; (b) `bkEnsureRepoReady` probes/inits with the legacy file — once the reconciler purges that file (all destinations migrated), the probe on a remote repo hard-fails fatally before any stage runs, and pre-purge a wrong-password probe is misclassified as "repo exists but unopenable" with a long misleading error; (c) tenant/admin downloads (`backup.materialize`) can't read the snapshot.
- **Trigger/impact:** Complete the per-destination password rotation the reconciler actively pushes the host toward. Backups to remote destinations then fail at ensure-repo, and any that do run are misreported as failed and can't be downloaded.
- **Suggested fix:** Accept + thread a password-file parameter in `bkEnsureRepoReady`; wrap the finalizer's `backup.status` and the materialize path in the same `WithDestPasswordFile` bridging the restore path already uses.

### Medium

#### ME-1: Mailbox-share delete is not scoped to the authenticated mailbox (IDOR)
- **Area:** panel-api REST handlers
- **Location:** `panel-api/internal/api/mailbox_share.go:178-195` (route `DELETE /mailboxes/:mbid/shares/:shareId`, registered at `mailbox_share.go:61`); repo delete at `panel-api/internal/repository/mailbox_share_repository.go:130-139`
- **Description:** `del` authenticates only `:mbid` via `loadMailbox` (discarding the result — `_, _, err :=`), then deletes by bare `c.Param("shareId")` with no `share.OwnerMailboxID == mbid` check. The sibling send-as delete does this correctly (`mailbox_sendas.go:198`, `DeleteByPair`), confirming this is an oversight.
- **Trigger/impact:** Any authenticated tenant who learns another tenant's share-row ULID (former share target, logs, leaked list response) can delete that row using their own mailbox as `:mbid`; the reconciler then strips the JMAP `shareWith` on Stalwart. Cross-tenant integrity/availability impact. Mitigating: ULIDs are 128-bit random and only exposed to the owner's tenant, so the ID must be obtained out-of-band. (Verified against source: `mailbox_share.go:181,186`.)
- **Suggested fix:** Keep the loaded mailbox and 404 unless `share.OwnerMailboxID == mb.ID`, or add `DeleteByOwner(ctx, shareID, ownerMailboxID)` mirroring `SendDelegations.DeleteByPair`.

#### ME-2: `RequireLocalhost` silently downgrades to "any loopback peer is trusted" on TCP binds
- **Area:** panel-api auth & middleware
- **Location:** `panel-api/internal/middleware/localhost.go:58-79`, `panel-api/internal/middleware/peercred.go:79-88`
- **Description:** The internal-route gate rejects proxied requests, requires SO_PEERCRED uid==0 for unix peers, then falls back to "RemoteAddr is loopback". On a unix socket this is airtight (`ConnContext` wired at `panel-api/cmd/server/serve.go:851`, socket 0660 root/jabali-sockets). But `peercred.go:82` treats "not a unix conn" as unknown → pass-through, and `localhost.go:70-80` then accepts any `127.0.0.1` peer. `server.addr` still accepts `host:port` (`config.go:368-385`), so a legacy/dev TCP loopback bind has no peer identity check at all.
- **Trigger/impact:** On any install binding `127.0.0.1:PORT` instead of the unix socket, every local process (tenant PHP-FPM pools, cron jobs, SSH shells) can POST unauthenticated to `/api/v1/internal/notifications/enqueue` (spoof/flood admin notifications, burying real alerts) and `/api/v1/admin/security/malware/event` (forge malware/quarantine rows pointing at arbitrary paths — `security_malware.go:86,629-758` even `os.Stat`s attacker-chosen paths). On a multi-tenant box, loopback is not a trust boundary.
- **Suggested fix:** When the listener is TCP, refuse to mount internal routes or require a shared-secret header read from a root-only file; at minimum log a loud startup warning.

#### ME-3: Finalizer 25-row window can permanently strand "running" jobs, leaking dispatcher slots
- **Area:** reconciler/jobs/backup/restore
- **Location:** `panel-api/internal/backupfinalizer/finalizer.go:92-119` + `panel-api/internal/repository/backup_job_repository.go:136-161` (`ListAll` = `ORDER BY created_at DESC LIMIT 25`), interacting with `panel-api/internal/backupscheduler/scheduler.go:179-184` (`CountByStatus(running)` gates dispatch)
- **Description:** The finalizer lists the 25 newest jobs of any status, then filters to `running` in memory. A long-running job that falls out of the newest-25 window (multi-hour home backup while a schedule fan-out creates 30+ newer rows) is never re-checked and never stall-failed. It stays `running` forever, permanently consuming one dispatcher concurrency slot (default max 2).
- **Trigger/impact:** One busy schedule tick during a long backup. Two stranded rows later, `slots <= 0` on every dispatch tick and all scheduled backups silently stop — no error surfaced, jobs just queue forever.
- **Suggested fix:** Query `WHERE status='running'` directly (add `ListRunning` beside `CountByStatus`) instead of filtering `ListAll` in memory, and apply the stall timeout in that query path.

#### ME-4: support-claim rate limit keyed on spoofable X-Forwarded-For; claim store and bucket map unbounded
- **Area:** SSO/tokens/crypto + supporting services (merged from two reviewers; both rated the core issue Medium)
- **Location:** `support-claim/main.go:244-253` (`clientIP`), `:166` (limit check), `:86-111` (store, no entry cap); `support-claim/ratelimit.go:37-57` (bucket map, no eviction)
- **Description:** `clientIP()` trusts the first client-supplied `X-Forwarded-For` value from any direct peer with no trusted-proxy check; nothing in code or README enforces proxy-fronting. Three compounding flaws: (1) random XFF per request fully bypasses the 30/min per-IP issue limit on the open `POST /claims`; (2) `rateLimiter.bucket` never evicts — every distinct key is a permanent allocation; (3) issued claims sit in an in-memory map with no max-entries cap and 14-day TTL, each up to ~16 KiB.
- **Trigger/impact:** `for i in …; curl -H "X-Forwarded-For: $RANDOM…" -d '{16KB}' …/claims` — an unauthenticated remote attacker grows the heap without bound until OOM, taking down the claim service support depends on. A direct hit to `:8088` bypasses any proxy anyway.
- **Suggested fix:** Only honor XFF when the peer is loopback/a configured proxy (same pattern as `hostedsvc/realip.go`); add a hard cap on `len(s.claims)` in `store.put` (507/503 when full); evict idle buckets (sweep alongside the claim sweeper).

#### ME-5: Remote-API values written unvalidated into `hostname.env`, later `source`d by root scripts
- **Area:** install.sh / installer / install assets
- **Location:** `install.sh:1110-1113` (writer; verified); sourced at `install/hostname/jabali-hostname-heartbeat.sh:11`, `install/hostname/certbot-auth-hook.sh:13`, `install/hostname/certbot-cleanup-hook.sh:10`, `install/hostname/jabali-hostname-cert.sh:18`; TUI writer `installer/internal/tui/freehostname.go:135-142`; agent writer `panel-agent/internal/commands/hostname_free_apply.go:65`
- **Description:** The JAB-213 free-hostname flow takes `label`/`fqdn`/`token` from the `api.jabalihosted.com` `/v1/claim` response and writes them verbatim (`printf 'TOKEN=%s\n' …`) into `/etc/jabali-panel/hostname.env`. Only `fqdn` is regex-validated (`install.sh:1232`); `TOKEN`, `LABEL`, and TUI-written `EMAIL` are not. Four root-executed scripts later `source` the file (daily heartbeat timer runs as root; certbot hooks re-run as root at every renew). A token containing `$(…)` or backticks executes as root at source time. The agent-side writer validates only `"\n\r "` exclusion, which still permits `$(…)`/backticks.
- **Trigger/impact:** Compromise of (or malicious response from) the hosted hostname service — or TLS MITM of `JH_API` — returns a crafted token; the next daily heartbeat or certbot renewal executes it as root on every free-hostname panel. Persistent: the file is re-sourced forever.
- **Suggested fix:** Validate every field at every writer (token/label `^[A-Za-z0-9_-]+$`); better, stop `source`-ing — parse with `val="$(grep '^TOKEN=' "$ENV" | cut -d= -f2-)"` so values are never executed.

#### ME-6: Adminer downloaded with no integrity check, served with DB-admin SSO
- **Area:** install.sh / installer / install assets
- **Location:** `install.sh:7147` (`adminer.php`), `install.sh:7158` (`plugin.php`)
- **Description:** Two PHP files fetched from `github.com/vrana/adminer` with plain `curl -fsSL`, no SHA-256 verification, then chowned www-data and served on the panel vhost behind jabali SSO. Every other third-party artifact in the same file (wp-cli, phpMyAdmin, Stalwart, Kratos, Bulwark, maldet, yara-x) is checksum-pinned against `install/`; `install/adminer/` has no `.sha256`, and `scripts/check-pinned-downloads.sh:74` only HEAD-checks that the URL exists.
- **Trigger/impact:** Takeover/tampering of the upstream repo or release asset ships arbitrary PHP running on the panel vhost with authenticated full database access (Adminer SSO). Silent: nothing in install or CI would detect the swap.
- **Suggested fix:** Pin `install/adminer.sha256` (and one for plugin.php) and `_die` on mismatch, mirroring `install_wp_cli`.

#### ME-7: Go toolchain downloaded unverified, with a silent version-downgrade fallback
- **Area:** install.sh / installer / install assets
- **Location:** `install.sh:4790-4818`
- **Description:** The Go tarball is fetched from go.dev with no checksum/signature verification (go.dev publishes SHA-256 per release). On any download failure of the pinned version, the fallback loop (4808-4815) walks the "published stable" list and installs the first older version that downloads — an unattended compiler downgrade that then builds the panel/agent binaries.
- **Trigger/impact:** TLS MITM of go.dev (or poisoned `?mode=json` response) delivers a malicious toolchain; the downgrade loop means simply making the pinned URL fail rolls the host back to an older Go with known CVEs. Deviates from the repo's pin-everything discipline.
- **Suggested fix:** Pin the SHA-256 in the repo and verify before `tar -C /usr/local`; in the fallback path verify against the checksum fetched from go.dev and log the downgrade loudly.

#### ME-8: Composer installer piped to root PHP with no signature check
- **Area:** install.sh / installer / install assets
- **Location:** `install.sh:1827-1836`
- **Description:** `curl -fsSL https://getcomposer.org/installer` executed by `php8.x` as root. Composer's own docs prescribe verifying the installer's SHA-384 against `getcomposer.org/installer.sig` first; this code does not.
- **Trigger/impact:** MITM or compromise of getcomposer.org yields immediate root code execution during install/update.
- **Suggested fix:** Fetch `installer.sig` and verify SHA-384 before executing (documented 4-line pattern), or pin an expected hash in `install/`.

#### ME-9: hostedsvc claim creates the label row before publishing DNS; DNS failure permanently burns the label
- **Area:** supporting services (hostedsvc)
- **Location:** `hostedsvc/api.go:160-175` (`Store.CreateLabel` → then `DNS.EnsureA`)
- **Description:** On `EnsureA` failure (Cloudflare/PDNS outage), the handler returns 500 *after* the label row + token hash were committed. The caller never receives the token, so the row is orphaned: it still counts in `LabelExists` (burning one of ≤26 collision suffixes for that source IP) and in the 10-labels-per-email cap, and is invisible to the reaper (`moved_at` NULL, heartbeats impossible). No test covers this path.
- **Trigger/impact:** Sustained DNS-backend errors during claims permanently exhaust an IP's label space (`label_space_exhausted` on every later attempt) and silently eat the user's email cap. Wrong-state convergence requiring manual DB cleanup.
- **Suggested fix:** Publish DNS first, then `CreateLabel`, rolling back with `RemoveLabel` if the insert fails (`ipMu` already serializes the check-then-act). Note `RevokeLabel` is not a substitute — revoked names are burned by design.

#### ME-10: hostedsvc `/v1/register` has no per-IP or global throttle — email-bomb / relay-abuse vector
- **Area:** supporting services (hostedsvc)
- **Location:** `hostedsvc/api.go:68-99`, `hostedsvc/store.go:102-118` (only a 60s per-email resend gap)
- **Description:** The only throttle on registration is a per-*email* resend gap. Nothing limits requests per source IP or overall.
- **Trigger/impact:** (a) Target one victim address with a verification email every 60s indefinitely — sent through the service's authenticated, reputation-clean relay; (b) fan out to millions of distinct addresses, using the service as a spam relay. Harassment mail the victim can't easily block plus relay reputation damage / provider abuse action.
- **Suggested fix:** Per-source-IP bucket (e.g. 10/hour, keying off the already-trusted `ClientIP(r)`) plus a daily cap per email address; consider turnstile/PoW on the endpoint.

### Low

#### LO-1: Cron `job_id` never validated before use in root-side file paths
- **Area:** panel-agent privileged handlers — **verified not reachable with current callers** (all panel-api callers pass DB-minted ULIDs; defense-in-depth only)
- **Location:** `panel-agent/internal/commands/cron_apply.go:57-62` (only non-empty check), used at `:175-176` and `:471-472`; `cron_remove.go:150-157`
- **Description:** `JobID` is embedded into root-written/root-deleted paths with no format validation. A `../ssh` value would make apply clobber `/etc/systemd/system/ssh.service`, and remove/cleanup unlink arbitrary `*.service`/`*.timer` files as root. Becomes live the moment any future caller (import, API extension, admin form) lets a client-influenced string reach `job_id`.
- **Suggested fix:** Validate `JobID` at handler entry in both files (`^[0-9A-HJKMNP-TV-Z]{26}$`, same shape as `privacyULIDRE` in `dirprivacy.go`) before any path is built.

#### LO-2: `db.restore` deletes the dump file via raw `os.Remove`, outside the scope that validated it
- **Area:** panel-agent privileged handlers — **not reachable with current callers** (panel-api passes a server-generated ULID path; would additionally need a parent-dir swap race + guessable name)
- **Location:** `panel-agent/internal/commands/db_restore.go:119,127`
- **Description:** The restore file is opened through a symlink-safe `filesafe.Scope` (Gitea #501 fix), but both cleanup paths call plain `os.Remove(p.Path)` on the unvalidated string; the scope's RESOLVE_BENEATH guarantees don't cover the unlink. Practical ceiling: root unlinking a same-named file in an attacker-chosen directory.
- **Suggested fix:** Delete through the scope (`restoreScope.RemoveInScope`) or `unlinkat` from the already-open parent fd.

#### LO-3: `fs.write_healthcheck` — unused registered command that writes/chowns any absolute path as root
- **Area:** panel-agent privileged handlers — **no caller found anywhere** (panel-api, panel-agent, installer); standing dead-code primitive
- **Location:** `panel-agent/internal/commands/fs_write_healthcheck.go:56-86`
- **Description:** Only validation is "path is absolute" + "user_group contains a colon"; then `os.WriteFile` fixed PHP content as root and shells `chown`. Stat-then-write is racy; a dangling symlink is followed by both write and chown. Registered on the root agent socket, it is a standing root arbitrary-file-create + arbitrary-chown primitive should any future caller pass an unvetted path, and bypasses the filesafe pattern every sibling `files_*` handler uses.
- **Suggested fix:** Delete the handler, or scope it (docroot-prefix via filesafe, `user_group` validated against owning tenant, O_NOFOLLOW create).

#### LO-4: Automation HMAC replay defense silently absent when Redis is nil
- **Area:** panel-api auth & middleware
- **Location:** `panel-api/internal/middleware/automation_hmac.go:75-79` (comment promises an info log) vs `:196-216` (no such log); mount site `panel-api/internal/app/app.go:402-433`
- **Description:** Doc comment says a nil `rdb` downgrades replay defense "plus a single info log fires at first request" — no such log exists. `app.go` mounts the automation API whenever `AutomationTokens != nil && SSOKey != nil`, passing `deps.Redis` straight through with no nil guard, despite the adjacent comment calling the replay gate "Required in production."
- **Trigger/impact:** On a Redis-less panel, anyone capturing a valid signed automation request can replay it freely within the ~5.5-minute window (e.g. re-fire `POST /automation/users/:id/disable`). The operator gets no signal the gate is off.
- **Suggested fix:** Startup WARN when automation API mounts with nil Redis, or refuse to mount write routes in that mode.

#### LO-5: `clientIP()` trusts client-supplied `X-Real-IP`/`X-Forwarded-For` off-nginx
- **Area:** panel-api auth & middleware — safe in the production topology (nginx overwrites `X-Real-IP` per `install/nginx/jabali-panel-vhost.conf.tmpl:129-133,180-185,203-208`); affects direct-bind deployments only
- **Location:** `panel-api/internal/middleware/audit.go:132-145`; consumers `ratelimit.go:103,120`, `automation_hmac.go:131`, `auth_user_api_token.go:191-207`
- **Description:** `clientIP` prefers `X-Real-IP`, then first XFF hop, with no check of who set them; the existing peer-cred machinery (uid www-data) isn't consulted.
- **Trigger/impact:** On direct TCP binds, rotating `X-Real-IP` bypasses the per-IP `KratosFlows` login/recovery rate limit (unrestricted online brute force), bypasses API rate tiers, defeats the automation-token IP allowlist with a stolen token, and poisons audit rows with arbitrary IPs.
- **Suggested fix:** Honor forwarding headers only when the peer is verified as the proxy (unix peer uid==www-data via existing `PeerUID`); otherwise key on `RemoteAddr`.

#### LO-6: Agent/DB error strings echoed to clients, bypassing the JAB-114 leak guard
- **Area:** panel-api REST handlers
- **Location:** `panel-api/internal/api/users.go:~494,~869` (`"error":"agent_error"` + `"detail": agentErr.Error()`), `domain_path_browse.go:94`, `backups.go:805,2581`, `docker_apps.go:592`, `docker_apps_user.go:561,583`, `files.go:717,732,1108,1148,1167`
- **Description:** The JAB-114 convention (and `agent_error_leak_test.go`) require agent/DB internals be logged, never echoed — but the gate is a single-line regex over four literal error codes, so multi-line responses or differently-named codes slip through. Agent errors carry root-daemon stderr and filesystem paths; GORM errors carry schema/SQL detail.
- **Trigger/impact:** `users.go:494` is reachable by a non-admin owner via `PATCH /users/:id` — a tenant changing their password while the agent is failing receives the root agent's raw error text. Others are admin/owner-scoped info leaks.
- **Suggested fix:** Route through `respondAgentErr`/`respondAgentErrStatus`; strengthen the gate (match code + `.Error()` within the same `c.JSON` block, or lint `"detail":` + `.Error()` on 5xx/agent paths).

#### LO-7: Domain browse endpoint returns server-absolute path to clients
- **Area:** panel-api REST handlers
- **Location:** `panel-api/internal/api/domain_path_browse.go:53,111` (`AbsPath ... // server-side absolute (for debugging)`, returned in every 200)
- **Description:** The docroot browse endpoint includes the absolute host path of the domain directory in its wire response. Auth is correct (owner-or-admin) and containment is well done (`normaliseBrowseRel` + `filepath.Rel` escape check at `:71-78`) — this is info disclosure to the legitimate owner, not traversal.
- **Trigger/impact:** Tenant (or anyone reading their browser logs) learns `/home/<linux-user>/…`, disclosing the Linux username — useful pre-recon for SSH/password attacks against that account.
- **Suggested fix:** Drop `abs_path` from the response struct (keep it in server-side logs if needed).

#### LO-8: `backup.restore` / `backup.restore_selective` never validate `target_username`
- **Area:** reconciler/jobs/backup/restore — **verified not reachable from an untrusted position today** (tenant selective restore server-derives the username, admin restore uses the DB username, CLI DR is root-operated); inconsistent with the agent's own policy everywhere else (`backup_create.go:138`, `backup_home.go:89`, `backup_databases.go:60` all use `backupUsernameRE`)
- **Location:** `panel-agent/internal/commands/backup_restore.go:102-128`, used unvalidated at `:271-283` (`useradd` argv, `--home-dir` join) and `:328-329` (rsync `dst`); `backup_restore_selective.go:76-78`
- **Description:** The wire value flows into path joins (no containment check on the rsync destination) and `useradd`/`loginctl` argv, where a leading `-` parses as flags. A `../etc`-style username would turn root rsync `--delete` into arbitrary-directory mirror+delete. Any future panel-api bug letting a caller influence `target_username` becomes instant root file-write.
- **Suggested fix:** Add `backupUsernameRE.MatchString(req.TargetUsername)` in both handlers next to the existing `ulidRE` check.

#### LO-9: Retention prune deletes DB rows even when the agent-side forget failed
- **Area:** reconciler/jobs/backup/restore
- **Location:** `panel-api/internal/backupscheduler/scheduler.go:449-470` (`pruneOldestForUser`)
- **Description:** The loop calls `backup.forget` on the agent, ignores the result (`_, _ =`), then deletes the `backup_jobs` row regardless. On transient repo failure the snapshot stays in the repo but its row is gone — the repo grows unboundedly while the panel's retention accounting says it's under the cap.
- **Trigger/impact:** Destination offline during a scheduled prune → orphaned snapshot data accumulates invisibly; repo size and panel state diverge permanently.
- **Suggested fix:** Delete the row only when forget succeeds (or queue a retry); at minimum log the forget error with snapshot IDs.

#### LO-10: Admin-sentinel (`__M46_ADMIN_ALL__`) SSO tokens get no validation-time privilege re-check
- **Area:** SSO/tokens/crypto
- **Location:** `panel-api/internal/api/sso_phpmyadmin_validate.go:116-133`, `sso_adminer_validate.go:97-113`
- **Description:** The M46 admin branch redeems the token straight into root-equivalent DB credentials without loading the user row — unlike the per-user path, which re-checks suspension and ownership (JAB-8). Suspension of admins is refused, but demotion is the sanctioned flow (`users_suspend.go:58`), and a demoted admin's in-flight admin token remains valid.
- **Trigger/impact:** Admin clicks "phpMyAdmin (all DBs)" and is demoted within the 5-minute TTL; the already-minted single-use token still redeems to `jabali_pma_admin` / postgres superuser. Narrow window + single-use, hence Low.
- **Suggested fix:** In the admin branch, load `token.UserID` and require a non-suspended user that still has admin rights before resolving the privileged credential.

#### LO-11: Mint-side audit `token_hash_prefix` hashes the base64 string, not the raw token
- **Area:** SSO/tokens/crypto
- **Location:** `panel-api/internal/api/sso_phpmyadmin.go:117-118`, `sso_adminer.go:123-124`
- **Description:** `MintToken` stores `sha256(rawTokenBytes)` and validate handlers log prefixes of that hash; the mint handlers compute `sha256.Sum256([]byte(token))` over the base64url *string* — a different digest — and log 8 bytes vs validate's 4. "Issued" and "validated"/"unauthorized" log lines for the same token never share a prefix.
- **Trigger/impact:** Not exploitable, but the SSO audit chain (issue → validate) is silently broken during incident forensics — exactly when these logs matter.
- **Suggested fix:** Base64url-decode before hashing (or return the hash from `MintToken`); use the same prefix length on both sides.

#### LO-12: Live SSO tokens and impersonation JWTs carried in URL query strings
- **Area:** SSO/tokens/crypto — partially contract-bound (webmail `?token=` is Bulwark's required contract); mitigations in place: 5-min TTL, single-use atomic consume, 60s webmail JWT
- **Location:** `panel-api/internal/api/sso_phpmyadmin.go:126`, `sso_adminer.go:133`, `databases_admin_ops.go:461,666`, `webmail_sso.go:193`
- **Description:** All SSO handoffs put the credential in `?token=…`, so unconsumed tokens land in nginx access logs, browser history, and Referer leakage. The project chose the self-deleting-file pattern for WP SSO (ADR-0040) precisely to avoid this class.
- **Trigger/impact:** Anyone with read access to panel/mail vhost access logs (`adm` group, log-shipping pipeline) can redeem a not-yet-consumed token within its TTL window, racing the legitimate user.
- **Suggested fix:** Accept as documented tradeoff where contract-bound (Bulwark); exclude `token` from nginx access-log formats (filtered `$args`) on phpMyAdmin/Adminer/mail vhosts.

#### LO-13: Cloudflare real-IP ranges written unvalidated into nginx config, installed before `nginx -t`
- **Area:** install.sh / installer / install assets
- **Location:** `install.sh:13917-13958`
- **Description:** Bodies of `https://www.cloudflare.com/ips-v4|ips-v6` are written line-by-line as `set_real_ip_from <line>;` with no CIDR-format validation, and installed into `/etc/nginx/conf.d/` *before* `nginx -t` runs — on test failure the bad file stays in place with only a `_warn`.
- **Trigger/impact:** Any 200-response garbage (captive portal, TLS-intercepting middlebox, CF error page) persists a config that breaks the next nginx start; a crafted line containing `;` could inject http-context directives (e.g. swapping `real_ip_header` to `X-Forwarded-For`, neutering IP-based blocking).
- **Suggested fix:** Filter lines through a strict CIDR regex before writing; validate the staged config before atomically installing.

#### LO-14: Fixed-name root tempfiles in world-writable `/tmp`
- **Area:** install.sh / installer / install assets — largely neutralized by default `fs.protected_symlinks=1`/`protected_hardlinks=1`, but that's a kernel sysctl the installer never asserts
- **Location:** `install.sh:12420, 12556, 13175, 13219, 7353, 4788, 12623`
- **Description:** Several root downloads/extracts use predictable `/tmp` paths instead of the `mktemp` discipline used elsewhere in the same file. Contents are sha256-verified, so content-swap fails; residual risk is symlink/pre-creation clobbering by a local unprivileged user.
- **Suggested fix:** Switch to `mktemp`/`mktemp -d` like the rest of install.sh.

#### LO-15: fsperm recursive walk holds one fd per depth level — tenant-deep trees break root perm repair
- **Area:** supporting services (internal/fsperm)
- **Location:** `internal/fsperm/fsperm.go:73-110` (`walkApply` recurses with `cfd` open; callers' fds also stay open)
- **Description:** `walkApply` opens each child dir with `openat` and recurses before closing, so fd usage equals tree depth. A tenant can create a tree deeper than `RLIMIT_NOFILE` (default 1024 soft) via `chdir`+`mkdir` loops that bypass `PATH_MAX`. `GroupSetgidTree`/`RepairDocrootGroup` then fail with EMFILE mid-walk.
- **Trigger/impact:** Tenant plants a >1024-deep tree under their docroot; the next root-side restore/fix-perms (`backup_restore.go:605`, `domain_fix_perms_cmd.go:70`, `repair.go:1269`) errors out, leaving docroot group/setgid unrepaired (www-data 403s). Availability edge only — the fd-descent design itself is sound.
- **Suggested fix:** Raise `LimitNOFILE` for the agent and/or make the walk iterative with a depth cap that reports a clear error.

#### LO-16: hostedsvc ACME present lets a token holder accumulate unbounded TXT records
- **Area:** supporting services (hostedsvc)
- **Location:** `hostedsvc/api.go:245-266`, `hostedsvc/cfdns.go:166-191`
- **Description:** `SetChallenge` is add-only (by design, for dual wildcard challenges) and `/v1/acme/present` has no per-label record cap — each distinct 1–255-byte value creates another TXT record at `_acme-challenge.<label>`. Cleanup/reap removes them eventually, but nothing bounds accumulation meanwhile.
- **Trigger/impact:** A valid token holder scripts distinct TXT values → thousands of records in the shared `jabalihosted.com` zone, degrading zone size/API list performance for everyone. Requires a claimed label (email-verified), hence Low.
- **Suggested fix:** Cap challenge records per label (e.g. refuse when ≥8 exist) in `SetChallenge`.

## Areas with no high-confidence findings

- **panel-ui frontend (`panel-ui/src/`)** — checked XSS sinks (zero `dangerouslySetInnerHTML`/`innerHTML`/`eval`; file preview escapes text and the backend inlines only raster/PDF with `nosniff`+CSP), token storage (pure cookie auth; localStorage holds only prefs), open redirects (login return-to guarded), popup/href flows (same-origin server-issued SSO URLs, noopener), i18n interpolation, and JSON.parse guards. One defense-in-depth note below threshold: the main SPA document has no Content-Security-Policy header.
- Everything else had at least one finding; within those areas the reviewers additionally verified clean: filesafe openat2 scoping and symlink-safe restore/chown paths; Kratos session auth, HMAC core, user API tokens, CSRF/CORS, impersonation, route mounting; ownership checks across the main user-scope route families; `applyListOptions` sort/pagination handling; ssokey AES-GCM and token entropy/single-use; SFTP credential handling; reload-storm gating; hostedsvc authz/real-IP/label model; agentwire (pure type definitions); appseccfg, kratosclient, dkim, dnsverify, limits, ops/cf-update-bridge; hostname env-var regex validation, secret-file permissions, and apt-key pinning (Sury/Launchpad) in the installer.

## Methodology & limitations

- Read-only static review; no dynamic testing, no exploit confirmation on a live box, no dependency/CVE scan. HI-1..HI-3 in particular deserve one live rotated-destination round-trip per the project's "verify on a box" rule.
- All Critical/High findings were re-verified against source at commit `fda13d89` during consolidation (line citations corrected where drifted); Medium/Low findings received plausibility spot-checks only.
- Findings a reviewer itself flagged as speculative non-issues or already-mitigated were dropped; overlaps between areas were merged (notably the support-claim rate-limit findings from two reviewers, kept at the higher severity).
- Low findings marked "not reachable today" are defense-in-depth gaps at privilege boundaries, included because the agent/handler layer is the last line of validation.

## Remediation status (updated 2026-08-08)

Every High and Medium finding below has been fixed and merged to `main`; the
material Lows went with them. Each entry names the PR that closed it, so this
document stays a record of what was found AND what was done, rather than a
list a future reader has to re-triage.

| Finding | Status | PR |
|---------|--------|-----|
| HI-1/HI-2/HI-3 per-destination restic password | fixed | #982 |
| ME-1 mailbox-share delete IDOR | fixed | #985 |
| ME-3 finalizer 25-row window (queue wedge) | fixed | #983 |
| ME-4 support-claim XFF + unbounded maps | fixed | #989 |
| ME-5 hostname.env sourced by root scripts | fixed | #986 |
| ME-6 Adminer unpinned | fixed | #988 |
| ME-7 Go toolchain unpinned + silent downgrade | fixed | #988 |
| ME-8 Composer installer unverified | fixed | #988 |
| ME-9 claim burns a label on DNS failure | fixed | #989 |
| ME-10 /v1/register has no per-IP throttle | fixed | #989 |
| LO-7 abs_path disclosure | fixed | #985 |
| LO-8 target_username unvalidated | fixed | #985 |
| LO-9 prune deletes rows on failed forget | fixed | #983 |
| LO-16 unbounded ACME TXT records | fixed | #989 |
| ME-2, LO-1..LO-6, LO-10..LO-15 | open | — |

Corrections to the review, found while fixing:

- **HI-1 was incomplete.** `bkEnsureRepoReady` used the legacy password for the
  repo INIT as well as the probe, and `system.backup` had no password plumbing
  at all (no `password_file` field, and it built its restic config without one).
  Five breaks on that value, not three.
- **ME-5 is arguably High**, not Medium: remote-to-root on the whole
  free-hostname fleet, persistent across reboots, with no operator-visible
  signal.
- **ME-3 is High, not Medium** — see the optimization review's HI-3, whose
  causal analysis is sharper: the running jobs are *guaranteed* outside the
  page, not merely likely.
- **ME-9 and ME-13 (optimization) conflict.** Fixing ME-9 the obvious way
  (publish DNS before the insert) forces DNS back inside the global mutex and
  undoes ME-13. Reserving the row first and rolling it back on DNS failure
  satisfies both.

Still unverified on a box: the #982 backup path deserves one live
rotated-destination round-trip (backup → finalize → download → restore).
Static review cannot confirm a data-loss fix.
