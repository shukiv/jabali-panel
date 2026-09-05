# Removed Features

If you're coming from the previous (PHP) Jabali generation or from an earlier dev snapshot of jabali2, here's what's intentionally **not** in the shipped panel.

## Removed because superseded

### ModSecurity → CrowdSec AppSec (M27, ADR-0060)

ModSecurity (libmodsecurity + nginx connector + the OWASP CRS) was removed entirely. Reasons:

- nginx connector is in long-term maintenance, not active development.
- Rule curation overhead (CRS false positives on legitimate app traffic).
- CrowdSec AppSec gives equivalent virtual-patching via the `crowdsecurity/vpatch` family, plus IP-layer escalation.

What replaces it: **CrowdSec AppSec** — inline `appsec-block` bouncer in nginx, rule packs from `hub.crowdsec.net`. See [security.md](./security.md#appsec-waf-m27--replaces-modsecurity).

The installer's `cleanup_modsecurity` step purges any leftover ModSecurity packages + configs on every install — leftover ModSecurity nginx directives would silently break vhost reloads.

### Filebrowser → in-panel AntD File Manager (M11, ADR-0030)

The separate `filebrowser` daemon was decommissioned 2026-04-19. The in-panel manager at `/jabali-panel/files` covers all the same use cases without a second auth stack / second listening socket / second UI.

If you have user docs that say "go to `/files`", update them to `/jabali-panel/files`.

### OIDC / Hydra (M16 — rolled back to M22)

There was a brief window where Jabali shipped Hydra as an OIDC provider so apps (WP via a plugin) could federate auth to the panel. We rolled it back:

- Hydra-on-SQLite (the path we had to take because Hydra wouldn't run on MariaDB) added a separate state store, which made backups + DR more complex.
- The WP plugin path was fragile across WP version updates.
- The user value (one login for all installed apps) ended up better served by the **M22 self-deleting SSO file** pattern (Installatron / Softaculous style) — no plugin in the app, no third-party state store.

Result: the panel does not act as an OIDC provider. If a third-party app needs SSO with Jabali, the SSO file pattern is the supported integration.

(ADR-0036 was superseded by ADR-0038; ADR-0040 covers the M22 rework.)

### In-panel admin → user impersonation — **restored**

This was removed with the M20 Kratos migration, but has since been **re-added**
(ADR-0128, act-as; ADR-0015, the JWT claim). Admins can impersonate a tenant
through the `/admin/impersonation` routes (admin-gated), which mint a scoped,
**60-minute** act-as grant (server-side TTL; ended explicitly or on expiry).
There is no dedicated impersonation start/stop audit event, but every mutating
request made while impersonating is recorded by the unified audit log
(ADR-0106) with the **target as the subject**, so those actions surface in the
target's own account activity (`GET /api/v1/me/activity`, subject-scoped and
IDOR-safe). It is no longer a removed feature — this entry is kept only to
correct earlier docs that listed it as gone.

### Refine → TanStack Query + AntD + react-router (M21)

`@refinedev/*` was removed; the UI is now built directly on TanStack Query + AntD + react-router. ADR-0037. Production JS dropped from 2.2 MB to 1.6 MB.

If you have docs that say "the panel uses Refine" — update.

## Removed because never shipped on the new generation

### Old PHP panel features not yet re-implemented

- **Plesk-style live console** — there is a `/jabali-admin/terminal`, but the per-user live shell-over-web isn't surfaced for tenants.
- **Add-on domains as a first-class concept** — alias domains exist; "add-on" with its own docroot is currently modeled as a separate Domain row.
- **Per-domain bandwidth meters per-day** — we report monthly bandwidth, not per-day; the old PHP graph isn't ported.
- **Spamassassin + ClamAV-on-delivery scanning of every inbound mail** — Stalwart handles spam scoring; ClamAV is on-demand only (M33 chose on-demand over the daemon to keep RAM usage predictable on small VPS). Async post-delivery YARA (M33.2) catches the high-priority class.

## What's deferred (planned, not removed)

These are on the roadmap, not abandoned:

- **Per-domain FastCGI micro-cache** (ADR-0108) — see the planning doc; not yet shipped.
- **CDS / CDNSKEY publication** for DNSSEC parent registrar automation.
- **PostgreSQL admin-ops UI parity** (M46 covers MariaDB; PG parity is partial).
- **APM / OpenTelemetry tracing** for the panel and agent.
- **Automation API** — scoped tokens UI exists (`/jabali-admin/automation`); the public API surface itself is rolling out.

---

If you're updating end-user docs: anything that pointed at filebrowser, oidc/hydra, modsec, impersonate-as-user, or refine should be removed or updated to the replacement.
