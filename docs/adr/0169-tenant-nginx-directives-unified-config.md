# ADR-0169: Tenant nginx directives + one config model (builder as source of truth)

**Status:** Proposed (2026-09-10) — design spike, no code. Each phase below is gated on separate approval.
**Driven by:** GH #1624.
**Related:** ADR-0009 (nginx file-per-vhost), ADR-0055 (ModSecurity per-domain), ADR-0088 (Snuffleupagus PHP hardening). Work referenced: JAB-65/JAB-66 (SSRF-validated Rule Builder + directive allowlist), GH #1580 (admin relaxed denylist), GH #307 (`TenantDomainOptionsEnabled`).

## Context

#1624 asks for two things:

1. Expose an **advanced nginx directives** surface to tenants, limited to a safe
   subset (rewrites, redirects, headers, locations), so a tenant migrating from
   another panel can paste working snippets instead of rebuilding them in the UI.
2. Unify the **builder** and the **advanced directives** into a single underlying
   config model, so a directive added by an admin is visible/editable in the
   tenant builder rather than living in a second, invisible store.

Neither is a config flip. Raw per-domain nginx is a hardening boundary, and the
"one model" ask runs into the fact that a raw directive blob cannot be reliably
reverse-parsed into typed fields.

## Current state (what already exists)

There are **three** nginx surfaces on a domain today:

- **`NginxCustomDirectives`** (raw text) — **admin-only**. Validated by
  `ValidateNginxDirectivesAdmin` (relaxed denylist, GH #1580); `nginx -t` is the
  syntactic guard at apply time.
- **`NginxRules`** (typed rule builder) — admin gets the full set; a **tenant**
  gets a safe subset (`validateTenantNginxRules`: `rewrite` + `custom_header`,
  rewrite target forced to a **local** path, stricter pattern cap) **only when the
  admin enables `TenantDomainOptionsEnabled`** (off by default).
- **`NginxSafeOptions`** (curated toggles: max body, HSTS, security headers, gzip,
  intercept-errors, path-info) — owner-settable.

There is also a **reserved** strict allowlist validator `ValidateNginxDirectives`
+ `allowedNginxDirectives` — kept and unit-tested, but **not wired to any field**
(the admin path moved to the denylist in GH #1580, and no tenant raw-directive
field exists yet).

**Why raw directives are admin-only** (the threat model, from the code): a tenant
with raw directives can SSRF localhost services (panel-api, Bulwark, Stalwart
admin, other tenants' PHP-FPM sockets), disclose files (`root /etc/jabali-panel/`),
suppress CrowdSec (`access_log off;`), or drop inherited auth. **`nginx -t` only
catches syntax — none of those is a syntax error.**

So part of #1624's ask (tenant rewrites + headers) is **already met** behind the
`TenantDomainOptionsEnabled` opt-in. The genuinely new work is (a) a wider,
audited tenant directive subset, and (b) the single-config-model story.

## Decision

Adopt **the typed model as the single source of truth**, extend the tenant-safe
surface through **typed rules first**, gate any **raw** tenant directives behind a
per-directive **value grammar** (not just a name allowlist), and treat admin raw
directives as a **read-only escape hatch** surfaced in the builder — not something
we reverse-parse into editable fields. Built in phases, each approval-gated.

### Phase 1 — surface the existing typed tenant Rule Builder

Make `TenantDomainOptionsEnabled` discoverable and documented. With it on, tenant
rewrites + custom headers already work. This alone answers the reporter's
immediate "I have to do rewrites from the admin side" case. No new trust surface.

### Phase 2 — widen the typed tenant subset

Add the other cleanly-typed, safely-constrained rule kinds the request names —
redirects (a local-only `return`/`rewrite`, same open-redirect guard the tenant
rewrite already uses) and a vetted response-header set. Still typed, still
value-validated, no raw paste.

### Phase 3 — admin directive presets in Web Templates

Let Web Templates carry admin-authored nginx directive presets (the @johnnyq
suggestion), applied at create — same shape as the DNS templates in #1627. The
**admin** authors the snippet, so it never passes through the untrusted-tenant
path. This is the safest vehicle for "copy my working config" and may cover most
migration cases without a tenant textarea at all.

### Phase 4 — tenant raw-directive subset (security-reviewed)

Only after Phases 1-3. A tenant raw "advanced directives" field wired to a
**tenant-specific** validator. The reserved `allowedNginxDirectives` allowlist is
**not** safe to reuse as-is for tenants — a name-level allowlist is insufficient
for untrusted input. It must become a per-directive **value grammar**:

- **`fastcgi_param` — excluded for tenants.** `fastcgi_param PHP_ADMIN_VALUE
  "disable_functions="` (or `open_basedir`, `SCRIPT_FILENAME`, `DOCUMENT_ROOT`)
  is a direct hardening/jail escape. Known class.
- **`location` — must not shadow the vhost's own locations** (`~ \.php$` / FPM /
  auth / ACME-challenge). That is a regex-overlap analysis against the *rendered*
  vhost, not a name check — or `location` is excluded in the first cut (dropping
  the "paste my location blocks" case). Trade-off to pick explicitly.
- **`error_page`** — a value form only (`404 /path`); must not repoint to a named
  location or replace the branded app-error page.
- **`add_header`** — inside a nested `location`, nginx **drops** inherited headers,
  so a tenant block can silently strip the panel's security headers. Nesting /
  inheritance rule required.
- **`return`/`rewrite`** to an absolute URL — open-redirect class; local-only, as
  the typed tenant rewrite already enforces.
- The GH #1580 admin denylist (`root`/`alias`/`include`/`auth_basic_user_file`/
  `load_module`, `access_log off`, `auth_basic off`) still applies.

