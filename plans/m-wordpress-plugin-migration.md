# Blueprint — WordPress Plugin Migration (push model) · GH #648

**Objective:** move a WordPress site into Jabali with **no source SSH** — a Jabali WP
plugin on the source site claims a one-time migration key and *pushes* the DB + files
(resumable, chunked) into a Jabali destination, which imports + URL-rewrites + verifies.

**Model:** the existing `internal/migrate` framework is **pull** (`Discoverer.Connect(host,user,secret)` → cPanel/DA/Hestia). This adds a **push** source kind `wordpress_plugin`: the source connects *in* through a public token-gated endpoint. It **reuses** the migration-job model, the **staging area**, and the **import/restore handlers** (`migrationImportHomeHandler`, DB import); it **replaces** the `pull-source` step with an inbound upload protocol.

**Security is the spine.** A public, tenant-triggerable upload endpoint is a new attack
surface. Every step below carries its slice of: token scoping, path normalization,
size/count/chunk caps, symlink handling, no-PHP-exec-in-staging, protect Jabali-managed
runtime files. Steps 3–6 are the critical ones and MUST get the strongest-model review.

**Grounding (reuse these, don't reinvent):**
- `panel-api/internal/api/admin_migrations.go` — `RegisterAdminMigrationRoutes`, `isKnownSourceKind` (allowlist — add the new kind here), `migrationStagingDir`.
- `panel-api/internal/repository/migration_job_repository.go` — `MigrationJobRepository` (job model, `FindBySource`, `UpdateSourceUser`, status transitions).
- Existing routes `/admin/migrations/:jobId/{pull-source,import}`.
- `panel-agent/internal/commands/migration_admin_run.go` — `migrationImportRunHandler`, `migrationPullSourceRunHandler`.
- `panel-agent/internal/commands/migration_import_home.go` — `migrationImportHomeHandler` + `TestMigrationImportHomeContainment` (path-containment security — the pattern staging→dest must follow).
- Short-lived one-time token precedent: the M22 self-deleting SSO file (256-bit nonce, flock+unlink, TTL, systemd reaper) and the magic-link token — mirror their expiry/one-time/revoke discipline.
- Per-tenant ownership scoping: `FindByIDAndUserID` pattern (a tenant may only target a domain/app they own).

**Workflow:** git + gh present → branch per step, PR, CI green before merge. Agents commit to feature branches, never main; dispatcher merges. ADR target: **ADR-00NN (wordpress_plugin migration + inbound upload protocol)**.

## Progress (branch `m648/wp-plugin-migration`)
- **S1 DONE** (`cd01dfbc`): source kind + `migration_keys` (000207) + `MigrationKey` model + `MigrationKeyRepository`. One-time claim = atomic conditional UPDATE, repo-tested (claim once→nil, second/expired→ErrNotFound, guard asserted in SQL).
- **S2 DONE** (`1193a75d`): admin key-mint API (`POST /admin/migrations/wordpress-plugin` + revoke + status), owner-coherence check, claim URL from `server_settings.hostname`, plaintext key once. Built+wired, no regressions. **Deferred: S2 handler test** (owner-scope 400/404, key-once) — needs migration mock scaffolding (the migration API has zero handler tests today); do it FIRST in the continuation.
- **NEXT: S3** — public claim endpoint. ⚠️ security trust boundary; strongest-model review; do NOT rush. Then S4→S12.

---

## Architecture decisions — RESOLVE IN S1/S3 (do not discover in S5)

**A1 — Ingress: where does the public endpoint live + what URL does the plugin hit?**
Post-M25 panel-api listens on a **unix socket behind nginx**; the source WP site is on the public internet. Decision: expose `/public/migrations/*` as an nginx `location` on the **panel hostname's public vhost** (the same TLS vhost that already fronts panel-api), proxied to panel-api's socket. S2's "install instructions" MUST emit the concrete base URL = `https://<panel-hostname>/public/migrations` + the key. No per-tenant hostnames (avoids cert/routing sprawl). Rate-limit this location at nginx too.

**A2 — Staging writer: panel-api (jabali) vs agent (root) — the ownership fork.**
Existing staging is **agent-written as root**, then the agent imports. The public upload endpoints are in **panel-api (jabali user)** → a writer/permission mismatch, and streaming GB-scale payloads through the control-plane API + unix socket is awkward. **Decision (recommended, spell out in S3/S5):** panel-api TERMINATES the public request (auth, path-safety, caps, hash) but **forwards each validated chunk to the AGENT via a new `migration.upload_chunk` verb**, and the **agent writes staging as root** — preserving the existing staging ownership + the containment guards `migrationImportHome` already trusts. panel-api never writes the staging tree; it is the validating front. (Alternative to reject explicitly: a dedicated agent-owned HTTP ingress — more surface, rejected unless perf demands it.) This decision is load-bearing for S5's write path and S10's client target.

**A3 — Import trigger (see S6): operator-gated by default**, a deliberate deviation from the issue's implied auto-flow — called out below.

---

## Dependency graph

```
S1 (data model + token) ──┬─> S2 (mgmt API, authed) ──┐
                          ├─> S3 (public claim) ──> S4 (manifest+validate) ──> S5 (chunked upload) ──> S6 (finalize+verify) ──> S7 (import+rewrite) ──> S8 (health+cleanup)
                          └─> S9 (progress) ───────────────────────────────────────────────────────────────────────────────────┘ (cross-cuts 3–8)
S10 (WP plugin)  depends on the wire contract frozen after S4/S5 (can start once endpoints are stubbed).
S11 (panel UI)   depends on S2 (+ S9 for live progress).
S12 (security hardening + adversarial tests) gates MERGE of S3–S7.
```
Parallel after S1: S2 ∥ S3-start. S10 (plugin) ∥ once the S3–S5 wire contract is frozen. S11 (UI) ∥ after S2.

---

## S1 — Data model: source kind + one-time migration key + upload session

**Context.** The job model exists; add the push-source vocabulary + the token. A `wordpress_plugin` job has NO source host/credentials (Jabali never connects out) — instead a **migration key** the plugin claims.

**Tasks**
- Add `wordpress_plugin` to `isKnownSourceKind` (allowlist) and any source-kind enum.
- New table `migration_keys` (migration NNNNNN, additive): `id`, `job_id` (FK), `key_hash` (store only a hash — never the plaintext), `dest_user_id`, `dest_domain_id`, `dest_docroot`, `domain_change bool`, `status` (`unclaimed|claimed|active|done|revoked`), `expires_at`, `claimed_source_url` (NULL until claimed, then pinned), `upload_session_hash` (the exchanged scoped token, hashed), `created_at`. Nullable/additive, no data restructure.
- Repo `MigrationKeyRepository`: `Create`, `FindByKeyHash` (constant-time compare in the handler, not SQL), `Claim(id, sourceURL)`, `Revoke`, `SetUploadSession`, `MarkDone`, `ReapExpired`.
- Key = 256-bit random, shown ONCE to the operator; only its hash persists.

**Verify:** `go build ./...`; migration up/down clean on MariaDB 11.x (see [[feedback_mariadb_reserved_words]], [[feedback_merge_audit_migrations]] — schema only, no seeding); repo unit tests (claim once → second claim rejected; expired → rejected).
**Exit:** the model + repo compile + are unit-tested; `wordpress_plugin` is an allowed kind. **Rollback:** drop the table + revert the allowlist line.

## S2 — Key generation + management API (AUTHENTICATED, owner-scoped)

**Context.** Admin OR tenant creates a `wordpress_plugin` job + key. **Tenant may only target a domain/app they own** (mirror `FindByIDAndUserID`). Under the existing migration route family.

**Tasks**
- `POST /migrations/wordpress-plugin` — body: dest user/domain/app + `domain_change`. Owner-scoped (admin any; tenant own). Creates the job (`internal/migrate`) + a key (S1). Returns the plaintext key ONCE + install instructions payload.
- `POST /migrations/:jobId/key/revoke` and `/rotate` — owner-scoped.
- `GET /migrations/:jobId` — status + progress (reads S9). Owner-scoped.
- Rate-limit key creation per user.

**Verify:** handler tests through the real router: tenant creating for an unowned domain → 404; key returned once, only hash stored; revoke flips status. **Exit:** authed CRUD + owner scope proven by test. **Rollback:** unregister routes.

## S3 — Public claim endpoint  ⚠️ CRITICAL / strongest-model review

**Context.** The plugin (no Jabali session) POSTs the key to a PUBLIC endpoint → exchange for a **scoped upload session token** bound to this one job + dest. This is the trust boundary.

**Tasks**
- `POST /public/migrations/claim` (unauthenticated except by the key). Validate: key hash matches (constant-time), status `unclaimed`, not expired. On success: pin `claimed_source_url` (from the body, validated as a URL/host), generate a scoped upload-session token (hashed at rest), set status `claimed`. Return the session token + the manifest endpoint.
- **One-time:** a claimed/expired/revoked key is rejected. Re-claim from a DIFFERENT source URL is rejected.
- Aggressive rate-limit + generic errors (no key-existence oracle). Log claims (M14 + audit).
- All subsequent upload calls authenticate with the **session token**, scoped to `job_id` — never the key again.

**Verify:** tests — valid claim once; second claim rejected; expired rejected; wrong key rejected (same generic 401/403, timing-safe); source-URL pinning enforced on later calls. **Exit:** claim is one-time, scoped, rate-limited, oracle-free. **Rollback:** unregister route (no data at rish — keys unclaimed).

## S4 — Manifest submit + capacity/ownership/multisite gate  ⚠️ CRITICAL

**Context.** Before ANY payload, the plugin sends a manifest; Jabali validates and can reject early (the issue's "validate capacity + destination ownership before accepting payloads").

**Tasks**
- `POST /public/migrations/manifest` (session-token auth). Body: site URLs, DB table list+sizes, file count+total size, active plugins/themes, WP+PHP version, table prefix, `multisite` flag.
- Validate: dest still owned + has capacity (disk quota headroom vs declared size); **multisite** → explicit-support decision (default: block with a clear message per the edge cases); declared totals within hard caps.
- Persist the manifest against the job; move status → `active`. Reject → terminal + revoke.

**Verify:** manifest over quota → rejected; multisite → blocked with message; accepted manifest advances status. **Exit:** payload is gated on a validated manifest. **Rollback:** route off.

## S5 — Resumable chunked upload (DB + files)  ⚠️ CRITICAL / strongest-model review

**Context.** The heart. Resumable chunked protocol (NOT one giant zip) so shared hosts with low limits / disabled ZipArchive work. Lands in the SAME staging area the import step reads.

**Tasks**
- `POST /public/migrations/db-chunk` and `/file-chunk` (session-token auth, scoped to job). Each chunk: `{path (for files), offset, total, sha256, bytes}`. Idempotent by `(path, offset, sha256)` for retry/resume/dedupe.
- Land under `migrationStagingDir(job)` ONLY. **Path safety (the whole ballgame):** normalize + reject any `..`, absolute paths, or escape from the job staging root — reuse the containment logic proven by `TestMigrationImportHomeContainment`. Reject symlinks in the payload (store as regular files or refuse). Enforce per-chunk size, per-file size, total size, and file-count caps (from the manifest, hard-capped).
- **Never execute uploaded PHP** during staging (files written, never included/run). Quarantine, do NOT let the payload overwrite Jabali-managed runtime files (`wp-config.php`, object-cache/advanced-cache drop-ins, `.user.ini`, custom `php.ini`) — those are handled explicitly in S7, not accepted raw.
- `GET /public/migrations/upload-status` — which chunks are present (drives client resume).

**Verify:** unit + integration — traversal payload (`../../etc`, absolute, symlink) rejected; oversized/over-count rejected; resume after interruption completes; hash mismatch rejected; a `wp-config.php` in the payload is quarantined not written to the live drop-in path. **Exit:** upload is path-safe, size-bounded, resumable, PHP-inert. **Rollback:** route off; S8 reaper cleans partial staging.

## S6 — Finalize + hash verify + containment recheck

**Context.** After the plugin signals complete, verify integrity before import.

**Tasks**
- `POST /public/migrations/finalize` (session token). Verify every declared file/chunk present + sha256 matches the manifest; assemble; final containment check of the whole staged tree; DB export reassembled + basic sanity (SQL, not arbitrary).
- Mark payload `staged`. **Do NOT auto-import.** DECISION (A3): import is **operator-gated by default** — the operator/tenant clicks "Import" in S11 after the upload finalizes. This is a **deliberate deviation** from the issue's implied auto-flow (upload→import→verify), chosen because the import is destructive (overwrites a dest docroot + DB) and the source is untrusted; a human authorizes the irreversible step. S11 progress therefore shows `staged — awaiting import`, not a running import. (If product wants auto-import later, gate it behind an explicit per-job "auto-import on finalize" opt-in — do not make it the default.)

**Verify:** missing/mismatched chunk → finalize fails, no import; clean payload → `staged` and STOPS (no import fires). **Exit:** only a hash-verified, contained payload reaches the (operator-triggered) import. **Rollback:** none (read-only verify).

## S7 — Import + config rewrite + serialized-safe URL replace

**Context.** Reuse the existing import runner. Staging → dest home; then WP-specific rewrite.

**Tasks**
- **First validate the import-reuse fits** (do not assume): `migrationImportHomeHandler` was built for a cPanel-style **account-home tree**, but a plugin push is one site's **docroot + DB**. Confirm the staging→dest layout matches; if there's an impedance mismatch (home-tree vs single-site docroot), add a thin `migration.import_wp_docroot` verb instead of forcing the account-home path.
- Trigger the import (existing handler if it fits, else the WP-docroot variant) to move staging → dest docroot (its containment guards apply).
- Import the DB into the dest's Jabali DB (existing DB-import path).
- Rewrite `wp-config.php` to the **Jabali** DB creds + socket (reuse the wp-config rewrite from `wordpress_clone.go` / `setWPConfigCacheConstants` — same trust-boundary openat2 no-symlink pattern).
- **Serialized-safe search-replace** for `domain_change` (old→new URL) — WP-CLI `search-replace` (handles PHP-serialized data) as the tenant, never a raw SQL `REPLACE`.
- Fix perms (tenant:www-data), flush object cache / opcache. Strip source cache drop-ins that point at a foreign Redis prefix (mirror the #621 clone strip).

**Verify:** on a test source→dest, imported site serves; domain-change rewrites serialized option data correctly; wp-config points at Jabali DB. **Exit:** a migrated site is live + correct. **Rollback:** dest is a fresh app; delete + retry.

## S8 — Health verify, cleanup, token revoke

**Tasks**
- HTTP/WP health probe of the migrated site (reuse the `migration.http_probe` / local-nginx `--resolve` fetch pattern). On success → job `done`.
- **Terminal cleanup (any outcome):** wipe the job staging dir, revoke the key + upload-session token, mark `migration_keys.status`. A systemd timer reaps expired/stranded keys + orphan staging (mirror `detectOrphanMigrationStaging`/`orphanMigrationStagingDirs` in `repair.go` + the M22 SSO reaper).

**Verify:** success → healthy + staging gone + token revoked; failure → staging gone + token revoked. **Exit:** no token or payload outlives a terminal state. **Rollback:** n/a.

## S8b — Same-domain preview + safe cutover (before DNS switch)

**Context.** The issue's edge case: a **same-domain** migration (`domain_change=false`) must be **testable on Jabali while DNS still points at the old host** — the operator can't just overwrite and pray. This is a deliverable, not a note.

**Tasks**
- After import (S7), let the operator PREVIEW the migrated site on this box *without* moving DNS: serve it via a temp preview URL and/or the `curl --resolve <domain>:443:<box-ip>` technique already used for warmup (#615) and health probe (#635) — pin the domain to the Jabali box locally for the preview, so the real vhost + FPM run against the migrated data. Surface a "Preview" action + a copy-paste `--resolve`/hosts-file hint in S11.
- Only after the operator confirms the preview do they cut over DNS (out of band). Same-domain import should NOT auto-serve to the world before that confirmation — until cutover, the live domain still resolves to the old host, and Jabali's copy is reachable only via the preview path.
- Domain-change (`domain_change=true`) skips this (the new domain has no prior host); S7's serialized-safe search-replace handles the URL rewrite.

**Verify:** a same-domain migrated site renders correctly via the preview `--resolve` path while public DNS is unchanged. **Exit:** the operator can validate before cutover. **Rollback:** preview is read-only; discard the dest app if wrong.

## S9 — Progress reporting (cross-cuts S3–S8)

**Tasks**
- Plugin reports stage/percent via the session-token endpoints (chunk counters already give upload %); server aggregates onto the job. `GET /migrations/:jobId` (S2) returns live progress; emit M14 events on state changes (mirror the notifications event sources).

**Verify:** progress advances through claim→manifest→upload→import→done in the status API. **Exit:** operator sees live progress. **Rollback:** progress is read-only.

## S10 — The WordPress source plugin (new `wp-plugins/jabali-migrator/`)  ∥ after S4/S5 contract frozen

**Context.** A NEW WP plugin (like `jabali-cache`), installed on the SOURCE site. wp-admin only — no SSH.

**Tasks**
- Admin screen: paste the migration key → claim (S3) → show progress.
- Collect metadata (WP/PHP version, active theme/plugins, table prefix, home/siteurl, upload size estimate, multisite flag) → manifest (S4).
- **DB export**: `WP_CLI` if available else `mysqldump` else a chunked PHP `SELECT` export; split into chunks. Never expose DB creds to the browser.
- **Files**: walk `wp-content` (+ core), send a manifest then file chunks with sha256; ZipArchive if present else pure-PHP chunked (the "disabled ZipArchive / low memory / low max_execution_time" fallback). Resumable via `upload-status`.
- Optional: maintenance mode during final sync. Report progress/errors back.
- Ship to WordPress.org later (like jabali-cache); bundle a fallback in the repo.

**Verify:** on a real shared-host-like WP, plugin claims + streams a full site; resumes after a killed request. **Exit:** a full site migrates from wp-admin only. **Rollback:** plugin is source-side; deactivate.

## S11 — Panel UI: Applications → Migration → WordPress (plugin mode)  ∥ after S2

**Tasks**
- In the Applications Migration flow, add WordPress → **plugin** mode: pick dest user/domain/app + same-or-changing domain; generate key (S2); show install instructions + the one-time key; **live progress** (S9); revoke/rotate. Admin (any) + tenant (own only). Follow the repo Drawer/SearchableTable conventions; `npm run build` + a component test (see the #616 drawer test pattern).

**Verify:** `npm run build` clean; component test locks the wire contract; a manual/Playwright drive of the create→key→progress flow. **Exit:** operator drives the whole flow from the panel. **Rollback:** UI-only.

## S12 — Security hardening + adversarial tests (GATES merge of S3–S7)

**Context.** The public upload surface must be attacked before it ships. This is a gate, not a feature.

**Tasks**
- Adversarial test suite: path traversal (`..`, absolute, encoded, symlink), zip-slip, oversized/over-count payloads, chunk hash forgery, key oracle/timing, claimed-key reuse, cross-tenant dest targeting, PHP-in-staging non-execution, wp-config/drop-in overwrite attempts, resumable-upload race/dedup abuse.
- Confirm size/count/quota caps enforced server-side (never trust the manifest).
- **Non-negotiable:** a real end-to-end run on the test server (source WP → dest) with `X`-style verification, + at least one adversarial payload proven rejected at runtime (unit + `nginx -t`-class checks are NOT sufficient for the upload boundary — cf. the #601/#637 lesson that runtime caught wiring/behavior bugs unit tests missed).

**Verify:** the adversarial suite is green AND a live source→dest migration succeeds on the test box. **Exit:** the public surface is proven hostile-input-safe at runtime. **Rollback:** if any adversarial case fails, S3–S7 do not merge.

---

## Anti-patterns (learned)
- Don't trust the manifest for caps — enforce server-side (S4/S5).
- Don't accept `wp-config.php`/drop-ins/`.user.ini`/`php.ini` raw — quarantine + handle in S7 (S5).
- Don't auto-import on finalize — a human/authorized path runs the destructive step (S6→S7).
- Don't reuse the migration key after claim — session token only (S3).
- Don't skip the runtime adversarial test — the upload boundary is exactly where unit tests lie (S12; cf. #601 `WithWordPressInstalls` dead-wiring caught only at runtime).
- Migration = schema only; no seeding from app-populated tables ([[feedback_migration_data_seed_ordering]]).
