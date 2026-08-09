# Code Review — Optimization & Performance Findings (2026-08-08)

- **Scope:** read-only static review of the Jabali Panel codebase by 8 parallel area reviewers: (1) panel-api GORM repositories/models, (2) reconciler + event sources, (3) panel-api HTTP handlers, (4) panel-agent command handlers, (5) backup scheduler/finalizer/jobs, (6) panel-ui frontend, (7) notifications/mailscan/bandwidth/log processing, (8) supporting services + shared libs (hostedsvc, support-claim, ops, internal/, agentwire).
- **Commit reviewed:** `735d7445` — "chore(webmail): update Bulwark checksum for 1.8.0 (GH #646)"
- **Method:** static analysis only (code reading + call-path tracing). Findings de-duplicated across areas; all High findings and a sample of Mediums were spot-checked against the source during consolidation.

## Summary

| Severity | Count |
|----------|-------|
| High     | 4     |
| Medium   | 14    |
| Low      | 15    |

Dominant themes:

- **Ungated per-tick / per-poll side effects.** The two worst offenders are a full PowerDNS zone rewrite + cache purge + NOTIFY per domain every 60s (HI-1) and a full `nginx -t` on every 5s dashboard poll (HI-2). Both are exactly the HANDOFF §9 "gate side effects behind a no-change compare" pattern, missing.
- **Poll-driven fan-out from idle browser tabs.** Server-status polling (5s/30s, two independent timers), the GoAccess modal (10s full-log re-parse, HI-4), and the log-stream modal's unbounded buffer (ME-12) all burn box CPU in proportion to how many admin tabs are open, not how much work exists.
- **N+1 residue where the batch method doesn't exist yet.** The hot list paths are already batched; what remains is per-row queries in secondary paths: docker-app ports (ME-1), backup enqueue/dispatch (ME-9, ME-10, LO-7), PHP pools (ME-7), per-domain reconciler fetches (ME-5), and per-grant mailbox-share lookups (ME-14).
- **Work repeated on data that changes rarely.** Bandwidth-quota enforcement recomputes daily-granularity sums every 60s (LO-11); mailscan recompiles the full YARA ruleset per attachment (ME-11); the singleton settings row is re-SELECTed ~25× per tick (ME-6).
- **One queue-deadlock bug hiding in a paging shortcut.** The backup finalizer pages "latest 25 jobs of any status" and filters running in Go; with >25 fanned-out jobs the running set falls out of the page and the whole backup queue wedges (HI-3).

## Findings

### High

#### HI-1: DNS zone push is ungated — full pdns rewrite + cache purge + NOTIFY per zone every 60s
- **Area:** reconciler + event sources
- **Location:** `panel-api/internal/reconciler/reconciler.go:2144` (unconditional `zone.Serial = time.Now().UTC().Unix()` + `dnsZones.Update`), `:2160-2167` (`dns.zone.upsert` per domain per tick); agent side `panel-agent/internal/pdns/client.go:137-155` (DELETE + re-INSERT every record) and `panel-agent/internal/commands/dns_zone_upsert.go:102-108` (`pdns_control purge`, `rec_control wipe-cache`, `pdns_control notify`).
- **Description:** `reconcileDNSZone` runs for every enabled domain every tick. It bumps the SOA serial unconditionally and UPDATEs the `dns_zones` row even when nothing changed, then pushes the full zone to the agent. The agent wipes and re-inserts every record row in PowerDNS's SQL backend, then shells out 3× (purge auth cache, wipe recursor cache, NOTIFY). There is no content compare anywhere in the path; because the serial changes every tick, the payload can never converge, so no downstream gate could ever kick in. Spot-checked: confirmed.
- **Trigger/scale:** D zones × once per 60s, forever. Each zone: 1 panel-DB UPDATE + ~R row DELETE/INSERTs in pdns's MariaDB + 3 subprocesses. With `ns2_ipv4` set, slaves also get NOTIFY/transfer churn every minute; `rec_control wipe-cache <zone>$` means recursor entries for hosted zones never live past one tick.
- **Suggested fix:** hash the compiled record set (excluding SOA serial), store it per zone, skip the UPDATE + `dns.zone.upsert` RPC when unchanged; bump the serial only on real content change. Gate the agent-side purge/notify on a real write.

