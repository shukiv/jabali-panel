# ADR-0128: Admin "act-as" impersonation via effective-user override (not session swap)

**Date**: 2026-06-15
**Status**: Accepted
**Deciders**: shuki + Claude
**Related**: ADR-0015 (legacy JWT `impersonated_by`, removed), ADR-0034 (M20 Kratos is the only auth source), ADR-0106 (M49 unified audit). Fixes GH #183.

## Context

Admins need to "log in as a user" to see/fix the user's panel. The legacy
mechanism (ADR-0015) rode on the panel's own JWT with an `impersonated_by`
claim; it died when M20 (ADR-0034) made the Kratos cookie the **only**
identity source. Today `RequireKratosSession` derives identity solely from
`ory_kratos_session` → Whoami → panel user, with no override.

So the only way to "be" a user is to put that user's Kratos session cookie
in the browser, which **overwrites the admin's own cookie** → the admin is
logged out of their account and needs a second browser (GH #183). Kratos
has no native "act-as"; minting a Kratos session for the target still
collides on the single per-browser cookie.

## Decision

Impersonation is an **authorization-layer effective-user override**, not a
session/authentication swap. The Kratos cookie still authenticates the
admin (ADR-0034 intact); we only change which user's **data** an
already-authenticated admin operates on. The admin's cookie is never
touched → no logout, no second browser.

**Mechanism.** Every user-scoped handler already scopes on
`claims.UserID` with an admin bypass (`if !claims.IsAdmin && res.UserID !=
claims.UserID`). A middleware mounted **after** `RequireKratosSession`
swaps the effective claims for an authenticated admin who presents a valid
grant: `UserID = target`, `IsAdmin = false`, `ImpersonatedBy = admin`. All
user endpoints then transparently serve the target — **zero per-handler
changes**.

**Grant model (server-side, revocable, time-boxed).** A
`POST /api/v1/admin/impersonation {user_id}` (RequireAdmin) creates an
`impersonation_grants` row (admin_user_id, target_user_id, expires_at) and
returns the grant id. The SPA carries it in `X-Jabali-Act-As: <grant id>`.
`DELETE /api/v1/admin/impersonation/:id` ends it. A reaper expires stale
rows. The grant row (not a bare header) is chosen so impersonation is
**revocable** server-side, **time-boxed** by `expires_at`, and leaves a
durable started/ended audit trail.

## Security model (load-bearing)

1. **Initiation is admin-only and unforgeable.** The grant is created only
   under `RequireAdmin`; the override middleware acts **only** when the
   *real* Kratos-cookie claims are `IsAdmin` AND
   `grant.admin_user_id == realClaims.UserID`. A tenant cannot set the
   header to gain anything — without an admin's live cookie it is ignored.
   A leaked grant id is useless without that exact admin's session.
2. **No privilege escalation.** Admins can already read any user's
   resources via the admin bypass; act-as is nicer UX over access they
   already have. During act-as the effective `IsAdmin=false`, so
   `RequireAdmin` endpoints 403 and the IDOR check confines the admin to
   **exactly the target** — no hopping to a third user. Admin actions
   require exiting act-as.
3. **Target must be non-admin.** An admin may not act-as another admin
   (no scoping into a higher-privileged account).
4. **Accountability — the real admin stays the audited actor.** The
   override sets `ImpersonatedBy`; the M49 audit middleware records
   `actor = ImpersonatedBy` (the real admin) with `acting_as = target` +
   an impersonation flag on **every** request. The target is never logged
   as the actor. Grant create/end emit explicit audit events.
5. **Reveal-once secrets unaffected.** Stored secrets are never re-readable
   by design; act-as cannot expose them. Only freshly-generated-on-create
   secrets are shown (inherent to the operation, audited).
6. **Always-visible + instant exit.** The SPA shows a persistent "Viewing
   as <user> — Exit" banner; exit DELETEs the grant + clears sessionStorage.
   An expired/revoked grant returns a specific 4xx so the SPA drops back to
   the admin view instead of silently acting as admin.

### Exception: per-domain PHP settings policy (GH #1701)

`GET/PATCH /domains/:id/php-settings` treats an act-as request as an admin
when it applies the domain owner's package PHP settings policy
(`phpPolicyAdmin` in `internal/api/domain_php_settings.go`): the admin may
set a directive the owner's package keeps for admins. There is no admin-side
page for a domain's PHP settings, so act-as is the only UI path an admin has
to that surface, and without this exception an admin could change an
admin-only directive only through the CLI.

The exception is narrow:
- It covers this one handler and only the package-policy check. The IDOR
  check still confines the request to the target's own domains, and every
  other rule in this section is unchanged: `RequireAdmin` endpoints still
  403 during act-as.
- It grants nothing an admin cannot already do: admins set these directives
  through the CLI.
- `ImpersonatedBy` is set only by `ResolveImpersonation`, after checks 1 and
  3 above, and the request is audited with the real admin as actor (4).

Do not "fix" this handler to match point 2 without first giving admins
another way to set admin-only PHP directives from the panel.

This is a deliberate, audited **carve-out around ADR-0034**: Kratos remains
the sole *authenticator*; impersonation is an *authorization* overlay for
an already-authenticated admin, fully logged and reversible.

## Alternatives Considered

- **Kratos session swap / admin-minted session** — still one cookie per
  browser → the GH #183 collision persists; Kratos has no act-as. Rejected.
- **Bare header, no server row** — not revocable, not server-time-boxed,
  weaker audit anchor. Rejected for a read-write feature.
- **Per-user admin endpoints** (admin lists user Y's domains/dbs/…) —
  would require new admin variants of every user list endpoint and a
  parallel UI. The effective-user override reuses all existing user
  endpoints + the user SPA. Rejected as higher-cost, lower-fidelity.

## Consequences

### Positive
- Fixes GH #183: admin stays logged in as admin, no second browser, instant
  enter/exit, full read-write on the target's panel.
- Tiny backend surface (one table + two endpoints + one middleware + a
  claims field + audit wiring); zero changes to user handlers.

### Negative
- Read-write act-as lets an admin mutate a user's resources as the user —
  bounded by `expires_at`, revocable, and fully audited (real admin as
  actor).
- One more middleware in the per-request chain (a cheap no-op when no
  `X-Jabali-Act-As` header is present).

## Implementation

Blueprint: `plans/m-admin-impersonation.md`. Migration `000167`
(`impersonation_grants`). `auth.AccessClaims.ImpersonatedBy` added;
`middleware.ResolveImpersonation`; `internal/api/impersonation.go`;
audit middleware actor = `ImpersonatedBy ?? UserID`; SPA "act-as" banner +
apiClient header. Branch `feat/admin-impersonation` — thorough tests +
live verify before merge.
