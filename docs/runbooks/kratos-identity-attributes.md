# Runbook — Kratos identity attributes: panel-controlled vs user-editable (JAB-5)

**Rule:** the **Jabali panel DB is authoritative** for identity and RBAC. Kratos
stores a mirror of the identity traits, but those traits are **never** the
source of truth for authorization and **must not** be user-mutable.

## Attribute ownership

| Trait / attribute | Source of truth | User-editable? | How it changes |
|---|---|---|---|
| `traits.username` | panel DB (`users.username`) | **No** | Jabali admin/user API → `kratosclient.UpdateUsernameTrait` (`PATCH /admin/identities/{id}`) |
| `traits.email` | panel DB (`users.email`) | **No** | Jabali admin/user API → Kratos admin API |
| `traits.is_admin` | panel DB (`users.is_admin`) | **No** | Jabali admin API only; **never** read for authz (middleware uses the DB) |
| password | Kratos (credential) | **Yes** | `/settings` flow, `password` method (self-service) |
| TOTP (2FA) | Kratos (credential) | **Yes** | `/settings` flow, `totp` method (self-service) |
| lookup secrets (recovery codes) | Kratos (credential) | **Yes** | `/settings` flow, `lookup_secret` method (self-service) |

## How the boundary is enforced

1. **Kratos self-service `profile` method is DISABLED**
   (`install/kratos.yml.tmpl` → `selfservice.methods.profile.enabled: false`).
   The profile method is the only settings-flow method that mutates identity
   **traits**. With it off, a signed-in user cannot change username/email/
   is_admin — not through the UI and not through a crafted direct `/.ory`
   settings/profile submission (Kratos rejects a disabled method).
   Guarded by `TestKratosProfileMethodDisabled`.

2. **Password / TOTP / lookup-secret stay self-service** (separate methods),
   so users still manage their own credentials via `/settings`.

3. **Admin/user changes sync over the Kratos ADMIN API**, never self-service —
   `UpdateUsernameTrait`, `SetPassword`, `SetIdentityState`, `RemoveSecondFactor`
   all `PATCH /admin/identities/{id}`. Disabling the profile method does not
   affect these.

4. **Display never trusts the trait.** The Active Sessions view
   (`GET /admin/sessions`) resolves `is_admin` from the panel DB by the
   session's email, falling back to the trait only when no panel row matches
   (deleted user / dev-without-Kratos). Guarded by
   `TestListSessions_ResolvesAdminFromPanelDB`. Authorization middleware
   (`RequireKratosSession`) already uses the DB, not the trait.

## Propagation to existing installs

`install.sh` re-renders `kratos.yml` from the template on **every**
`jabali update` (not only fresh installs), so existing hosts pick up
`profile.enabled: false` on their next update — no manual step.

## If you must add a genuinely user-editable trait later

Do **not** re-enable the `profile` method wholesale. Instead split the identity
schema so only the new self-service field lives in a settings-editable trait,
keep username/email/is_admin panel-controlled, and update
`TestKratosProfileMethodDisabled` deliberately with the rationale.
