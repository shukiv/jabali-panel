# ADR-0164 — Billing automation endpoints (account lifecycle over the Automation API)

**Status:** Accepted — JAB-190. Blueprint at `plans/billing-automation-endpoints.md`.
**Related:** ADR-0093 (M44 automation tokens), ADR-0157 (JAB-140 write layer),
ADR-0083 (shared ops packages), ADR-0034/M20 (Kratos sole auth).

## Context

The `jabali-integration` repository ships WHMCS/Blesta/WiseCP provisioning
modules. They can suspend/unsuspend today (JAB-140) but a billing panel's
core contract is the account lifecycle: create, terminate, password
rotation, package change, plus usage sync and a package list for product
mapping. ADR-0157 reserved the destructive-verb design
(`delete:<resource>` + `confirm:true` + dry-run) without shipping one.

## Decision

Extend `/api/v1/automation` with billing endpoints, all on the JAB-140
spine (HMAC → scope + writes_enabled → per-token write rate limit →
signed-body validation → audit success/failure):

| Endpoint | Scope | Notes |
|---|---|---|
| `POST /users` | `write:users` | via `userops.Create`; `is_admin` not exposed, forced false; password min-10 enforced in-handler (bindSigned has no gin bindings); `201 {ok,status:"created",user_id,message}` |
| `DELETE /users/:id` | `delete:users` | first destructive verb; requires `confirm:true`; `dry_run:true` = read-only cascade enumeration; refuses admin targets |
| `POST /users/:id/password` | `write:users` | via extracted `userops.RotatePassword`; refuses admin targets |
| `PUT /users/:id/package` | `write:users` | via extracted `userops.SetPackage`; refuses admin targets |
| `GET /users/:id/usage`, `GET /usage` | `read:users` | snapshot-store + batched `BWDaily` reads ONLY — never agent calls (billing panels poll bulk daily) |
| `GET /packages` | `read:packages` (new scope) | thin secrets-free rows for product-mapping dropdowns |
| `GET /users` | `read:users` | grows `?email=&username=` exact-match filters + `suspended` in rows |

**One write path, enforced by extraction.** The GUI's password-rotation,
package-change and delete-cascade logic moves out of `users.go` handlers
into `userops` (`RotatePassword`, `SetPackage`, `DeleteCascade`); REST
handlers and automation handlers both call the extracted functions. The
delete cascade's docker-teardown-failure block (Gitea #532) is preserved
as a typed error both callers map to 409.

**Create is idempotent by target state** — keyed on the **username**
(supplied, or derived from the email the same way `userops.Create`
derives it), which is the panel's only unique identity post-M54. A retry
whose username resolves to an existing user **with the same email**
returns `200 {ok:true,status:"exists",user_id}`; a username collision
with a different email stays `409 conflict`. Email is deliberately NOT
unique (one billing client legitimately owns several accounts) — live-
verified on mx 2026-08-08: same email + new username creates a second
account. Billing modules therefore resolve accounts by stored ULID
first, then username; an email-only lookup can be ambiguous.

**Admin-target refusal is blanket** (create can't mint one; delete/
password/package refuse `is_admin` rows) — a headless server-wide key
never manages admins; last-admin counting stays a GUI concern.

**Login-token SSO deferred.** No one-time panel login exists panel-wide
(Kratos sole auth). Reserved: `users.login_token` capability +
`POST /users/:id/login-token {client_ip, ttl_s}` — mint carries the
END-USER's IP because the billing server mints but the browser redeems.
Needs its own ADR (Kratos session minting) before any implementation.

## Alternatives considered

- **Billing modules call the Kratos-session admin REST API.** Rejected:
  browser-oriented (cookies/CSRF), no scoping, no revocable machine
  credential — exactly what ADR-0093 exists to avoid.
- **Automation handlers duplicate the GUI logic instead of extraction.**
  Rejected: two write paths for destructive ops is how drift ships;
  ADR-0083 already establishes the shared-ops pattern.
- **Bulk usage recomputes live via the agent.** Rejected: daily
  bulk polls from billing panels would hammer the agent; snapshots +
  `computed_at` honesty is enough for billing-grade usage.

## Consequences

- The jabali-integration modules go end-to-end functional (their
  capability feature-detection lights up `users.create`, `users.delete`,
  `users.password`, `users.package`, `usage.read`, `packages.read`).
- `delete:users` becomes live ammunition: operator docs must repeat the
  ADR-0157 write-token guidance (IP allowlist, expiry, `writes_enabled`
  kill switch) for any token holding it.
- `userops` grows three functions with the GUI handlers as thin shells —
  future callers (CLI, migrations) inherit them for free.
- No schema changes; no migration.

## Addendum — JAB-233: `domain` on create is now honored

**Status:** accepted, 2026-08-08. Amends the "domain accepted but not
auto-created in v1" deferral.

`POST /automation/users` now creates the primary domain when the request
carries `domain`. The deferral's real cost was that the ~265-line GUI
domain-creation orchestration lived inline in `domainHandler.create`; it was
extracted to `createDomainOp` (zero-behavior-change; GUI domain regression
tests pass unmodified), which the automation handler now reuses.

Semantics:
- **Scope:** `domain` present requires `write:users` **and** `write:domains`
  (both already in `AllowedAutomationScopes`). Absent → `write:users` only,
  unchanged. New capability `domains.create` (advertised when wired).
- **Order / atomicity:** the domain is pre-validated (scope, FQDN, global
  name-conflict) BEFORE `userops.Create`, so a bad/taken domain fails fast
  (403/400/409) and never leaves a half-created account. Inline SSL is skipped
  on this path — the reconciler bootstraps the cert on its first tick (like
  `jabali domain create`).
- **Failure after the account exists** (agent down, insert race) does NOT roll
  back: `201` + `domain_warning`, audited; the operator retries the domain.
- **Idempotent retry** (`status:"exists"`): create the domain if the user
  doesn't own it, no-op (return its id) if they do, `409` if owned by another.
- **Delete:** unchanged — `userops.DeleteCascade` already purges owned domains.

No schema changes; no migration.
