# Blueprint — Write-automation endpoints + write scopes (JAB-140)

**Status:** SHIPPED. Built as [ADR-0157](../docs/adr/0157-automation-write-endpoints.md); operator runbook `plans/jab140-write-automation-runbook.md`. This blueprint is kept as design history.

**Objective.** Add WRITE remediation endpoints + write scopes to the existing public
Automation API (M44) for the Jabali Sounder control plane's M2 milestone. Fully
**additive** — the read-only `/api/v1/automation/*` surface, its tokens, and its
envelopes stay byte-for-byte unchanged. Reversible actions only in M2 (no delete).

**Reporter:** QA team (Sounder). Full spec also in the Sounder repo at
`docs/M2-write-automation-spec.md` (out of this repo; the issue text is authoritative here).

## Grounding facts (recon done — do NOT re-discover)

- **API:** `panel-api/internal/api/automation.go` — `RegisterAutomation(rg, AutomationConfig)`
  mounts `/automation/*` behind `middleware.RequireAutomationHMAC(tokens, key, redis)`;
  each route wraps `middleware.RequireScope("read:X")`. List envelope: `{data, total}`.
- **HMAC middleware** (`middleware/automation_hmac.go`): `Authorization: Jabali-HMAC
  kid=<token-id>, ts=<unix>, sig=<hex>`; verifies sig, ts window, and a Redis SETNX
  replay nonce (`automation:replay:<kid>:<sig>`); stashes the token in ctx
  (`AutomationToken(c)`, `tok.ID` == kid). Reuse verbatim for writes.
- **Scope model** (`models/automation_token.go`, mig 000116, `scopes_json`):
  `AutomationScopes.Has(want)` wildcard is **prefix-scoped** — `read:*` matches only
  `read:X`, so it does NOT imply `write:X`. **Security requirement already met**; a
  symmetric `write:*` is all that's needed. `AllowedAutomationScopes` is the validation
  source of truth; `HasReadWildcardConflict` + `NormalizeScopes` handle `read:*` dedup and
  must be extended symmetrically for `write:*`.
- **`RequireScope` emits `{"error":"scope … not granted"}`** — NOT the JAB-140 envelope.
  Writes need a new guard (`RequireWriteScope`) emitting `{ok:false,error:"scope_denied"}`
  and also enforcing the per-token `writes_enabled` master switch.
- **ids match:** `/automation/users` returns `user.ID`, `/automation/domains` returns
  `domain.ID` — the same DB ids the write endpoints take.
