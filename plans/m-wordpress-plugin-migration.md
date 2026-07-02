# Blueprint — WordPress Plugin Migration (PULL-via-API) · GH #648

**Objective:** move a WordPress site into Jabali with **no source SSH** — the operator
installs the **jabali-migrator** plugin on the source WP site, which exposes token-authed
REST endpoints; **Jabali pulls** the DB + files (resumable, chunked) into a destination,
then imports + URL-rewrites + verifies.

**Design pivot (2026-07-02):** the first draft was a **push** model (source uploads to a
public Jabali endpoint). Reworked to **pull** because it deletes the biggest risk — a new
internet-facing, tenant-triggerable upload surface on the control plane — and it *is* the
shape the existing framework already runs.

**Model.** The existing `internal/migrate` framework is **pull** (`Discoverer.Connect(host,user,secret)` → cPanel/DA/Hestia; `migrationPullSourceRunHandler` runs the pull **in the agent**, writing staging as root; then `import` stages→dest). This adds a `wordpress_plugin` **Discoverer** that connects to the source's `jabali-migrator` REST API and pulls. **It reuses:** the migration-job model, the migration **secret** store, the **agent pull runner**, the **staging area**, and the **import handlers**. **It adds:** the WP-REST source client + a WP plugin that serves it. **No new public Jabali endpoint** (optional tiny phone-home only).

