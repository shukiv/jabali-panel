# ADR-0165 — Automation login-token SSO via Kratos admin recovery links

**Status:** Accepted — JAB-190 follow-up (the piece ADR-0164 deferred).
**Related:** ADR-0164 (billing automation endpoints), ADR-0034/M20 (Kratos
sole auth), ADR-0093/0157 (automation token spine).

## Context

Billing panels (WHMCS/Blesta/WiseCP via `jabali-integration`) need a
"Log in to Jabali Panel" button: the billing server mints a one-time URL,
the END USER's browser redeems it and lands in the panel with a session.
The panel has no one-time-login surface: Kratos is the sole auth source
(M20), impersonation rides the admin's own session (ADR-0015), and the
M22 magic-link is app-level WordPress/Drupal/Joomla SSO, not panel login.

Building a bespoke token→session flow would mean minting Kratos sessions
ourselves — new security-critical machinery.

## Decision

Reuse Kratos's **admin recovery code** API (`POST /admin/recovery/code`),
already wrapped by `kratosclient.CreateRecoveryCode` and battle-tested by
`jabali admin rebuild-kratos`:

```
POST /api/v1/automation/users/:id/login-token   scope write:users
  {ttl_s?: int, client_ip?: string}
  → 200 {ok:true, url, expires_in, message}
```

- `url` is the Kratos-issued `recovery_link` — **single-use**, expiring,
  served from the panel's own hostname (`serve.public.base_url` is
  `https://<panel-host>:8443/.ory/`), which also satisfies the PHP
  client's SSO-host validation.
- Opening it gives the user an active session and lands them on the
  account-settings page (Kratos recovery UX). For v1 this "logged in,
  on the settings page" landing is accepted; a dashboard landing needs a
  Kratos `return_to` flow and can layer on later without a wire change.
- `ttl_s` clamps to [60s, 3600s], default 300.
- Admin targets are refused (blanket, per ADR-0164); users without a
  Kratos identity are a 409.
- Audit row on every mint (success and failure) — the row records the
  actor token and target user, NEVER the link or code. No M14
  notification: SSO clicks are routine (one per "Log in" press), unlike
  suspend/create/delete.
- `client_ip` is accepted for wire-compat with the integration contract
  but **not enforced**: Kratos recovery codes are not IP-bindable.
  Compensating controls: single-use, short TTL, HTTPS-only transport,
  audit. A panel-side IP-bound wrapper (own table + unauthenticated
  redeem route) was considered and rejected for v1 — it adds an
  unauthenticated endpoint and a migration to defend against an attacker
  who can already read the HTTPS response in flight.

Capability advertised: `users.login_token` (the name ADR-0164 reserved);
mounts when Users + KratosClient are wired.

## Consequences

- The billing modules' SSO buttons (`ServiceSingleSignOn`,
  `tabClientActions`, `use_clientArea_SingleSignOn`) go live with zero
  module changes — they already feature-detect `users.login_token`.
- Recovery must stay enabled in Kratos config
  (`selfservice.methods.code.enabled=true`) — already required by the
  rebuild-kratos path; CreateRecoveryCode surfaces a clear operator
  error when disabled.
- A minted link in the wrong hands is a full account login for its TTL:
  the runbook's write-token guidance (IP allowlist, expiry, kill switch)
  applies to any token holding `write:users`.

## Addendum — JAB-232: redemption was broken; code-in-URL + SPA /recovery page

**Status:** accepted, 2026-08-08. Amends the original decision.

The original premise — "opening the minted `recovery_link` gives the user an
active session" — did **not** hold. Kratos's **code** recovery strategy returns
a link with **no token in the URL** (the 6-digit code is a separate response
field designed for out-of-band email delivery), and the SPA had **no `/recovery`
route** — the catch-all bounced the visitor to `/login`, unauthenticated. The
one-click `link` strategy that would embed a token is disabled in `kratos.yml`.
Verified broken in a clean browser on 192.168.100.86 (see jabali-integration
HANDOFF §6).

**Decision (Option B — keep the code strategy, add a redeem page):**

1. `userLoginTokenHandler` folds `recovery_code` into the minted URL as a query
   param → `https://<panel>:8443/recovery?flow=<id>&code=<code>`. The code-in-URL
   is the **same sensitivity class** as the link itself (both are single-use,
   short-TTL, full-login credentials over HTTPS); it is never logged (the audit
   still records the mint only).
2. A new **public** SPA route `/recovery` (`RecoveryRedeem`) reads `flow`+`code`
   and POSTs `{method:"code", code}` to `/.ory/self-service/recovery?flow=<id>`.
   Code flows carry no CSRF node, so a same-origin POST works. Kratos creates the
   target session and answers `422 browser_location_change_required`; the page
   then does a **full** navigation to `/` so `AuthProvider` re-runs `whoami` and
   `LandingRedirect` drops the now-authenticated user on their dashboard.

**Why Option B over enabling the Kratos `link` strategy (Option A):** Option A is
a smaller code change but a **global** Kratos config change that also widens the
email-recovery LINK surface (enumeration/delivery review); Option B changes no
Kratos config, keeps the blast radius to this feature, and also fixes the
identically-broken `jabali user password --link` UX (same missing page).

**Dashboard landing (former follow-up #3):** resolved here — we deliberately do
NOT follow Kratos's post-recovery `/settings?flow=` (its optional "set a new
password" step); the recovery session is a valid aal1 session, so an SSO login
lands on the dashboard, not a password-reset prompt.

Modules are unaffected — they still consume only `url` from the response
envelope.
