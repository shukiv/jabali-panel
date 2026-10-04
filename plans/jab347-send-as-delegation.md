# Blueprint — Send-as delegation (GH #347)

**Status:** SHIPPED (GH #347). Built as [ADR-0156](../docs/adr/0156-send-as-delegation.md); operator runbook `plans/jab347-send-as-runbook.md`. This blueprint is kept as design history.

**Objective.** Let one login mailbox (`support@`) send email **From** another mailbox's
address (`sales@`, `billing@`) **without** receiving that mailbox's mail. The grantor
mailboxes remain their own physical inboxes and keep delivering to themselves. Enforced
(no spoofing): `support` may send only from its own address, its aliases, and its
*explicitly delegated* senders — nothing else.

**Why the obvious tools don't fit** (already confirmed with the reporter on #347):
- **Aliases** couple send + receive → `support` would *receive* `sales@`'s mail. ✗
- **Mailbox sharing** grants read access to the inbox. ✗
- **Group membership** only grants send-as the *group* address; `sales@`/`billing@` are
  individual mailboxes. ✗

**Feasibility (confirmed, Stalwart 0.16.12 — contrast with the infeasible mailbox-2FA):**
- Sender enforcement = **`mustMatchSender`** (MtaStageAuth, boolean, default `true`): an
  authenticated user may only `MAIL FROM` its own login + associated addresses (aliases).
- **`isSenderAllowed`** (MtaStageMail) is an expression (general acceptance).
- Sieve supports SQL lookups: `query :use "sql" :set [...] "SELECT ... WHERE ?" [...]`.
- Config lives in Stalwart's internal RocksDB store, applied via
  `stalwart-cli update <Object> --field ...` / `--json`, and jabali's ADR-0073 converger
  in `install.sh` `_install_stalwart_apply_plan` (patches `queryRecipient`/`queryEmailAliases`
  on the create-once `Directory`).

**External SQL directory** (jabali runs Stalwart against the panel DB — ADR-0042 mailboxes
are authoritative). `install/stalwart/apply-plan.json.tmpl`:
- `queryLogin` — auth.
- `queryRecipient` — receive resolution (UNIONs mailboxes + mail-group members + forwarders).
- `queryEmailAliases` — the account's aliases → feeds identities / send-as set that
  `mustMatchSender` checks.

**⚠️ This is security-load-bearing.** A bug here = spoofing / open relay.

**✅ STEP 1 SPIKE DONE (2026-07-11, live on `.86`) — mechanism chosen. See
`project_jab347_spike_verdict`.**
- **Mechanism A (`queryEmailAliases` UNION) — DISPROVEN.** Adding a *real mailbox's*
  address to another account's `queryEmailAliases` does NOT grant send-as: Stalwart
  resolves it to its own account (ownership conflict) and rejects. (A genuine non-account
  alias *did* work — but grantors are real mailboxes, so A cannot serve this feature.)
- **Mechanism C (conditional `mustMatchSender` expression) — CHOSEN + PROVEN.**
  `MtaStageAuth.mustMatchSender` is an *expression* (`{"match":{},"else":"true"}`). Make it
  return `false` only for a verified `(delegate = authenticated_as, grantor = sender)` pair,
  `true` otherwise. Live-verified: send-as allowed; delegate→random REJECTED; grantor→delegate
  (reverse) REJECTED; own always allowed; **header `From: grantor` delivered intact**; grantor
  still RECEIVES its own mail (`queryRecipient` untouched); **fails CLOSED** on expr/store error.
  Non-delegated users keep the built-in check fully (expr = `true` for them) — no global
  `mustMatchSender=false`, so **strictly better than the old Mechanism B**; blast radius = one
  delegation pair.
- **`sql_query` data source — RULED OUT.** Stalwart 0.16.12 exposes NO addable named SQL
  store (DataStore/SearchStore are singletons, primary is RocksDB; the directory's embedded
  MySql store and a `StoreLookup(@type MySql)` are both rejected by `sql_query('<id>',…)` as
  *"Store not found or is not a SQL store"*). So the expression cannot query jabali_panel via
  `sql_query`. **Data source = MATERIALIZED expression** (Step 3): the panel regenerates the
  `mustMatchSender` expression enumerating the current `(delegate,grantor)` pairs on every
  change, via `stalwart-cli update MtaStageAuth` (ADR-0073 converger pattern).
- **Operational:** `MtaStage*`/`Directory` config edits need `create Action/ReloadSettings`
  to take effect — NOT `InvalidateCaches`.

---

## Preconditions / conventions
- Codeberg is source of truth; github is a mirror. Branch → codeberg PR → CI (Forgejo,
  4 checks) → merge via Forgejo API → sync `main` to github. See
  `project_codeberg_source_of_truth`.
- Migrations are **schema-only**; data seeds live in the app (migration-data-seed-ordering
  scar). Next free migration on this line = **000218** (verify: highest is 000217).
- The SQL directory user (`jabali-stalwart-ro`) is **read-only** — all writes go through
  the panel. Config must survive fresh installs + `jabali update` (wire through `install.sh`,
  not a one-off CLI — install_sh_is_truth scar).
- Live server for spikes + verification: **192.168.100.86** (`ssh root@192.168.100.86`),
  runs jabali + Stalwart 0.16.12. `.13` is a fresh-install target if needed.
- Every Stalwart claim is a hypothesis until proven on `.86`. Treat docs as hypotheses,
  the live server as truth (mailbox-2FA lesson).

---

## Step 1 — LIVE SPIKE: choose the mechanism ✅ DONE 2026-07-11
**Outcome recorded above + in `project_jab347_spike_verdict`. Mechanism C chosen; A
disproven; `sql_query` ruled out; data source = materialized expression. The original
A-vs-B spike script is retained below as the historical record.**

**Gate for everything else. Nothing is built until this returns a proven mechanism.**

Context brief: We must pick between two mechanisms and prove the winner on the live
Stalwart before writing any panel code.

- **Mechanism A (preferred if it works):** add the delegated sender address to the
  delegate's `queryEmailAliases` result (UNION a delegation query). Then `mustMatchSender`
  (unchanged, still `true`) accepts `support` sending From `sales@`. **Risk:** `sales@`
  becomes both a standalone account (its own `queryRecipient` receive) *and* an
  alias-of-`support` (send). Stalwart's ownership resolution for that ambiguous address is
  **unverified** — it may reroute `sales@`'s inbound mail to `support`, or error.
- **Mechanism B (fallback):** `stalwart-cli update MtaStageAuth --field mustMatchSender=false`
  + a custom session Sieve (mail/data stage) that re-enforces sender ∈
  {own login + aliases + delegated} via `query :use "sql" ...`, rejecting otherwise.
  **Risk:** disables the built-in anti-spoofing; the hand-rolled Sieve is now the only
  guard — a logic bug = open relay.

Tasks:
1. On `.86`, snapshot current config: `stalwart-cli list Directory`, capture the live
   `queryEmailAliases`, and `stalwart-cli get MtaStageAuth` (or equivalent) for
   `mustMatchSender`. Record exact commands + current values so everything is revertible.
2. Create two throwaway test mailboxes in the panel DB on `.86` under a test domain:
   `deleg-a@<testdom>` (delegate) + `deleg-b@<testdom>` (grantor), each with a known
   bcrypt password. `pdns`/domain already provisioned or use an existing test domain.
3. **Test Mechanism A:** temporarily edit the live `Directory`'s `queryEmailAliases`
   (via `stalwart-cli update Directory <id> --json`) to UNION a hardcoded row returning
   `deleg-b@` as an alias of `deleg-a@`. Then:
   - Authenticate as `deleg-a@` over SMTP submission (587, STARTTLS) and issue
     `MAIL FROM:<deleg-b@…>` — **is it accepted?** (mechanism A send-as works)
   - Send a message TO `deleg-b@` (external/loopback) — **does it deliver to `deleg-b@`'s
     own mailbox, or get rerouted to `deleg-a@`?** (the load-bearing ambiguity)
   - Authenticate as `deleg-a@`, `MAIL FROM:<random@…>` — **rejected?** (no spoofing)
   - Check via IMAP/JMAP where `deleg-b@`'s test message landed.
4. If Mechanism A grants send-as **and** `deleg-b@` still receives its own mail **and**
   random senders are rejected → **A wins**; document the exact `queryEmailAliases` UNION
   shape. If A reroutes/errrors → **fall back to B**: set `mustMatchSender=false`, install a
   session Sieve that does the SQL delegation check, and re-run the same three tests until
   send-as works, delegation is enforced (random rejected), and receive is unchanged.

   **CRITICAL — envelope vs header From.** `mustMatchSender` governs the envelope
   `MAIL FROM`, but the reporter cares about the **`From:` header** the recipient sees.
   Step 1 MUST establish: (a) does Stalwart also bind the header `From:` to the authenticated
   account, and (b) does the chosen mechanism make the **header From** `sales@` deliverable
   (not just the envelope)? Test by sending an actual message From `sales@` and inspecting the
   received headers. If Stalwart only checks the envelope, the delegation is insufficient for
   the real use case — flag it before building.

   **CRITICAL — Mechanism B is a GLOBAL change.** `mustMatchSender=false` removes the
   built-in sender check for **every** authenticated mailbox, not just delegates. The Sieve
   then becomes the *sole* sender guard system-wide, so it MUST fully replicate
   `mustMatchSender`'s own+aliases enforcement for **non-delegated** users too — otherwise any
   ordinary mailbox can spoof. Spike must include: a **non-delegated** test mailbox trying
   `MAIL FROM:<someone-else@>` → still **REJECTED** by the Sieve. If this can't be proven
   airtight, prefer Mechanism A or do not ship B.
5. Fully revert: restore the original `queryEmailAliases`/`mustMatchSender`, delete the two
   test mailboxes, purge Stalwart caches if needed. `.86` must be byte-identical to before.

Verification / exit criteria:
- A written record: **which mechanism, the exact `stalwart-cli` commands + config JSON that
  worked, and the three proven behaviours** (send-as allowed; delegation enforced /
  arbitrary sender rejected; grantor receive unchanged).
- `.86` restored to its pre-spike state (config + no test mailboxes).
- Steps 3 + 6 are written against the CHOSEN mechanism; if B, Step 3 also covers the Sieve.

Rollback: none needed (throwaway); ensure `.86` restore is complete.

---

## Step 2 — DB: `mailbox_send_delegations` table + repository · depends: Step 1
Context brief: Persist "delegate mailbox X may send from grantor mailbox Y." Schema-only
migration (data seeds live in the app). Next number **000218** (verify at author time).

Tasks:
1. Migration `000218_create_mailbox_send_delegations.up.sql` / `.down.sql`:
   `id CHAR(26) PK`, `delegate_mailbox_id CHAR(26) NOT NULL` (FK → mailboxes, the login that
   sends), `grantor_mailbox_id CHAR(26) NOT NULL` (FK → mailboxes, whose address is sent
   from), `created_at/updated_at`, `UNIQUE(delegate_mailbox_id, grantor_mailbox_id)`, indexes
   on both FKs, `ON DELETE CASCADE` from both mailboxes. (Store grantor as a mailbox FK, not a
   raw email, so it tracks renames + guarantees the grantor is a real mailbox.)
2. `models/mailbox_send_delegation.go` (GORM tags; watch initialism column tags —
   gorm_column_tags scar).
3. `repository/mailbox_send_delegation_repository.go`: `Create`, `Delete`,
   `ListByDelegate(mailboxID)`, `ListByGrantor(mailboxID)`, `DeleteByPair`. sqlmock test.

Verification: `go build ./...`; repo unit test green; migrate up/down on a scratch DB;
`detect_changes` scope check.

Rollback: `.down.sql` drops the table; no other code depends yet.

---

## Step 3 — Stalwart `mustMatchSender` materialized-expression converger (Mechanism C) · depends: Step 1, Step 2
Context brief: Make Stalwart honor the delegation by setting `MtaStageAuth.mustMatchSender`
to an expression that is `false` **only** for the currently-delegated `(delegate, grantor)`
pairs and `true` for everything else. Because `sql_query` cannot reach jabali_panel
(spike finding), the pair list is **materialized into the expression text** and re-applied
whenever delegations change — the same converge-on-write pattern as ADR-0073.

**Expression shape** (built by the panel, one OR-clause per delegation pair):
```
!(
  (authenticated_as == '<delegateA@dom>' && sender == '<grantorX@dom>') ||
  (authenticated_as == '<delegateB@dom>' && sender == '<grantorY@dom>') ||
  ...
)
```
- Empty delegation set → the whole disjunction is empty → expression must render to the
  literal `true` (NOT `!()`, which is a parse error) — i.e. when there are zero pairs, write
  `{"else":"true","match":{}}` verbatim (byte-identical to the stock value).
- Escape/validate each address (single-quoted string literals; reject any address containing
  `'`, `\`, control chars, or not matching the mailbox's real `email_cached`) — the expression
  is code, so an unescaped address = injection. Only ever emit addresses read back from the
  `mailboxes` table (never user free-text).
- `queryRecipient` / `queryEmailAliases` / `queryLogin` are **UNCHANGED** — grantor receive and
  normal aliases untouched.

**Where it lives (two write paths, same builder):**
1. `install.sh` (`_install_stalwart_apply_plan` neighbourhood): on every install / `jabali
   update`, rebuild the expression from the live `mailbox_send_delegations` table and
   `stalwart-cli update MtaStageAuth --file <patch>` then `create Action/ReloadSettings`.
   Keeps the feature alive across fresh installs (install_sh_is_truth scar). The stock
   apply-plan template value stays `true`; the converger overwrites it post-apply.
2. panel-agent command (e.g. `mail.sendas.reconcile`) the panel-api calls after any
   delegation add/remove (Step 4), doing the same rebuild + `ReloadSettings`. Reuse the
   existing Stalwart-admin client (user `admin`, token `/etc/jabali-panel/stalwart-admin.token`,
   `http://127.0.0.1:8446`). **Idempotent + full-replace** (rebuild the entire expression from
   the table each time — never append), so a missed event self-heals on the next reconcile.

Tasks: shared Go builder `BuildMustMatchSenderExpr(pairs []Pair) string` (unit-tested incl.
empty→`true`, escaping, ordering-stable); wire it into both the panel-agent command and an
install.sh shell path (or have install.sh invoke the agent command — preferred, one
implementation); `bash -n install.sh`; assert the emitted JSON patch is well-formed + the
expression parses (`stalwart-cli update … ` returns ok on `.86`).

Verification: on `.86`, insert a real `mailbox_send_delegations` row, run the reconcile, then
re-run Step 1's live tests against the REAL row → send-as allowed, arbitrary rejected, reverse
rejected, grantor receive unchanged, header From correct, non-delegated unchanged. Confirm
`create Action/ReloadSettings` is what applies it (InvalidateCaches does NOT — spike finding).
Delete the row, reconcile → expression back to `true`, send-as revoked.

**Safety (review findings — fold into impl):**
- **A malformed expression fails closed GLOBALLY.** `mustMatchSender` is evaluated for every
  authenticated submission; a parse error → eval error → *all* submission MAIL FROM rejected
  = mail-send outage for the whole server (not just delegates). Mitigation: before
  `ReloadSettings`, (1) build + `stalwart-cli update` returns ok (it validates the expression
  server-side — a bad expr is rejected at update, seen in the spike), and (2) capture the
  prior `mustMatchSender` value first; if the post-reload smoke (one own-address submission)
  fails, restore the prior value. Never leave a half-applied expression.
- **Expression size scales with delegation count.** One OR-clause per pair; hundreds of pairs
  = a large expression. Fine for the reporter's use case; if it grows, revisit the untested
  lookup-store path (`key_exists` against a StoreLookup) — do NOT silently let it grow
  unbounded (log the pair count on each reconcile; flag a threshold).

Rollback: reconcile with an empty table → expression = `true` (stock). Fully idempotent; the
delegation table can stay.

---

## Step 4 — panel-api: manage delegations · depends: Step 2 (parallel with Step 3)
Context brief: Expose delegation management. Fold into the mailbox update surface or add a
sub-resource; verify the wire envelope against the handler (verify_wire_contract scar).

Tasks:
1. Endpoints (admin-scoped, `mailboxes.go` neighbourhood): `GET
   /admin/mailboxes/:id/send-as` (list grantors), `POST` (add a grantor by mailbox id/email),
   `DELETE /admin/mailboxes/:id/send-as/:grantorId`. OR fold a `send_as: []` array into
   `updateMailboxRequest` (nil = leave; explicit = replace-set) — pick one, keep it consistent
   with the existing partial-update convention.
2. Validation: delegate + grantor must be real, enabled mailboxes; policy decision — restrict
   grantor to the SAME account/user or same domain (default: same domain; surface in the ADR).
   Reject self-delegation and duplicates. Never let a scope-restricted token widen send-as.
3. Audit each add/remove (AuditRecord). No agent call needed — the directory query reads the
   table live.

Verification: handler unit tests (add/remove/list, cross-domain reject, self reject);
`go build`; envelope shape matches what panel-ui reads.

Rollback: remove routes; table + directory query unaffected.

---

## Step 5 — panel-ui: "Can send as" on the mailbox form · depends: Step 4
Context brief: AntD add/edit mailbox form gets a **"Can send as"** multi-select of other
mailbox addresses (same domain), per johnnyq's suggestion. Follow repo AntD conventions
(SearchableTable/Drawer, `npm run build`/`tsc -b` not `--noEmit` — panel_ui_use_npm_run_build
scar; verify the API envelope, not the blueprint's assumption).

Tasks: add the field to the mailbox create/edit Drawer; load candidate grantors (other
mailboxes in the domain) via the list API; submit the delegation set; show current delegations
on the mailbox row/detail. Verify visually on `.86` (chrome-devtools/claude-in-chrome
screenshot) after wiring.

Verification: `tsc -b` clean; `npm run build`; screenshot the form; add a delegation in the UI
and confirm the row lands in `mailbox_send_delegations`.

Rollback: hide the field (feature-flag or revert the component change).

---

## Step 6 — Live e2e verify on `.86` + ADR + runbook + issue · depends: Steps 3,4,5
Context brief: Prove the whole feature end-to-end on real Stalwart; document; close the loop
on #347.

Tasks:
1. On `.86`: create `support@`, `sales@`, `billing@` under a test domain; via the UI add
   `sales@`+`billing@` as `support`'s send-as grantors.
2. Hard gate (all must hold):
   - Authenticated as `support`, `MAIL FROM:<sales@>` and `<billing@>` → **ACCEPTED**, message
     delivers out with the right From.
   - Authenticated as `support`, `MAIL FROM:<notdelegated@>` → **REJECTED** (no spoofing).
   - Mail TO `sales@` → lands in **`sales@`'s own inbox**, not support's.
   - Remove the delegation → `support` can no longer send From `sales@`.
   - The **`From:` header** the recipient sees is `sales@` (not just the envelope).
   - A **separate non-delegated** mailbox still **cannot** send From anyone else's address
     (proves the materialized expression left `mustMatchSender = true` for non-delegates —
     no global spoofing hole).
   - Add a SECOND delegation pair, reconcile → both pairs work and the expression rebuilt
     cleanly (multi-pair disjunction, no stale first pair) — catches append-vs-replace bugs.
3. ADR (next number) recording the chosen mechanism + the security rationale + why not
   aliases/sharing/groups. Runbook `plans/jab347-send-as-runbook.md`. Comment on GH #347 with
   the shipped behaviour + how to configure it in the UI.

Verification: the four gate conditions demonstrated on `.86` (capture the SMTP transcript +
where mail landed). ADR + runbook committed. Issue comment posted.

Rollback: the feature is additive (no delegation rows = prior behaviour); to fully back out,
revert Step 3's directory/config change via the converger.

---

## Dependency graph
```
Step 1 (spike) ─▶ Step 2 (db) ─▶ Step 3 (stalwart)  ─┐
                        └──────▶ Step 4 (api) ─▶ Step 5 (ui) ─┴─▶ Step 6 (verify+docs)
```
Serial spine is Step 1 → 2 → 3 and 2 → 4 → 5; Steps 3 and 4/5 can proceed in parallel after
Step 2 (no shared files — Step 3 is install.sh/apply-plan, Step 4/5 are api/ui). Step 6 joins
all. Step 1 is strongest-model + blocks everything.

## Invariants (verify after every step)
- Non-delegated mailboxes' auth + send + receive are byte-for-byte unchanged.
- Delegation is *enforced*: a delegate cannot send from an address it wasn't granted.
- The grantor mailbox's inbound routing is unchanged (it still receives its own mail).
- Config survives a fresh install and `jabali update` (it's in install.sh, not a one-off).
- No secret/PII in logs; SQL directory user stays read-only.