**Why pull > push (recorded so the executor doesn't relitigate):**
- **Security:** Jabali is a CLIENT, not a server for untrusted uploads. The public surface is the SOURCE's own WP REST (already public), behind a plugin token. A leaked token exposes the user's OWN site export, not Jabali. The entire push S3–S6 public-endpoint hardening spine evaporates.
- **Framework fit:** it's the `Discoverer`/pull model already in production for 3 source kinds. Far less net-new code.
- **Jabali drives:** capacity-gate, retry, resume, parallelism from the strong side, before pulling.
- **Trade-off (the ONE cost):** the source must be reachable by Jabali. For "migrate my live WordPress site" it always is (it's an inbound HTTP server). Push only wins for firewalled/unreachable sources — rare; keep push as a documented future fallback, not v1.

**Grounding (reuse, don't reinvent):**
- `panel-api/internal/migrate/discover.go` — the `Discoverer` interface (`Connect`/`ListAccounts`/`DescribeAccount`/`Close`) + `Session`/`AccountManifest`. Add a `wordpress_plugin` Discoverer here (blank-import registered like cpanel/directadmin/hestiacp in serve.go).
- `panel-agent/internal/commands/migration_admin_run.go` — `migrationPullSourceRunHandler` (the agent pull) + `migrationImportRunHandler` (staging→dest). The WP pull hooks the SAME run path.
- `panel-agent/internal/commands/migration_import_home.go` — `migrationImportHomeHandler` + `TestMigrationImportHomeContainment` (staging→dest path containment — reuse).
- `panel-api/internal/api/admin_migrations.go` — routes (`/pull-source`, `/import`), `isKnownSourceKind` (already has `wordpress_plugin`), the migration **secret** upload mechanism + the `jabali-migration-secrets-reap.timer`.
- Migration secret store (encrypted at rest, reaped) — the `wordpress_plugin` token lives here (Jabali must SEND it to the source, so it's stored recoverable, NOT hash-only).
- SSRF floor + per-user egress (M34 / GH #401) — the agent's outbound to the source URL must be validated public (see A4).

**Workflow:** git + gh; branch per step, PR, CI green before merge. ADR target: **ADR-00NN (wordpress_plugin PULL migration)**.

---

## Architecture decisions

**A1 — No public Jabali ingress.** Pull means Jabali connects OUT; there is NO public upload endpoint to expose or harden. (Optional later: a tiny unauthenticated `plugin-register` phone-home so the plugin can hand Jabali its URL + confirm reachability — a single URL write, not a data path. v1 skips it: the operator enters the source URL when minting the key.)

**A2 — The AGENT pulls + writes staging** (unchanged from the existing pull kinds). The `wordpress_plugin` pull runs under the same transient-unit `migrationPullSourceRunHandler` path as cPanel/DA; it fetches manifest + chunks over HTTPS from the source and writes the job staging dir as root. panel-api never touches the payload. This resolves the push draft's panel-api-vs-agent writer fork by simply reusing the pull path.

**A3 — Import is operator-gated by default** (unchanged): after the pull completes + verifies, the operator triggers `/import`. Destructive (overwrites a docroot + DB) from an untrusted source → a human authorizes. S11 shows `pulled — awaiting import`.

**A4 — SSRF is the new #1 risk (replaces public-upload hardening).** The source URL is user-supplied and the AGENT fetches it. **Pin-the-IP, not check-the-name:** resolve the host, validate the resolved address is **public** (reject loopback, RFC1918, link-local, `169.254.169.254` + cloud metadata, and anything the SSRF floor blocks), then **connect to that pinned IP** with the Host header set — a pre-check on the hostname alone loses to **DNS rebinding** (the name re-resolves to a private IP between check and fetch). Re-validate the peer IP on every redirect (no redirect to a private host); https-only. Cap response size + total (a hostile source could serve a decompression bomb or an infinite chunk stream — bound every fetch, don't stream forever). This is the S12 gate's core.

---

## Dependency graph

```
S1 (token+job model) ──> S2 (mint API + source URL) ──> S3 (wordpress_plugin Discoverer: Connect+ping) ──> S4 (pull manifest + capacity/ownership/multisite gate) ──> S5 (resumable chunked PULL: DB+files → staging) ──> S6 (verify hashes+containment) ──> S7 (import+rewrite) ──> S8 (health+cleanup)
S9 (progress) cross-cuts S3–S8.  S10 (WP plugin, REST provider) ∥ once the S3–S5 REST contract is frozen.  S11 (UI) ∥ after S2.
S12 (SSRF + adversarial gate) gates MERGE of S3–S7.
```

---

## S1 — Token + job model  ·  ⚠ THE COMMITTED S1/S2 CODE IS PUSH-SHAPED — this is REWORK + DEAD-CODE REMOVAL, not a tweak

**Reality check.** `cd01dfbc`/`1193a75d` were built for the PUSH model and are now largely wrong: **there is no claim in pull.** Dead / inverted:
- `MigrationKey.KeyHash` (hash-at-rest) — pull needs a **recoverable, encrypted** token because Jabali *sends* it outbound. The posture INVERTS.
- Repo `Claim` (atomic conditional UPDATE), `FindByKeyHash`, `FindByUploadSessionHash` — DELETE (no claim, no upload session).
- **`TestMigrationKeyRepo_Claim_OneTime` + `TestMigrationKeyRepo_FindByKeyHash` — DELETE.** They test deleted mechanisms; keeping them is the exact "inert artifact after a pivot" trap caught earlier this session (the drawer's object/page/TTL fields), now at a security-critical layer.
- Migration `000207` — **rewrite** to the pull shape (drop `key_hash`/`claimed_source_url`/`upload_session_hash`; add `source_url`). Additive, branch unmerged, so a clean rewrite is fine.

**Tasks (pull shape)**
- Keep: `wordpress_plugin` source kind + `isKnownSourceKind` (`cd01dfbc`).
- Rewrite `migration_keys`: `id, job_id, dest_user_id, dest_domain_id, dest_docroot, domain_change, status(unclaimed→active→done/revoked), expires_at, source_url`. The **token itself** lives in the existing migration-**secret** store (encrypted, reaped), NOT in this table.
- Repo: `Create`, `FindByJobID`, `SetSourceURL`, `UpdateStatus`, `Revoke`, `ReapExpired` — and remove the push methods + their tests.

**Verify:** `go build`; migration up/down clean (MariaDB 11.x, schema-only); repo test (create/find/status — NOT claim). **Exit:** model fits the pull token + source URL; no push-era symbol or test survives. **Rollback:** revert the table rewrite.

## S2 — Mint token + source URL (AUTHENTICATED, owner-scoped)  ·  keep, extend

**Context.** S2 built the admin mint (`1193a75d`). Extend: accept the **source URL** and store the token in the secret store.

**Tasks**
- `POST /admin/migrations/wordpress-plugin` — body: dest user/domain + `domain_change` + **`source_url`**. Owner-coherence (already). Generate a 256-bit token, store it in the migration-secret store for the job, return it ONCE + install instructions ("install jabali-migrator on `<source_url>`, paste this token"). Validate `source_url` is a well-formed https URL up front (full SSRF check is at pull time, A4).
- Revoke / status — keep.
- **Do the deferred S2 handler test now** (needs the migration mock scaffolding): owner-coherence 400/404, token-once, source-URL required.
- Owner-scoped **tenant** route (own domains only) can follow in S11.

**Verify:** handler tests through the router (token once; secret stored, not returned again; bad source_url → 400). **Exit:** authed mint with source URL, tested. **Rollback:** routes off.

## S3 — `wordpress_plugin` Discoverer + Connect/ping  ·  ⚠ SSRF gate (A4)

**Context.** Replaces the push "public claim." A Discoverer that validates the source is reachable + the token works, before a long pull.

**Tasks**
- **FIRST verify the reuse claim (do not assume):** read `migrationPullSourceRunHandler` internals. cPanel/DA pull over **SSH/rsync**; a WP pull is **HTTP-REST chunk fetching**. Confirm the runner dispatches by `source_kind` (so a new kind slots in) rather than being SSH-shaped throughout. If it's SSH-only, S3/S5 are bigger (a parallel WP pull path in the agent) and the "far less net-new code" claim weakens — know this before committing to reuse.
- New `panel-api/internal/migrate/wordpressplugin/` Discoverer (blank-import registered in serve.go like the others). `Connect(sourceURL, token)` → `GET <source>/wp-json/jabali-migrator/v1/ping` with `Authorization: Bearer <token>` → 200 + a version → Session; auth/unreachable → clear error surfaced early.
- **A4 SSRF validation** in the HTTP client used for the pull: resolve host, reject private/loopback/link-local/metadata, https-only, no-redirect-to-private, bounded timeouts. Shared by S3–S5.

**Verify:** unit — ping ok → Session; wrong token → auth error; private/metadata source URL → refused before any fetch. **Exit:** reachability+auth+SSRF proven before pull. **Rollback:** don't register the Discoverer.

## S4 — Pull manifest + capacity/ownership/multisite gate

**Tasks**
- `DescribeAccount` → `GET <source>/wp-json/jabali-migrator/v1/manifest` (token). Manifest: site URLs, DB tables+sizes, file count+total, plugins/themes, WP+PHP version, table prefix, `multisite`.
- Gate BEFORE pulling payload: dest still owned + quota headroom vs declared size; **multisite** → block with a clear message (v1) or explicit support; declared totals within hard caps. Persist manifest on the job.

**Verify:** over-quota → refused; multisite → blocked; accepted → advances. **Exit:** payload pull gated on a validated manifest. **Rollback:** n/a (read-only pull).

## S5 — Resumable chunked PULL (DB + files) → staging  ·  ⚠ agent-side, path-safe

**Context.** The agent pull runner fetches chunks and writes the job staging dir (as root), resumable so a shared-host source with low limits works.

**Tasks**
- Agent (under `migrationPullSourceRunHandler` for kind=wordpress_plugin): loop `GET <source>/wp-json/jabali-migrator/v1/db-chunk?offset=` and `/file-chunk?path=&offset=` (token), each returning `{sha256, bytes, total}`; verify sha256 per chunk; write under `migrationStagingDir(job)` ONLY.
- **Path safety on the WRITE (still critical):** normalize each declared file path, reject `..`/absolute/escape (reuse `TestMigrationImportHomeContainment` logic), reject symlinks, enforce per-file/total/count caps from the manifest (hard-capped — never trust the source's numbers). **Never execute pulled PHP** in staging. Quarantine `wp-config.php`/drop-ins/`.user.ini`/`php.ini` (handled in S7, not written raw).
- Resume: skip chunks already present+hash-matched. Bound EVERY fetch (size/time) — a hostile source must not stream forever or bomb memory.

**Verify:** unit+integration — a source serving a `../../etc` path / symlink / oversized-count is rejected; resume after interruption completes; hash mismatch rejected; wp-config quarantined. **Exit:** pull is path-safe, size-bounded, resumable, PHP-inert. **Rollback:** reaper cleans partial staging.

## S6 — Verify (hashes + containment) + mark pulled

**Tasks**
- After the pull loop, verify every manifest file/chunk present + sha256 matches; final containment check of the staged tree; DB dump reassembled + sane. Mark job `pulled` (NOT auto-import — A3).

**Verify:** missing/mismatched → fail, no import; clean → `pulled`. **Exit:** only a verified, contained payload reaches import. **Rollback:** read-only.

## S7 — Import = the SHARED `migration.import_wp` (⋆ from #647)

**Tasks**
- **Import is the shared `migration.import_wp` spine defined in `plans/m647-wordpress-ssh-migration.md` S4** (the canonical WP import contract for BOTH transports). Whichever of #647/#648 ships first BUILDS it; here we REUSE it — do NOT fork a second WP import, and do NOT reuse the cPanel-account-shaped `migration.import_run`. This pull just lands `files/` + `dump.sql` in staging (S5/S6); `import_wp` does DB create + import + files→docroot (containment) + `wp-config.php` rewrite + serialized-safe `wp search-replace` + perms + cache flush.
- Import staging→dest docroot (containment guards); import DB into the dest Jabali DB.
- Rewrite `wp-config.php` to Jabali DB creds/socket (reuse `wordpress_clone.go` openat2 no-symlink pattern). **Serialized-safe** `wp search-replace` for `domain_change`. Fix perms; flush object/opcache; strip foreign source cache drop-ins (mirror #621).

**Verify:** test source→dest serves; domain-change rewrites serialized data; wp-config points at Jabali. **Exit:** migrated site live+correct. **Rollback:** dest is a fresh app; delete+retry.

## S8 — Health verify, cleanup, token revoke

**Tasks**
- WP/HTTP health probe (reuse `migration.http_probe` / `--resolve`); success → job `done`.
- Terminal cleanup (any outcome): wipe staging, revoke the token (secret store), mark key status. Reaper timer for stranded tokens + orphan staging (existing `jabali-migration-secrets-reap.timer` + `orphanMigrationStagingDirs`).

**Verify:** success → healthy + staging gone + token revoked; failure likewise. **Exit:** no token/payload outlives terminal state. **Rollback:** n/a.

## S8b — Same-domain preview + safe cutover

**Tasks**
- Same-domain (`domain_change=false`): preview the imported site on Jabali via `curl --resolve <domain>:443:<box-ip>` (the warmup/probe technique) BEFORE DNS cutover — a "Preview" action + copy-paste hint in S11. Only after operator confirmation does DNS cut over (out of band). Domain-change skips this (S7's search-replace handles the new URL).

**Verify:** same-domain site renders via the preview path while public DNS is unchanged. **Exit:** validate before cutover. **Rollback:** discard the dest app if wrong.

## S9 — Progress reporting (cross-cuts S3–S8)

**Tasks**
- The agent pull writes `migration_stages` (BytesProcessed) as it fetches — the EXISTING stage/progress model already used by cPanel/DA pulls. `GET /admin/migrations/:id` returns live progress; M14 events on state change. Little new code — reuse the stage rows.

**Verify:** progress advances ping→manifest→pull→pulled→(import)→done. **Exit:** operator sees live progress. **Rollback:** read-only.

## S10 — The jabali-migrator WordPress plugin (REST provider)  ∥ after S3–S5 contract

**Context.** New WP plugin `wp-plugins/jabali-migrator/` (like jabali-cache), installed on the SOURCE. wp-admin only. It SERVES a token-authed REST API that Jabali pulls; it does NOT push.

**Tasks**
- Admin screen: generate/paste the token, show status. `register_rest_route('jabali-migrator/v1', …)`: `ping`, `manifest`, `db-chunk`, `file-chunk` — ALL behind a constant-time token check (Bearer). Never expose DB creds to the browser.
- `manifest`: WP/PHP version, active theme/plugins, table prefix, home/siteurl, file count+size estimate, multisite flag.
- `db-chunk`: stream the DB in ranges — `WP_CLI` if available, else `mysqldump`, else a chunked `SELECT` export; per-chunk sha256.
- `file-chunk`: serve `wp-content` (+ core) file ranges with sha256; handle low `max_execution_time`/memory by serving small ranges (Jabali drives the loop). Optional maintenance mode during final pull.
- Rate-limit + lock the endpoints to the token; ship to WordPress.org later + bundle a fallback in the repo.

**Verify:** on a real shared-host-like WP, Jabali pulls a full site through the plugin's REST; resumes after a killed request. **Exit:** a full site migrates, source needs only wp-admin + being reachable. **Rollback:** deactivate the plugin.

## S11 — Panel UI: Applications → Migration → WordPress (plugin mode)  ∥ after S2

**Tasks**
- Migration flow → WordPress → plugin mode: dest user/domain + same-or-changing domain + **source URL**; mint token (S2); show install instructions + the one-time token; live progress (S9); **Preview** (S8b) + **Import** (A3) actions; revoke. Admin + tenant-owner-scoped. Repo Drawer/SearchableTable conventions; `npm run build` + a component test (#616 drawer pattern) + a browser/Playwright drive.

**Verify:** `npm run build` clean; component test locks the wire contract; manual drive of mint→token→pull→preview→import. **Exit:** operator drives the whole flow. **Rollback:** UI-only.

## S12 — SSRF + adversarial gate (GATES merge of S3–S7)

**Context.** The risk moved from "defend a public upload endpoint" to "the agent safely pulls from an untrusted, possibly-hostile source." That's the gate.

**Tasks**
- Adversarial suite: **SSRF** (source URL = loopback/RFC1918/link-local/`169.254.169.254`/metadata, DNS-rebind, redirect-to-private) refused; hostile source serving path-traversal/symlink/zip-slip file paths → write refused; oversized/over-count/decompression-bomb/infinite-stream → bounded + aborted; chunk hash forgery → rejected; token oracle/timing; PHP-in-staging non-execution; wp-config/drop-in raw-write refused.
- Server-side caps never trust the manifest.
- **Non-negotiable:** a real end-to-end **pull** on the test server (a live WP source → dest) + at least one adversarial case (an SSRF source URL AND a traversal file path) proven refused at runtime (unit tests lie at trust boundaries — cf. #601/#637 runtime-caught bugs).

**Verify:** adversarial suite green AND a live source→dest pull succeeds on the test box. **Exit:** the agent-pull path is hostile-source-safe at runtime. **Rollback:** if any adversarial case fails, S3–S7 don't merge.

---

## Anti-patterns (learned)
- Don't trust the manifest for caps — enforce server-side (S4/S5).
- Don't accept `wp-config.php`/drop-ins/`.user.ini`/`php.ini` raw — quarantine + handle in S7 (S5).
- Don't auto-import on pull-finalize — operator-gated (S6→S7, A3).
- Don't fetch the source URL without SSRF validation — the agent is the new attack vector (A4/S3/S12).
- Don't build a public Jabali upload endpoint — pull, no ingress (A1). (This is the whole pivot; don't drift back to push without re-justifying.)
- Don't skip the runtime adversarial test — trust boundaries lie in unit tests (S12; cf. #601 dead-wiring).
- Migration = schema only; no seeding from app-populated tables ([[feedback_migration_data_seed_ordering]]).
