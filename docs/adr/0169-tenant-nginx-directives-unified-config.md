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
  intercept-errors, path-info) — admin always; a **tenant** only when the admin
  enables `TenantDomainOptionsEnabled` (the same opt-in as `NginxRules`, off by
  default). (An earlier draft called these plainly "owner-settable"; the code
  gates them on the opt-in, so a plain owner cannot set them until it is on.)

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

## Implementation notes (as shipped)

The phase plan above is the decision as recorded; a few things landed narrower or
differently than the sketch. Kept here so the ADR and the code do not drift.

- **Phase 1 — surface + document (GH #1681, merged).** As planned: made
  `TenantDomainOptionsEnabled` discoverable (Overview hint when off) and documented
  both surfaces. No new trust surface.

- **Phase 2 — hardened the typed header set, did not widen (GH #1684).** The
  redirects the phase named already existed (`page_redirects`, `redirect_all_to`,
  and the tenant `rewrite` rule's local-path `redirect`/`permanent` flag), and no
  other typed kind is tenant-safe (`proxy_pass` = SSRF; `ip_access` / `php_setting`
  / `static_alias` are admin). So the real Phase 2 content was **constraining**
  `custom_header`, not adding kinds: a tenant `custom_header` may no longer set a
  panel-managed response header (`Strict-Transport-Security`, `X-Frame-Options`,
  `X-Content-Type-Options`, `Referrer-Policy`, `Content-Length`,
  `Transfer-Encoding`), which a server-scope `add_header` could otherwise duplicate
  or void (e.g. HSTS `max-age=0`).

- **Phase 3 — Web Templates entity was built, not pre-existing (GH #1687 backend,
  #1689 UI).** The sketch read as though a Web Templates entity already existed to
  "carry" presets; it did not (only DNS templates #1627 and page templates did), so
  a new `web_templates` table + `domains.web_template_id` were added. The preset is
  **snapshot-copied** onto `nginx_custom_directives` at create (not a live link),
  validated by the admin denylist at both save and apply. Selection is
  **admin-only**: because the admin denylist does not block `proxy_pass`, a
  tenant-pickable template would be SSRF with the admin as unwitting author, so a
  non-admin create with `web_template_id` is refused.

- **Phase 4 — tenant raw subset shipped tighter than the sketch (GH #1691 backend,
  #1692 UI).** The field is `domains.nginx_tenant_directives`, kept separate from
  the admin `nginx_custom_directives`, behind `ValidateNginxDirectivesTenant`:
  - **Allowlist is `add_header` / `expires` / `etag` only** — all response-shaping,
    none routing. The sketch's `return`/`rewrite` (local-only) and `error_page`
    (value form) were **excluded in the first cut**: raw `return` can preempt
    `/.well-known/acme-challenge` and open-redirect via `$vars`, and `error_page` /
    `gzip` are already emitted at server scope by the template / `NginxSafeOptions`,
    so a tenant copy duplicates them and fails `nginx -t`. `location`, `proxy_pass`,
    `root`, and `fastcgi_param` stay excluded as the sketch required. Widening to
    `return`/`rewrite`/`error_page` is a documented follow-up if wanted.
  - **Blocks (`{ }`) are rejected outright**, which resolves the sketch's open
    "nested `location` / `add_header` inheritance" question — a tenant cannot open a
    nested scope, so inherited-header stripping is not reachable.
  - Structural guards the sketch did not spell out but the code needs: **one
    statement per line** (exactly one unquoted `;`, quote-aware for CSP/`Link`
    values) so the shared first-token scanner cannot be smuggled past, and
    **backslashes rejected outright** (a security-review finding: naive quote
    tracking treats `\"` as a close while nginx treats it as a literal, leaving the
    string open to swallow following config). 8 KB / 64-line caps.
  - Renders through the single existing `custom_directives` assembly in the
    reconciler — **no panel-agent change** — so it inherits the per-vhost
    `nginx -t` + revert containment.

- **Phase 5 — read-only surfacing, both directions (GH #1694).** UI only; no
  migration, no render-pipeline change (both stores already render). The tenant
  builder view now shows the admin's `nginx_custom_directives` read-only (they were
  already in the tenant's domain JSON, just never displayed), and — the mirror the
  sketch did not call out — the admin Nginx section shows the tenant's
  `nginx_tenant_directives` read-only with a **Clear** action, because disabling the
  opt-in does not deactivate an already-stored tenant snippet (the reconciler still
  renders the column), so an admin needs a way to null it. Neither store is
  reverse-parsed into the other (Alternative C stays rejected). Precedence between
  typed rules and raw directives is deliberately left unspecified.