Behind the same admin opt-in, and reviewed before it ships.

### Phase 5 — unify the two stores

Make the typed Rule Builder the rendered source of truth. Admin raw directives
remain the escape hatch and are **surfaced read-only** in the builder view
("visible, not silently ignored") rather than reverse-parsed. This gives the
consistency #1624 wants (nothing is invisible to the tenant) without pretending an
arbitrary paste can be turned back into structured, editable rules.

## Alternatives considered

- **A — leave raw directives admin-only (status quo).** Rejected: does not meet the
  migration copy/paste UX, and Phase 1 already shows a safe tenant path exists.
- **B — a tenant raw textarea guarded only by the existing name allowlist.**
  Rejected: `allowedNginxDirectives` includes `fastcgi_param` and unrestricted
  `location`, so it re-introduces the PHP_ADMIN_VALUE jailbreak and FPM/auth
  location shadowing. Name-level allowlisting is not sufficient for untrusted
  input — hence the Phase 4 value grammar.
- **C — full bidirectional builder ↔ raw round-trip** (reverse-parse raw
  directives into editable typed rules). Rejected: needs a real nginx-config
  parser with lossless round-trip, and every non-mapping directive strands as an
  opaque blob anyway. Phase 5's read-only surfacing achieves the visibility goal
  without the fragility.

## Consequences

- **Security boundary unchanged through Phase 3.** Only Phase 4 opens a new tenant
  trust surface, and it is gated on the value grammar + a security review, behind
  the admin opt-in.
- **Most migration cases likely solved before Phase 4** — typed redirects/headers
  (Phase 2) + admin template presets (Phase 3) cover the common snippets without
  handing tenants a raw block.
- **Backwards compatible.** With `TenantDomainOptionsEnabled` off, tenant behaviour
  is exactly as today; admin raw directives keep the GH #1580 denylist path.
- **`nginx -t` remains a syntactic backstop only** — it does not, and cannot,
  enforce any of the value-grammar rules above; those are validated in the panel
  before render.