#### HI-2: `/admin/server-status` runs a full `nginx -t` on every 5s poll (and 30s header poll)
- **Area:** panel-api HTTP handlers / panel-agent command handlers / panel-ui (merged from 3 area reports)
- **Location:** `panel-api/internal/api/server_status.go:152-156` (`call("nginx", "nginx.test", ...)`; the code's own comment states 15–30s on many-vhost/AppSec hosts); executor `panel-agent/internal/commands/nginx.go:43-55` (unsynchronized, uncached `exec nginx -t`); UI: `panel-ui/src/hooks/useServerStatus.ts:163` (`refetchInterval: 5000`, mounted ungated by `ServerStatusPage.tsx:29`, `Dashboard.tsx:62`, `AuditEventDetail.tsx:51`), plus `ServerHealthIndicator.tsx:38-64` (raw `setInterval` every 30s, bypasses the TanStack cache — see ME-2).
- **Description:** The status aggregator fans out 8 agent sub-calls per request, one of which is `nginx.test` → `nginx -t`, which parses every vhost plus the full CrowdSec AppSec/CRS rule set (~19s at >60% CPU on AppSec hosts per `nginx_status.go:9-17`, which introduced the cheap `nginx.status` verb specifically for this poller — the aggregator never switched). The 45s `nginxTestTimeout` exceeds the 5s poll interval and the agent serves each connection on its own goroutine, so slow `nginx -t` runs overlap and stack. Spot-checked: confirmed.
- **Trigger/scale:** any admin sitting on the Dashboard / Server Status / Audit detail page; 12 polls/min × a 15–30s `nginx -t` → permanently saturated CPU core(s) on exactly the hosts (many vhosts + CRS) that can least afford it. Even the idle 30s header indicator alone keeps ~60% of a core busy on such hosts.
- **Suggested fix:** point the `nginx` slot at `nginx.status` (the verb built for this), or wrap the `nginx.test` result in a 30–60s TTL cache (the pattern `system.software` already uses, `system_software.go:75-105`). Reduce the poll cadence and/or drop `nginx.test` from the per-poll envelope.

#### HI-3: Backup finalizer pages "latest 25 jobs of any status" instead of querying running jobs → backup queue wedges permanently
- **Area:** backup scheduler/finalizer/jobs
- **Location:** `panel-api/internal/backupfinalizer/finalizer.go:93` (`Jobs.ListAll(ctx, MaxJobsPerTick=25, 0)`, tick `30s` at `:31-33`), ordering at `panel-api/internal/repository/backup_job_repository.go:155` (`ORDER BY created_at DESC`, plus a full-table `COUNT(*)` at `:149`); dispatcher gating at `panel-api/internal/backupscheduler/scheduler.go:179-188`.
- **Description:** The finalizer tick fetches the 25 *newest* `backup_jobs` rows across all statuses and filters `status='running'` in Go. In a scheduled full-server fan-out with >25 jobs (users × destinations — e.g. 13 tenants × 2 destinations), the dispatcher fills slots via `ListQueuedOldest` (created_at ASC), so the running jobs are the *oldest-created* of the batch and immediately fall outside the finalizer's page. They can then neither be finalized nor hit the 4h stall timeout: they stay `running` forever, `CountByStatus(running)` stays at the cap, `slots <= 0`, and the entire backup queue deadlocks until manual DB intervention. Spot-checked: query shape, ordering, tick interval, and slot logic all confirmed.
- **Trigger/scale:** one schedule firing with >25 fanned-out jobs — a normal configuration. First occurrence permanently stalls all subsequent scheduled backups and leaves phantom "running" jobs in the UI.
- **Suggested fix:** add `ListRunning(ctx, limit)` (`WHERE status='running' ORDER BY started_at` — `idx_backup_jobs_status` exists) and use it in `tickOnce` instead of `ListAll`. Fixes both the wedge and the wasted full-table count per 30s tick.

#### HI-4: GoAccess modal re-parses the entire access log every 10s per open modal
- **Area:** notifications/mailscan/bandwidth/log processing (+ panel-ui trigger)
- **Location:** `panel-api/internal/api/websocket_logs.go:269-277` (`runGoAccess` — fresh `goaccess <full-access.log> -o html` subprocess, no caching, no incremental mode) served by `:300` (`renderGoAccessHTTP`, `Cache-Control: no-store`); triggered by `panel-ui/src/shells/admin/logs/LogStreamModal.tsx:92-104` (iframe cache-bust every 10s).
- **Description:** While the GoAccess modal is open, each 10s tick runs a full goaccess pass (15s timeout) over the whole current-day access log — O(log size) CPU plus a multi-MB HTML payload per response. No `--persist`/`--restore`, no mtime-keyed cache. Spot-checked: confirmed.
- **Trigger/scale:** open modal × 6 renders/min × a day's access log (100s of MB on a busy domain; the server-wide variant reads `/var/log/nginx/access.log`). Several admins, or user+admin surfaces together, multiply linearly. Sustained double-digit CPU from one idle browser tab on a busy host.
- **Suggested fix:** cache rendered HTML keyed by (log path, mtime, size) with TTL ≥ poll interval, and/or run goaccess with `--persist --restore` against an on-disk DB so re-renders only parse new lines. Dropping the UI cadence to 30–60s is a free extra win.

### Medium

#### ME-1: Docker Apps list — per-app port query + full unbounded domain load with heavy TEXT columns
- **Area:** panel-api HTTP handlers / GORM repositories (merged)
- **Location:** `panel-api/internal/api/docker_apps.go:302` (`Domains.List(ctx, repository.ListOptions{})` — no LIMIT, `OmitHeavyColumns` unset) and `:305-306` (per-app `ListPortsForApp` + O(apps × domains) in-memory scan); repeated at `:392, :808, :979, :1140, :1315`. No batch repo method (`repository/docker_app_repository.go:250`).
- **Description:** Each list/install/update call issues one port query per installed app (textbook N+1) and reads the entire domains table including wide TEXT columns (DKIM material, disclaimer, nginx safe-options — the exact columns `domains.go:330-332` documents skipping) just to match `DockerAppID`. Spot-checked: confirmed.
- **Trigger/scale:** every Docker Apps page load / install / update; dozens of extra round-trips + a full domains-table read per request on a real host.
- **Suggested fix:** add `ListPortsForApps(ctx, ids)` with `WHERE app_id IN ?` and group in Go; replace the domain scan with `SELECT id, name, docker_app_id FROM domains WHERE docker_app_id IS NOT NULL` (or at minimum `OmitHeavyColumns: true` + a limit).

#### ME-2: ServerHealthIndicator polls the full aggregator every 30s from every admin page, bypassing the query cache
- **Area:** panel-ui frontend
- **Location:** `panel-ui/src/components/ServerHealthIndicator.tsx:38-64` (mounted on all admin pages via `JabaliHeader.tsx:450`).
- **Description:** The header badge needs only `alerts`, but calls the full `/admin/server-status` envelope (HI-2's 8-way fan-out, `nginx -t` included) via raw `apiClient.get` on a hand-rolled `setInterval` — no shared TanStack cache (stacks with the page's own 5s poll), no tab-visibility pause, no in-flight guard. Spot-checked: confirmed.
- **Trigger/scale:** every logged-in admin, every 30s, forever — including background tabs left open overnight.
- **Suggested fix:** expose a lightweight `/admin/server-status/alerts` endpoint (or server-side cache the envelope ~30s), and rewrite the indicator as a `useQuery` with `refetchInterval` + `select: (d) => d.alerts` so it shares the cache and pauses in background tabs.

#### ME-3: User mail tabs fan out per-domain `useQueries` (N+1 per page view)
- **Area:** panel-ui frontend
- **Location:** `panel-ui/src/shells/user/mail/tabs/MailboxesTab.tsx:76` + `:88` (2 queries per email-enabled domain: mailboxes + group-memberships), `ForwardersTab.tsx:59`, `CatchAllTab.tsx:55`, `SharedResourcesTab.tsx:66`.
- **Description:** Each tab fetches the domain list, then issues one HTTP request per email-enabled domain. MailboxesTab fires 2×N requests on mount — the per-parent N+1 shape CONVENTIONS bans for list handlers; the backend lacks a user-scope aggregate, so the UI compensates with fan-out. Spot-checked (MailboxesTab): confirmed.
- **Trigger/scale:** a tenant/reseller with D email domains pays ~D requests per tab visit (2D for Mailboxes); at D=50 that's 100 requests and up to 50×200 rows materialized client-side for one table. All requests re-fire together after any single mailbox mutation invalidates `["list","mailboxes"]`.
- **Suggested fix:** add user-scope aggregate endpoints (mirror `/admin/mailboxes` scoped to the caller's domains) and switch tabs to one paginated query each; keep per-domain endpoints for drawer/edit flows.

#### ME-4: Server-wide mailbox lists — no pagination, wide `m.*` projection (incl. password columns), COUNT-via-full-list
- **Area:** panel-api HTTP handlers / GORM repositories (merged)
- **Location:** `panel-api/internal/api/mailboxes.go:706-734` (`GET /admin/mailboxes` → `ListAllWithDomain`, envelope `{data,total}` only); `panel-api/internal/repository/mailbox_repository.go:126-136` (`SELECT m.*, ...` — includes `password_hash`/`password_enc`, unused by callers); `panel-api/internal/api/automation.go:215-235` and `:258` (`/automation/mail/summary` loads all rows just to `len()` them); `reconciler/mailbox_usage_ticker.go:60` (pulls wide rows, needs only `id` + `email_cached`).
- **Description:** The admin Mail tab and automation mail inventory load the entire joined mailboxes table into memory and serialize it in one response — no page/page_size, against the project's own list-envelope convention. The summary endpoint materializes full joined rows where a `COUNT(*)` would do. Spot-checked: confirmed.
- **Trigger/scale:** one full-table read + full serialization per admin Mail-tab visit or fleet-manager poll; fine at hundreds of mailboxes, multi-MB responses at tens of thousands.
- **Suggested fix:** add LIMIT/OFFSET pagination with the standard `{data,total,page,page_size}` envelope; a `CountAll()` for the summary; a slim `ListIDsWithEmail` projection for the ticker. Keep the wide variant for the admin UI only.

#### ME-5: Per-domain N+1 query fan-out in the DNS reconcile loop — same data re-fetched 4–6× per domain per tick
- **Area:** reconciler + event sources
- **Location:** `panel-api/internal/reconciler/reconciler.go:2131` (compile list), `:2998` (`migrateBootstrapShape`), `:2914` (`convergeApexAddrRecords`), `mail_provider_reconcile.go:39`, `panel_primary_dkim.go:234-249` (`ensureTenantDKIMRecords`); plus `reconciler.go:1591, 1809` in `createDomainOnAgent` (per-domain `users.FindByID`, `pageTemplates.Get` re-fetch of the identical row, `sslCerts.FindByDomainID`, `phpPools.FindByID` + `ListByUserID`, rule→credentials N+1 at `:1709`).
- **Description:** `dnsRecords.ListByZoneID` runs 4 separate times per domain per tick inside `reconcileDNSZone` alone, plus a 5th in `ensureTenantDKIMRecords` for email-enabled domains. The adjacent account skeleton got a 30s cache; these didn't.
- **Trigger/scale:** D domains × 60s. At D=100 that's ~500+ redundant indexed SELECTs per minute, growing with records-per-zone.
- **Suggested fix:** list zone records once per domain per tick and pass the slice to the migrate/apex/mail-provider helpers; hoist `users` (map by ID), `pageTemplates.Get`, and `serverSettings` to once per tick.

#### ME-6: `serverSettings.Get` is uncached and called from ~25 reconciler sites, several per-domain
- **Area:** reconciler + event sources / GORM repositories
- **Location:** `panel-api/internal/repository/server_settings_repository.go:61-70` (full-row `First(&s, 1)` per call); per-domain call sites `reconciler.go:2113, 2923, 1583`, `panel_primary_dkim.go:248`.
- **Description:** The singleton settings row is fetched with a full `SELECT *` on every call — once per sweep pass (~15 passes) plus 2–3× per domain per tick. The reconciler already demonstrates the fix pattern elsewhere (`isStandby` TTL cache, `previewStateCached`, `skeletonWire`). Spot-checked (repo method): confirmed.
- **Trigger/scale:** ~20 + 3D full-row reads per 60s tick — cheap per query, but the single most-executed query in the process, and it runs inside the 4-worker domain loop, adding DB contention.
- **Suggested fix:** read settings once at the top of `ReconcileAll` and thread it through, or add a short-TTL cache in the repo matching the `isStandby` pattern.

#### ME-7: `ReconcilePHPPools` / `reconcileVersionedPHPPools` per-user N+1
- **Area:** reconciler + event sources
- **Location:** `panel-api/internal/reconciler/reconciler.go:1086` (`FindByUserID` per user per tick), `:1104` (same fetch repeated), `:1210` (`ListByUserID` per user), `:1225` (`CountByPHPPoolID` per versioned pool).
- **Description:** Every tick, one pool query per user just to discover nothing needs doing — the early-exit only stops after 50 users *needing* pools, so a healthy host pays U queries every tick, twice (both passes list all users). Versioned pools add a per-pool COUNT. Spot-checked: confirmed.
- **Trigger/scale:** U users × 60s tick. U=200 → 400+ pool queries/min for a steady-state no-op.
- **Suggested fix:** one `ListAll` on php_pools per tick indexed by user_id; one grouped `COUNT(*) ... GROUP BY php_pool_id` for the reap check.

#### ME-8: `php.version.status` — per-user `systemctl` subprocesses + 7× redundant pin-file reads
- **Area:** panel-agent command handlers
- **Location:** `panel-agent/internal/commands/php_version_status.go:59-116` (`fpmWorkerStatus` per-version loop), `:81-84` (`fpmInstanceActive` — one `systemctl is-active --quiet jabali-fpm@<user>.service` per pinned user), `:107-116` (outer loop over the 7 supported versions).
- **Description:** For each of 7 PHP versions the handler re-reads the `userPhpverDir` directory, re-reads every pin file per version (7×U file reads), and spawns one `systemctl` per pinned user. The batched alternative exists in the same package: `php_version_reload.go:85` uses one `systemctl list-units 'jabali-fpm@*.service' --state=running`. Spot-checked: confirmed.
- **Trigger/scale:** every PHP Manager page load (panel-api `php_versions.go:70,114`). At 100 pinned users: ~100 subprocess spawns + 700 file reads → 1–3s added page latency, linear in tenant count.
- **Suggested fix:** read the dir + pin files once; take running state from a single `systemctl list-units` call and bucket per version in memory.

#### ME-9: Finalizer runs a full `restic snapshots` repo probe per running job every 30s
- **Area:** backup scheduler/finalizer/jobs
- **Location:** `panel-api/internal/backupfinalizer/finalizer.go:140` (10s timeout at `:138`) → agent `backupStatusHandler` at `panel-agent/internal/commands/backup_create.go:493` (`c.Snapshots(...)`), manifest re-open at `:535-537`.
- **Description:** Each finalizer tick issues one `backup.status` call per running job; the agent answers with `restic snapshots --tag job-id=<id>`, which reads every snapshot's metadata blob to apply the tag filter — on SFTP/S3 destinations a fresh SSH session + index/snapshot listing per call. Once the manifest exists it runs `restic dump` (another repo open, twice on the `/manifest.json` → `/system_manifest.json` fallback). The 10s call timeout is shorter than a typical remote-repo probe, so probes time out and retry 30s later, doubling the churn. Spot-checked: confirmed.
- **Trigger/scale:** a 60-minute backup = ~120 repo probes per job; 2 concurrent jobs against SFTP → a new SSH handshake + full snapshot listing every ~15s for the duration of every backup, competing with the running `restic backup` for backend I/O.
- **Suggested fix:** record the manifest snapshot ID locally (job log / marker file the agent can answer `backup.status` from without opening the repo); at minimum back the poll off (60–120s or exponential), share one snapshot listing per repo per tick, and raise the call timeout for remote destinations.

#### ME-10: Scheduled backup fan-out enqueue does per-(user, destination) package + retention-cap queries
- **Area:** backup scheduler/finalizer/jobs
- **Location:** `panel-api/internal/backupscheduler/scheduler.go:347` (destination loop) → `:386` (`userPackage` + `atOrOverCap` per pair); `ListForUser` count+select at `backup_job_repository.go:140-161`.
- **Description:** For an account schedule with no explicit user list, `enqueueAccountBackup` re-runs `Packages.FindByID` (same package for every destination) plus `atOrOverCap` → `ListForUser(userID, 1, 0)` (a `COUNT(*)` *and* a `LIMIT 1` SELECT just to read the total) for each (user, destination) pair. Spot-checked: confirmed.
- **Trigger/scale:** 1k-user host with 2 destinations → ~6–8k redundant queries inside the 60s enqueue tick; bursty MariaDB pressure exactly when the enqueue loop should be fast.
- **Suggested fix:** hoist the package lookup and cap evaluation out of the destination loop (once per user); add a COUNT-only `CountForUser` repo method instead of abusing the paginated list.

#### ME-11: mailscan recompiles the full YARA rule set for every attachment
- **Area:** notifications/mailscan/bandwidth/log processing
- **Location:** `panel-api/internal/mailscan/scanner.go:74-98` (`scanBytes` — `yr scan /usr/local/maldetect/sigs/rfxn.yara /etc/jabali/yara <tmpfile>`), called per attachment from `panel-api/internal/mailscan/tick.go:272`.
- **Description:** Each attachment spawns a fresh `yr scan`; yara-x compiles the entire rfxn pack (thousands of rules) from source on every invocation. Rule compilation typically dwarfs the scan of one small attachment, so per-message cost is constant-and-large regardless of attachment size. Spot-checked: confirmed.
- **Trigger/scale:** when mail scanning is enabled (opt-in), up to `PerTickBudget` (default 200) attachments per 5-min tick → up to 200 full rule compilations per tick; the dominant mailscan CPU cost on mail-heavy hosts.
- **Suggested fix:** precompile once with `yr compile` into a cached compiled-rules file invalidated on rule-file mtime, or batch a tick's attachments into one `yr scan <rules> f1 … fN` invocation.

#### ME-12: Log-stream modal accumulates lines unboundedly → O(n²) re-render on busy logs
- **Area:** notifications/mailscan/bandwidth/log processing (+ panel-ui)
- **Location:** `panel-ui/src/shells/admin/logs/LogStreamModal.tsx:148` (`setLogs(prev => [...prev, ...])`, no cap — same for the paused buffer at `:151`) and `:277` (`logs.join("\n")` per render).
- **Description:** Every WS frame appends to React state with no ring-buffer cap, and each render re-joins and re-renders the entire array — quadratic cost over the stream's lifetime (up to 15 min). Spot-checked: confirmed.
- **Trigger/scale:** any admin/user watching a live access log of a moderately busy domain; ~10 lines/s reaches ~9k lines, each line triggering a full-array join + DOM update. Tab janks within minutes; memory grows until stream expiry.
- **Suggested fix:** cap the buffer (keep last 1–2k lines via `prev.slice(-N)`) in both the live and paused-buffer paths, matching the "recent lines" semantics the feature advertises.

#### ME-13: hostedsvc global claim mutex held across Cloudflare/PowerDNS API calls
- **Area:** supporting services + shared libs
- **Location:** `hostedsvc/api.go:136-137` (`a.ipMu.Lock()` + function-level `defer Unlock`), DNS calls at `:171-180`.
- **Description:** `claim` takes a single process-wide mutex (despite the comment saying "serialises claim per source IP") and holds it across `DNS.EnsureA` and `DNS.EnsureWildcardA` — two remote API round-trips with a 15s timeout each. All claims fleet-wide serialize behind the slowest DNS publish. Spot-checked: confirmed.
- **Trigger/scale:** any concurrency on the public `/v1/claim` endpoint (batch onboarding, retry storms). Typical CF latency ~200–500ms caps global claim throughput at ~2–5 claims/s; a slow CF day stalls every claim service-wide.
- **Suggested fix:** release the lock after `Store.CreateLabel` succeeds (the collision race it exists for ends there), before the DNS calls — scope it with an explicit block. If per-IP serialization is the intent, key the mutex by IP; either way remote DNS I/O must sit outside the critical section.

#### ME-14: Per-grant `FindByID` N+1 in mailbox-share / shared-resource reconcile (per tick)
- **Area:** reconciler + event sources (cross-area note from the GORM reviewer; severity assigned in consolidation — the originating reviewer left it unrated)
- **Location:** `panel-api/internal/reconciler/phases/m65_mailbox_share.go:50-54` (per share: `Mailboxes.FindByID` + `Domains.FindByID`) and `panel-api/internal/reconciler/shared_resource_reconcile.go:133-140` (same pattern per grant).
- **Description:** Per-tick loops resolve each grant's target mailbox and domain with individual PK lookups although batch `FindByIDs` methods already exist (the JAB-147 pattern). Spot-checked: confirmed.
- **Trigger/scale:** every reconcile tick × shares/grants per mailbox/resource. Small n today, but it's unbatched work on the 60s hot path and trivially fixable with existing batch methods.
- **Suggested fix:** collect grantee IDs, one `FindByIDs`, one domain batch, map in Go.

### Low

#### LO-1: Send-as list — per-row `FindByID` despite existing batch method
- **Area:** panel-api GORM repositories
- **Location:** `panel-api/internal/api/mailbox_sendas.go:96-97`.
- **Description:** The send-as list loops delegations and calls `Mailboxes.FindByID` per row to resolve the grantor email, even though `MailboxRepository.FindByIDs` exists. n = delegations per mailbox (normally <10) — pattern violation more than a bottleneck.
- **Fix:** collect `GrantorMailboxID`s, one `FindByIDs`, map by id.

#### LO-2: Audit verify endpoint loads the entire `audit_events` table into memory
- **Area:** panel-api GORM repositories
- **Location:** `panel-api/internal/repository/audit_event_repository.go:254-263` (`AllForVerify`), called from `panel-api/internal/api/audit.go:75`.
- **Description:** Unbounded `Find(&rows)` ordered by `ts ASC` to re-verify the hash chain; the append-only security log can reach hundreds of thousands of rows between prunes. Rare admin action, but memory is O(table size) with no cap — the one endpoint that can OOM-spike the API on a busy box.
- **Fix:** stream with `Rows()`/`FindInBatches` and chain-verify incrementally (hash state is scalar).

#### LO-3: Ghost/registrar stale-scan indexes don't match their queries
- **Area:** panel-api GORM repositories
- **Location:** `panel-api/internal/repository/domain_repository.go:835-848` (`ListForGhostCheck`) and `:923-931` (`ListForRegistrarRefresh`); migrations `000092` / `000186`.
- **Description:** `ListForGhostCheck` filters on `ghost_checked_at` only, but `idx_domains_ghost_state (ghost_state, ghost_checked_at)` has a leftmost column absent from the predicate — dead index. `registrar_checked_at` (filtered the same way) has no index at all. Both are per-tick full scans of `domains`.
- **Fix:** single-column indexes on `ghost_checked_at` and `registrar_checked_at` (or fold `ghost_state` into the WHERE if that was the intent).

#### LO-4: Mailbox usage sampler is O(mailboxes) sequential agent RPCs
- **Area:** reconciler + event sources
- **Location:** `panel-api/internal/reconciler/mailbox_usage_ticker.go:66-87`.
- **Description:** Every 10 min, one `mailbox.usage` agent RPC (JMAP call into Stalwart) per mailbox, serialized with a 150ms stagger. M=500 → a ~75s sequential pass of 500 individual JMAP round-trips, each its own unix-socket connection.
- **Fix:** batched agent verb (`mailbox.usage_all` or paginated) so Stalwart is queried once per pass.

#### LO-5: Cron reconcile harvests `cron.status` per enabled job per tick
- **Area:** reconciler + event sources
- **Location:** `panel-api/internal/reconciler/cron_reconcile.go:77` → `:171`.
- **Description:** Every enabled cron job gets a `cron.status` RPC every 60s; agent-side each runs `sudo -u <tenant> systemctl --user show jabali-cron-<id>` — noisy enough that jabali's own per-cron-per-tick systemctl execs required a false-positive filter in the audit pipeline (`exec_audit_burst.go:78-89`, GH #403). The write-back is timestamp-gated; the RPC itself isn't.
- **Fix:** only poll `cron.status` for jobs whose schedule was due since the last harvest, or batch all of a user's jobs into one agent call.

#### LO-6: `bandwidth.scan_day` runs full goaccess analytics per domain log, serially
- **Area:** panel-agent command handlers
- **Location:** `panel-agent/internal/commands/bandwidth_scan_day.go:92-113` (serial loop), `:135-157` (one goaccess per file, 90s timeout each).
- **Description:** Goaccess builds its complete in-memory analytics model (hosts, URLs, referrers…) per log, but the handler only consumes `general.bandwidth` and `general.total_requests`. A byte-sum pass over the COMBINED format would be orders of magnitude cheaper. Runs once daily off-hours (bw ticker `bw_ticker.go:73`), hence Low.
- **Fix:** replace goaccess with a byte-offset sum (field 10), or cap concurrency and skip unused report sections if the version supports it.

#### LO-7: Per-dispatch N+1 in backup mailbox/metadata gathering
- **Area:** backup scheduler/finalizer/jobs
- **Location:** `panel-api/internal/backupscheduler/scheduler.go:727-749` (`userMailboxes`: per-domain mailbox query), `:558-559` (`userDatabasesByEngine` runs the identical `Databases.ListByUserID` twice per dispatch); `panel-api/internal/backupmetadata/builder.go:237-311` (per-domain: mailboxes, forwarders, DNSSEC keys, SSL cert, DNS zone + records; per-mailbox: autoresponder, shares), `:355` (`LimitOverrides.ListAll` full-table scan filtered in Go per user).
- **Description:** Every dispatched account-backup job re-queries the tenant's entire relational footprint (~6 queries/domain + 2/mailbox). Per-job rather than per-tick, and dispatcher concurrency is capped at 2, so no stampede — but hundreds of avoidable queries per dispatch.
- **Fix:** batch with `IN (domain_ids)` / `IN (mailbox_ids)` variants, fetch the database list once and split by engine in Go, add `FindByUserID` for limit overrides.

#### LO-8: Retention prune loop runs serial `restic forget` calls inside the enqueue tick
- **Area:** backup scheduler/finalizer/jobs
- **Location:** `panel-api/internal/backupscheduler/scheduler.go:449-470` (`pruneOldestForUser`).
- **Description:** When a tenant is over cap with `prune` policy, the enqueue path loops {count → oldest → agent `backup.forget` (full `restic forget`, 30s timeout each) → delete row} up to 1000 times, synchronously inside the 60s enqueue tick; each `restic forget` also takes the repo lock, contending with any running backup. Normally prunes exactly 1; hurts when a cap is lowered across existing backups (10→2), blocking the tick for minutes.
- **Fix:** prune only enough to admit the new job, or hand pruning to the dispatcher/background path.

#### LO-9: All 14 locale catalogs eagerly bundled into the main chunk
- **Area:** panel-ui frontend
- **Location:** `panel-ui/src/i18n.ts:45-58, 232-247`.
- **Description:** Every locale's `common.json` (444KB total source; en alone 88KB) is statically imported into the eager 1.6MB `index-*.js` chunk — ~13 catalogs (~280KB) downloaded and parsed by every user but never used. Route-level splitting (JAB-145) fixed pages; i18n stayed monolithic.
- **Fix:** keep `en` static (fallback) and dynamic-`import()` the detected locale before `i18n.init`, or use `i18next-http-backend` with locale JSONs as static assets.

#### LO-10: TasksIndicator polls every 3s; comment says 8s
- **Area:** panel-ui frontend
- **Location:** `panel-ui/src/components/TasksIndicator.tsx:3, 45`.
- **Description:** Admin header polls `/admin/active-tasks` every 3s (header comment says 8s). Endpoint is cheap and TanStack pauses in background tabs, but at 3s it's ~28k requests/day per always-open admin tab for a component that renders `null` 99% of the time.
- **Fix:** raise the idle cadence (15–30s) and drop to 3s only while `count > 0` via function-form `refetchInterval`; sync the comment.

#### LO-11: Bandwidth-quota enforce does a per-user N+1 batch every 60s against daily-granularity data
- **Area:** notifications/mailscan/bandwidth/log processing
- **Location:** `panel-api/internal/reconciler/bandwidth_quota_enforce.go:54-87` (called every tick at `reconciler.go:746`).
- **Description:** When `bandwidth_quota_enforce_enabled` is on, every 60s tick lists all users, then per eligible user issues 3 queries: `packages.FindByID` (static table, uncached), `bwDaily.SumByDomainForUser` (join+SUM over month-to-date), `domains.ListByUserID`. The underlying `bw_daily` data changes once per day, so 1439 of 1440 daily evaluations recompute identical results. Spot-checked: confirmed.
- **Fix:** evaluate hourly (or gate on `max(bw_daily.updated_at)` changing); hoist the package lookup into one batch `FindByIDs`/small map per pass.

#### LO-12: mailscan idle tick still writes one state row per mailbox per 5 min
- **Area:** notifications/mailscan/bandwidth/log processing
- **Location:** `panel-api/internal/mailscan/tick.go:212-216` and `:245-246`.
- **Description:** Every tick, for every mailbox of every principal, the scanner does a state-row `Get` + JMAP `Email/query`, then an unconditional `Upsert` just to bump `ScannedAt` — even when zero new mail arrived. 1 DB write + 2 JMAP round-trips per mailbox per 5 min, forever.
- **Fix:** skip the `Upsert` when the mailbox returned zero new emails (or throttle `ScannedAt` refresh to ~hourly); the cursor only matters when something was scanned.

#### LO-13: PDNS `getRRSet` fetches and decodes the entire zone to read one TXT rrset
- **Area:** supporting services + shared libs
- **Location:** `hostedsvc/pdns.go:130-161` (called from `SetChallenge`, `:108`).
- **Description:** ACME DNS-01 `SetChallenge` does `GET /api/v1/servers/localhost/zones/{zone}` with no filter and decodes every rrset to extract one `_acme-challenge` TXT; the zone grows by 2 A rrsets per claimed label, so cost grows linearly with fleet size. Only when `DNS_BACKEND=pdns` (Cloudflare default filters server-side correctly); per-label cert issuance/renewal frequency.
- **Fix:** pass PowerDNS's `?rrset_name=<name>&rrset_type=TXT` query params, which the API supports natively.

#### LO-14: support-claim maps grow unboundedly; rate-limit key is attacker-controlled
- **Area:** supporting services + shared libs
- **Location:** `support-claim/ratelimit.go:37-57` (bucket map never evicted), `support-claim/main.go:128-136` (sweep only removes TTL-expired claims), `:244-253` (`clientIP` trusts `X-Forwarded-For` unconditionally).
- **Description:** `rateLimiter.bucket` adds a permanent entry per key with no eviction, and the key comes from client-supplied XFF — rotating the header both bypasses the 30/min limit and grows the map without bound; `POST /claims` can likewise stuff the `claims` map. Public unauthenticated endpoint; ~100–250 bytes/entry, so a slow memory-growth/DoS surface rather than CPU. (Also a security observation, not just perf.)
- **Fix:** honor XFF only from trusted proxies (loopback), mirroring `hostedsvc/realip.go`; opportunistically evict idle buckets in `allow`.

#### LO-15: hostedsvc `audit` table is append-only forever
- **Area:** supporting services + shared libs
- **Location:** `hostedsvc/store.go:299-302` (schema at `:60-65`).
- **Description:** Every register/claim/revoke/reclaim inserts an audit row; nothing deletes them, no index on `at`, and the table is never read on any serving path — pure growth in the single SQLite file holding all service state. Harmless short-term; slowly raises backup/restore cost and risk.
- **Fix:** add a retention prune (e.g. 90 days) to the existing daily `reap` cron path.

## Areas with no high-confidence findings

- **Event-source cadences:** 30s–6h, all with `shouldFire`/dedupe/debounce; SSH keys, sendmail creds, user limits, nginx rate limits, user-egress, error pages, DKIM2, docker status, update-run ticker all have working no-change gates. Per-domain `domain.create` RPC per tick is a documented, deliberate design with an agent-side content-hash gate.
- **Core list handlers:** `domains.go:list`, `audit.go`, `users.go:list`, `admin_counts.go`, `mail_logs.go`, `user_limits.go` (60s TTL cache), `/automation/status` — paginated/batched/cached per convention.
- **panel-agent socket model:** goroutine per connection — no head-of-line RPC blocking; `service.list`, `system.software` (5-min cache), `system.user_slices`/`processes`/`cpu_usage` (/proc reads), `php.pool.reap-orphans` (gated, capped), `domain.list` (single ReadDir), `ssl.renew` (scoped, status-gated) all checked clean.
- **Redis Streams dispatch:** blocking XREADGROUP, pipelined XACK+XDEL, capped streams, bounded parallel fanout, stable consumer name — no short-poll or XACK storms.
- **internal/kratosclient, appseccfg, dnsverify, dkim, filesafe, fsperm, limits, agentwire, ops/cf-update-bridge:** pooled HTTP clients, whoami LRU+TTL cache, write-on-diff atomic config writes, bounded ACME-path retries, no MustCompile-in-function or hot-loop allocation issues. hostedsvc request-path queries all hit PK/UNIQUE/indexed columns.

## Methodology & limitations

- Static read-only review; no profiling, benchmarks, or load tests were run.
- Impact estimates are reasoned from call frequency × data size (tick cadences, poll intervals, fleet/table sizes), not measured. Severity reflects worst-credible-case cost on a busy production host.
- Reviewers cross-checked claimed missing indexes against `panel-api/internal/db/migrations/` before reporting, and all High findings plus a sample of Mediums were re-verified against the source during consolidation. Line numbers cite commit `735d7445`.
- Dropped during consolidation: one cold-path finding explicitly flagged as such by its reviewer (`me_disk_usage` recompute N+1 — user-triggered `POST /me/disk-usage/refresh` only, GET reads a persisted snapshot) and one speculative, unverified correctness side-observation (per-destination restic password tempfile lifetime in `backupwrapperhelpers/dest_password.go:73-81` — flagged by its own reviewer as unverified end-to-end; it is a correctness question, not performance, and is worth a separate look by the owner of that path).

## Remediation status (updated 2026-08-08)

| Finding | Status | PR |
|---------|--------|-----|
| HI-1 ungated DNS zone push | fixed | #984 |
| HI-2 `nginx -t` on every server-status poll | fixed | #981 |
| HI-3 finalizer pages 25 jobs (queue wedge) | fixed | #983 |
| HI-4 GoAccess re-parses the log every 10s | fixed | #981 |
| ME-2 ServerHealthIndicator bypasses the query cache | fixed | #981 |
| ME-9 finalizer repo probe (timeout half) | fixed | #982 |
| ME-10 enqueue per-(user,destination) queries | fixed | #983 |
| ME-12 log modal unbounded buffer | fixed | #990 |
| ME-13 claim mutex held across DNS calls | fixed | #989 |
| LO-1 send-as per-row FindByID | fixed | #990 |
| LO-3 ghost/registrar indexes don't match queries | fixed | #990 |
| LO-8 serial restic forget inside the enqueue tick | fixed | #983 |
| LO-10 TasksIndicator 3s poll | fixed | #990 |
| LO-14 support-claim maps grow unboundedly | fixed | #989 |
| ME-1, ME-3..ME-8, ME-11, ME-14, LO-2, LO-4..LO-7, LO-9, LO-11..LO-13, LO-15 | open | — |

Notes from doing the work:

- **HI-1's root cause is worth stating precisely**: there was no content
  compare, and there *could not* have been one. The SOA serial was stamped
  with `time.Now()` on every pass, so the payload differed every tick by
  construction and no downstream gate could ever have fired. Any fix has to
  exclude the serial from the comparison.
- **ME-6 (server_settings memoization) was attempted and reverted.** The
  reconciler already contains caches (`previewStateCached`, `standbyCached`)
  whose contract is "when MY TTL expires, re-read settings fresh"; a cache
  underneath them silently defeats that, which `TestPreviewStateCacheTTL`
  correctly caught. Doing it properly needs an explicit fresh-read path for
  those callers, so it stays open rather than half-done.
- **The dropped finding was real.** This review dropped the per-destination
  restic password tempfile lifetime as "speculative, unverified end-to-end".
  It is verifiable from two files: the cleanup is deferred to the return of
  `backup.create`, which is fire-and-forget. The security review rated it High
  and was right. Fixed in #982.