- **Reuse these write MECHANISMS (don't reinvent):**
  - service restart → agent `service.restart`; `admin_services.go` maps `restart`→
    `service.restart` and enforces a **restartable-service allowlist** (masked/critical
    services never restartable) — reuse that allowlist.
  - cache purge → agent `nginx.cache.purge` `{domain}` (per-domain) / `{scope:all}`.
  - user disable/enable → user repo `SetSuspended(ctx, id, bool, reason)`.
  - domain suspend/unsuspend → `is_quota_suspended` via domain `SetSuspended`.
  - backup trigger → M30 backup-run path (`backups.go`, agent backup run) — the only
    genuinely async action.
- **Audit** (`internal/audit` + `AuditEventRepository.Create(models.AuditEvent{...})`):
  every write MUST write an audit row `{actor=token kid, action, target, result, ts}`.
- **Rate limit:** `middleware/ratelimit.go` exists — reuse for a per-token write limiter.

## Preconditions / conventions

- Codeberg is source of truth; branch → codeberg PR → CI (Forgejo, 4 checks) → merge via
  API → sync `main` to github (`project_codeberg_source_of_truth`). See the just-shipped
  JAB-347 (`project_jab347_spike_verdict`) for the exact flow + panel↔agent split.
- Migrations schema-only (`migration_data_seed_ordering` scar); next free number is
  **000219** (verify: highest is 000218 `mailbox_send_delegations`).
- Pin GORM `column:` tags on initialism fields (`TokenID`→`token_id`, `OperationID`)
  (`gorm_column_tags` scar).
- Client/consumer reads the REAL handler envelope (`verify_wire_contract` scar) — the
  capabilities endpoint + Sounder must match what handlers actually emit.
- Config must survive fresh install + `jabali update` (wire through code/app.go, not a
  one-off; `install_sh_is_truth`).
- Live server for verification: **192.168.100.86** (jabali + Stalwart + Redis).

## Shared contract (all write endpoints)

- **Success:** `200 {ok:true, status:"done", message}` (sync) or
  `202 {ok:true, operation_id, status:"pending", message}` (async).
- **Error:** `{ok:false, error:<code>, message}` with `code ∈ {scope_denied, not_found,
  conflict, unsupported, rate_limited, internal}`. (`writes_disabled` → reuse
  `scope_denied` per the issue's enumerated set, message distinguishes.)
- **Idempotent:** disabling an already-disabled user, suspending a suspended domain, etc.
  → `200 {ok:true, status:"done", message:"already …"}`, never an error.
- Deny by default; keep replay protection; rate-limit writes; audit every write.

---

## Security hardening (MUST-FIX — folded from adversarial review)

**A write token is a headless, server-wide admin key.** Automation tokens have no
per-tenant scoping: a `write:users`/`write:domains` token can act on ANY tenant. This is
a static bearer secret with admin power, so the whole feature is built to that threat model.

1. **Replay defense — tighten before the first write route (write replay = duplicate
   create/action, unlike a harmless read re-read):**
   - **Reject future timestamps.** Accept only `now-5m .. now+30s`. AND/OR set the replay
     nonce TTL = `2×maxSkew` so the key outlives the entire acceptance window (a nonce must
     never expire while its signature is still time-valid).
   - **Idempotency keys for non-idempotent writes.** POST creates/actions aren't naturally
     idempotent — a network retry double-fires. Require an `Idempotency-Key` (or reuse the
     signature as the key) recorded in Redis/`automation_operations`; a repeat returns the
     first result, not a second action. State-toggle writes (disable/suspend) are already
     idempotent by target-state check; creates (backups) MUST use the key.
   - **Redis-down fail-closes `503`** (already true) — now load-bearing; keep it.
2. **Split write vs delete scopes.** `write:<resource>` is distinct from `read:<resource>`
   AND from `delete:<resource>`. `write:*` must NOT imply `delete:*`. M2 ships **no**
   destructive verb, but reserve `delete:*` + the per-resource `delete:` scopes now so a
   create-capable monitoring token can never cascade-delete later.
3. **Treat write tokens as admin-equivalent.** Per-token **IP allowlist** (optional CIDR
   set; empty = any), shorter default **expiry**, and a **rotation runbook**. (Per-tenant
   token scoping is a larger follow-up — note it; M2 = admin-equivalent + IP allowlist +
   expiry + `writes_enabled` master switch.)
4. **Validate at the boundary.** Writes validate their body with the SAME schema/validators
   the cookie API uses; rule violation → `422`. GORM parameterised queries only — no
   hand-rolled SQL, no parallel write path.
5. **One write path.** Route every write through the SAME repository/service + reconciler
   (DB-is-truth) the GUI uses — never bypass reconciler convergence or the agent dispatch
   contract. Automation is a thin auth+audit shell over the existing service methods.
6. **Read the SIGNED body, not a re-read.** The HMAC covers `method‖URI‖ts‖sha256(body)`.
   The middleware must buffer the raw body, verify the hash, stash the bytes in ctx, and
   handlers must parse THOSE bytes — never `c.ShouldBindJSON` a fresh stream (validated ==
   signed). This likely needs a middleware change (stash `rawBody`) + a `bindSigned(c,&v)`
   helper; verify no handler re-reads the stream.
7. **Destructive path (future, not M2).** DELETE user/domain cascades `/home` + DBs +
   mailboxes. When it lands: dedicated `delete:<resource>` scope + `confirm:true` body +
   mandatory audit + optional `dry_run:true`. Ship nothing destructive in M2.
8. **Rate limit + concurrency cap** per token on writes (read flood is cheap; write flood =
   provisioning storms). `429 rate_limited` on trip.
9. **Audit every write** to the M49 unified log: `{actor=token id, action, target, client
   IP, request id, result, ts}` — on success AND failure.
10. **Notify high-impact writes** (suspend/disable, and future deletes) via the same M14
    events the GUI fires, so operators see automation-driven changes.

**Adversarial tests (before merge, in the relevant steps):** replay a captured write → `401`;
`read:*` token → write endpoint `403`; `write:x` token → a read endpoint outside its scope
`403`; same POST-create twice (idempotency) → ONE resource; malformed + oversized body →
`422`/`413`; `writes_enabled=false` → `403`; wildcard-scope blast-radius sanity.

**Unchanged / already correct (do NOT touch):** HMAC integrity (`method‖URI‖ts‖sha256(body)`),
constant-time compare, encrypted reveal-once secret, no cookie/CSRF path (HMAC exemption is
correct).

---

## Step 1 — Scopes + `writes_enabled` + operations table (foundation) · strongest model
**Depends: none. Blocks all others.**

Context brief: the data + scope layer. Additive migration + scope-list changes.

Tasks:
1. `models/automation_token.go`: add the 5 write scopes + `write:*` to
   `AllowedAutomationScopes`; **also reserve the distinct `delete:*` + per-resource
   `delete:<resource>` scopes** (validatable now, no endpoint uses them in M2) so `write:*`
   can never satisfy a future delete. Extend `HasReadWildcardConflict` → flag `write:*`+
   `write:child` AND `delete:*`+`delete:child`; extend `NormalizeScopes` symmetrically. Add
   `WritesEnabled bool` (`column:writes_enabled`, default true), `ExpiresAt *time.Time`
   (short default for write tokens; nil = read-only legacy), and `IPAllowlist` (CIDR set,
   `column:ip_allowlist_json`; empty = any) to the token model.
2. Migration **000219** (schema only):
   - `ALTER TABLE automation_tokens ADD COLUMN writes_enabled TINYINT(1) NOT NULL DEFAULT 1,
     ADD COLUMN ip_allowlist_json JSON NULL, ADD COLUMN expires_at DATETIME(6) NULL`
     (existing read tokens unaffected: writes_enabled only gates write scopes, allowlist/
     expiry NULL = unchanged behaviour).
   - `CREATE TABLE automation_operations` (`id CHAR(26) PK`, `token_id CHAR(26) NOT NULL`,
     `idempotency_key VARCHAR(80) NULL`, `action VARCHAR(64)`, `target VARCHAR(255)`,
     `status ENUM('pending','running','done','error')`, `message VARCHAR(512)`,
     `created_at`/`updated_at DATETIME(6)`, `KEY ix_ao_token (token_id)`,
     `UNIQUE KEY uq_ao_idem (token_id, idempotency_key)`). No FK to tokens (revocable —
     keep op history). `.down.sql` drops the table + the three columns.
3. `models/automation_operation.go` + `repository/automation_operation_repository.go`:
   `Create`, `FindByID`, `FindByIdempotencyKey(tokenID, key)`, `SetStatus(id, status, message)`.
   sqlmock tests.
4. Extend the automation-token repo tests for the new scopes + `writes_enabled`/`expires_at`/
   `ip_allowlist` round-trip.

Verify: `go build ./...`; migrate up/down on scratch DB; scope-list unit tests
(`read:*` does NOT match `write:services`; `write:*` matches `write:backups` but NOT
`delete:users`; normalize/conflict symmetric across read/write/delete); `detect_changes`.

Rollback: `.down.sql`; scope-list additions are backward-compatible (no existing token
holds a write scope).

---

## Step 2 — Write guard + replay-hardening + signed-body + audit/rate-limit · depends: Step 1 · strongest model
Context brief: the shared write-request pipeline every endpoint reuses. This is the
security spine — the must-fixes live here.

Tasks:
1. **Tighten the HMAC middleware for writes** (`automation_hmac.go`):
   - **Reject future timestamps** — accept only `now-5m .. now+30s`; AND set the replay
     nonce TTL = `2×maxSkew` so the nonce outlives the acceptance window.
   - **Stash the SIGNED raw body** in ctx (the exact bytes hashed into the signature) and
     add `bindSigned(c, &v)` that unmarshals THOSE bytes — no handler may `c.ShouldBindJSON`
     a fresh stream (validated == signed). Enforce a max body size (`413` over cap).
   - Enforce the token's **IP allowlist** (client IP ∈ CIDR set, empty = any) and
     **`expires_at`** (expired → `401`). Redis-down still fail-closes `503` (keep).
2. `RequireWriteScope(scope)`: after HMAC, require `tok.Scopes.Has(scope)` AND
   `tok.WritesEnabled`; else `403 {ok:false, error:"scope_denied", message}` (message
   distinguishes missing-scope vs writes-paused). Deny by default.
3. **Idempotency:** `withIdempotency(c, tok, action, target, fn)` — for non-idempotent
   creates, look up `automation_operations` by `(token_id, Idempotency-Key || signature)`;
   a hit returns the recorded result instead of re-running `fn`. State-toggle writes skip
   this (idempotent by target-state check).
4. Per-token write **rate limiter + concurrency cap** (reuse `middleware/ratelimit.go`,
   keyed by `tok.ID`); trip → `429 {ok:false, error:"rate_limited", message}`.
5. Response helpers `writeOK/writeAsync/writeErr` emitting the shared contract exactly.
6. `auditWrite(ctx, tok, action, target, result)` → `AuditEventRepository.Create`,
   **M49 unified fields**: `{actor=token kid, action, target, client IP, request id, result,
   ts}`. Called on every write — success AND failure.
7. `notifyWrite(...)` hook for high-impact actions (suspend/disable) → the same M14 event the
   GUI fires. (No-op for low-impact like cache purge.)

Verify: unit tests — future-ts rejected; replayed signed request → `401`; `bindSigned`
parses the buffered bytes (a body swapped after signing fails the hash); scope granted/denied;
`writes_enabled=false` → denied; expired token → `401`; IP outside allowlist → `401`;
rate-limit trip; oversized body → `413`; audit row carries IP+request-id. `go build`.

Rollback: middleware change is guarded to the write group; helpers are new files unused
until Steps 3–4.

---

## Step 3 — Synchronous write endpoints (services / users / domains / cache) · depends: Step 2
Context brief: the four fast, reversible actions. Each: HMAC → `RequireWriteScope` →
rate-limit → `bindSigned`+validate body (`422` on rule violation) → do action **through the
same repository/service the GUI uses** (DB-is-truth → reconciler/agent converges; NO parallel
write path, NO hand-rolled SQL) → audit → notify (if high-impact) → `200 {ok:true,
status:"done"}`. Idempotent by target-state check. Register under the same `/automation`
group, gated on the relevant repo/agent being non-nil (mirror the read routes' nil-guard →
route absent).

Tasks:
1. `POST /automation/services/:name/restart` (`write:services`): validate `:name` against
   the **existing restartable-service allowlist** (unknown/masked → `{ok:false,
   error:"unsupported"}`); agent `service.restart`; audit.
2. `POST /automation/users/:id/{disable,enable}` (`write:users`): `Users.FindByID` (404
   `not_found`); `SetSuspended(id, true/false, "automation:<kid>")` (the same repo method the
   GUI calls); idempotent no-op if already in state; audit + **M14 notify**. Never allow
   disabling an admin via automation → `{ok:false, error:"conflict"}` (safety; ADR).
3. `POST /automation/domains/:id/{suspend,unsuspend}` (`write:domains`): domain `SetSuspended`
   on `is_quota_suspended` (same service path → reconciler converges nginx); idempotent;
   audit + **M14 notify**.
4. `POST /automation/cache/purge` (`write:cache`): body `{scope:"all"|"domain", domain?}`;
   `scope:"domain"` requires a resolvable domain (404 else); agent `nginx.cache.purge`
   `{domain}` or `{scope:all}`; audit.

Verify: handler tests per endpoint (success + audit call, scope_denied via read-only token,
idempotent no-op, not_found, allowlist reject, admin-user conflict). `go build`. Envelope
matches the shared contract exactly (`verify_wire_contract`).

Rollback: routes are additive; remove the registrations.

---

## Step 4 — Async backups + operations polling · depends: Step 2 (parallel with Step 3)
Context brief: the one long-running action + its polling surface.

Tasks:
1. `POST /automation/backups` (`write:backups`): **idempotency-key-guarded** create — if an
   op already exists for `(token_id, Idempotency-Key || signature)`, return its
   `operation_id` (no second backup). Else create an `automation_operations` row
   (`status:pending`, action `backup`, target = scope, the idem key); kick the M30 backup-run
   path in a goroutine that flips the op `running`→`done`/`error` with a message; return
   `202 {ok:true, operation_id, status:"pending"}`; audit the enqueue.
2. `GET /automation/operations/:id`: any valid automation token may poll (no extra scope —
   it only reveals status of an op the caller could create); `FindByID` (404 `not_found`);
   return `{ok:true, operation_id, status, message}`. Do NOT leak cross-token ops → scope
   the lookup to `token_id == tok.ID` (404 otherwise).
3. Bound concurrency / dedupe: a second identical backup while one is `pending/running`
   for the same token+target → `409 {ok:false, error:"conflict"}` (idempotent-ish).

Verify: handler tests — enqueue returns 202 + op row; poll transitions
pending→done; cross-token poll → 404; duplicate → conflict. `go build`.

Rollback: additive routes + the Step-1 table.

---

## Step 5 — Capabilities + wiring + admin token UI · depends: Steps 3, 4
Context brief: discovery, DI wiring, and operator control of write tokens.

Tasks:
1. `GET /automation/capabilities` (any valid token): returns the list of MOUNTED write
   actions with `{action, method, path, scope, async:bool}`, computed from which repos/agent
   are non-nil (feature-off → action omitted, mirroring the read routes). Sounder reads this
   to show only available actions + stay forward-compatible.
2. `AutomationConfig`: add `Operations` repo + any missing write repos (Users already has
   `SetSuspended`; ensure Domains/Backups wired); wire in `app.go`. Existing read wiring
   unchanged.
3. panel-ui admin token management (`admin_automation_tokens` surface): allow selecting
   write scopes in the create/edit form + a `writes_enabled` toggle; show the scope set on
   the token row. `tsc -b` clean; screenshot on `.86`.

Verify: capabilities reflects mounted set (toggle a repo nil in a test → action drops);
UI builds + a write-scoped token can be minted from the panel. `go build`, `tsc -b`.

Rollback: hide the UI field; capabilities + routes are additive.

---

## Step 6 — Live e2e on `.86` + ADR + runbook + issue · depends: Steps 3,4,5
Context brief: prove the whole write path on the real server; document; close JAB-140.

Tasks:
1. On `.86`: mint a token with `write:services,write:cache` (via panel). HMAC-sign
   (`kid,ts,sig`) requests and demonstrate the hard gate:
   - `POST /automation/services/nginx/restart` with the write token → `200 ok:true` AND an
     audit row `{actor=kid, action, target=nginx, result=ok}` exists.
   - The SAME endpoint with a `read:*` token → `403 {ok:false, error:"scope_denied"}`.
   - `POST /automation/cache/purge {scope:all}` → `200`; audit row written.
   - `POST /automation/backups` → `202 {operation_id}`; poll `GET /operations/:id` →
     eventually `done`.
   - Flip the token's `writes_enabled=false` → the write endpoint now `403 scope_denied`
     (master switch), while read endpoints still `200`.
   - Replay the exact signed write request → rejected by the nonce gate.
2. ADR (next number, verify) recording: additive write layer, prefix-scoped wildcard
   (no read→write leak), reversible-only-in-M2 + the future `confirm:true` destructive
   path, `writes_enabled` master switch, audit-every-write, rate limiting.
3. Runbook `plans/jab140-write-automation-runbook.md` (mint a write token, sign a request,
   the capability list, troubleshooting scope_denied/rate_limited).
4. Comment on JAB-140 (Plane) with the shipped surface + a signed-request example; note the
   answered open questions (ids match; allowlist reused; async only for backups; scopes as
   specified).

Verify: all gate conditions demonstrated on `.86` (capture the signed request + response +
audit row). ADR + runbook committed. Issue comment posted.

Rollback: the whole feature is additive — no token holds a write scope until an admin
grants one, so disabling = revoke write scopes / set `writes_enabled=false`, or drop the
routes.

---

## Dependency graph
```
Step 1 (scopes+ops table) ─▶ Step 2 (write guard/helpers) ─▶ Step 3 (sync endpoints) ─┐
                                                          └▶ Step 4 (async backups) ───┴─▶ Step 5 (caps+wiring+UI) ─▶ Step 6 (verify+docs)
```
Serial spine 1→2, then 3 and 4 parallel (no shared files: 3 = services/users/domains/cache
handlers, 4 = backups/operations handlers), joined by 5. Step 1 is strongest-model (scope
security + schema).

## Invariants (verify after every step)
- The read-only surface (routes, tokens, envelopes) is byte-for-byte unchanged.
- `read:*` (and any `read:X`) can NEVER satisfy a write scope; `write:*` can NEVER satisfy
  `delete:*`; deny by default.
- No irreversible action ships in M2 (no delete / destructive verb).
- **One write path:** every mutation goes through the same repository/service + reconciler
  the GUI uses — no parallel path, no hand-rolled SQL.
- **Handlers parse the SIGNED body bytes** (`bindSigned`), never a re-read stream; bodies are
  validated (`422`) and size-capped (`413`).
- Replay defense: future timestamps rejected, nonce TTL ≥ 2×maxSkew, non-idempotent creates
  guarded by idempotency key (same POST twice → one resource); Redis-down → `503`.
- Every write produces exactly one audit row (success or failure) with actor=token kid,
  client IP, request id; high-impact writes also fire the M14 notification.
- Per-token write rate limit + concurrency cap active; `writes_enabled=false`, expiry, and
  IP allowlist each independently gate writes while leaving reads working.
- Write tokens are treated as admin-equivalent (rotation runbook, IP allowlist, expiry).
- Config survives fresh install + `jabali update` (no one-off wiring).
