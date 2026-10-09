# ADR-0144: Scope-restricted user API tokens (RBAC)

**Status:** ACCEPTED (2026-06-21) — ALL phases (1–5) shipped + live-verified on mx.
**Issue:** GH #245
**Commits:** `38810f53` (phase 1+2 DNS+DDNS), `400f96c5` (phase 3+4 — all tenant areas + UI permission grid), `68c2f093` (phase 5 — per-record DDNS)
**Blueprint:** `plans/m245-api-key-rbac.md`

## Context

`user_api_tokens.scopes_json` existed but was never enforced — every token had
full per-user access. A token put in a router for DynDNS could do anything its
owner could (GH #245). The admin automation tokens already had the pattern:
`<action>:<area>` scopes + `RequireScope` middleware.

## Decision

Enforce user-token scopes, **fail-closed**, with backward compatibility:

- **Empty scopes = full access** — every existing token is unchanged, no
  migration. Browser (Kratos cookie) sessions are never scope-checked.
- A token with a **non-empty** scope set is capability-restricted: allowed only
  on a route whose required scope it `Has()`, and 403'd everywhere else.
- Enforcement is one middleware (`EnforceUserTokenScopes`) on the `/api/v1`
  group, keyed on gin's `c.FullPath()` (the registered route *pattern*, so
  `:params` match exactly with no hand-rolled path parsing). A route absent from
  the scope map denies scoped tokens (**fail-closed**) — so unmapped areas are
  safe-by-default during the phased rollout.
- Scope vocabulary: `read:<area>` / `write:<area>` (+ `read:*` / `write:*`
  wildcards via the existing `Has`), plus a narrow `ddns` grant for the DynDNS
  shim only. All 13 areas are now mapped (dns, mail, files, databases, apps,
  domains, cron, ssl, php, ssh, logs, notifications, backups).
- `write:redis` (GH #2003) grants only `POST /me/redis-access/flush`, so an app
  can flush its Redis keys without a full token. There is no `read:redis`:
  `GET /me/redis-access` returns the Redis password, so it stays unmapped and
  scoped tokens are refused there.
- The DDNS shim (`/nic/update`) accepts empty scopes, `ddns`, or `write:dns`;
  everything else is `badauth`. A `ddns`-only token can use the router shim but
  is 403'd on the DNS REST API.
- Token-create validates scopes against the known vocabulary (rejects typos that
  would silently fail-close).

## Why fail-closed + FullPath (not per-route RequireScope)

Per-route `RequireUserScope` is explicit but, during a phased rollout, leaves
not-yet-mapped routes open to scoped tokens — a hole. A central `FullPath`→scope
resolver that denies unmapped routes is fail-closed: a still-unmapped area is
*unusable* by a scoped token rather than *wide open*, and a route/typo mismatch
results in denial, never escalation. A coverage test guards drift.

## Consequences

- Safe DynDNS / router keys today (`ddns` scope), DNS read/write scoping, and a
  UI permission preset — johnnyq's request is met.
- All tenant areas (mail, files, databases, apps, domains, cron, ssl, php, ssh,
  logs, notifications, backups) are mapped (phase 3+4); the UI ships a per-area
  read/write permission grid. Full tokens are unaffected throughout.
- Per-resource scoping ("tie a token to one DNS record") shipped as phase 5:
  a `record:<26-char ULID>` scope constrains a token to updating only the listed
  DNS record(s) via the DDNS shim (`tokenAllowsRecord`). This is johnnyq's exact
  "update one record, not add/delete" request.

## Verification

Live on mx (2026-06-21, via the api unix socket): full token reaches an unmapped
route (200); a `ddns`/`read:dns` scoped token is 403'd on unmapped routes; a
`read:dns` token reads DNS records (200, real route FullPath matches the map)
but the `ddns` token is 403'd on the DNS REST API; `/nic/update` returns a
non-`badauth` result for a `ddns` token and `badauth` for a `read:dns` token.
Unit tests cover the full/scoped × mapped/unmapped × read/write matrix.
