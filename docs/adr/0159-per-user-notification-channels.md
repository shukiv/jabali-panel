# ADR-0159 — Per-user notification channels + routing (JAB-171)

**Status:** Accepted — JAB-171. Extends the M14 notification dispatcher (ADR-0056) so
non-admin tenants can own their own channels and route their own events to them, behind a
default-off, security-gated surface. Blueprint at `plans/jab171-per-user-notifications.md`.
Shipped in six serial, individually-reviewed slices (phases 1–6).

## Context

The M14 dispatcher (ADR-0056) delivered notifications to **server-wide, admin-only** channels:
`notification_channels` had no owner, and every enabled channel received every event it was
configured for. Tenants had no way to be notified on their own channels (their phone's ntfy,
their Telegram) for their own events (their backup finished, their cert renewed). Adding a
tenant-facing surface is mostly **scoping + routing + a safe tenant boundary** — the senders
already exist; the risk is entirely in exposing channel creation to untrusted users.

## Decision

**Ownership.** `notification_channels` gains a nullable `user_id` (NULL = server-wide/admin;
set = tenant-owned). A new `user_notification_routes(user_id, event_kind, channel_id)` maps a
user's events to their own channels. The dispatcher's base fan-out uses
`FindEnabledServerWide` (`user_id IS NULL`) so a tenant channel never receives a broadcast;
a user-scoped envelope *additively* reaches the recipient's own routed channels, each
re-checked for ownership (`channel.UserID == recipient`) so a stray/forged route can never
deliver another user's — or an admin — channel.

**Tenant API** `/api/v1/me/notifications/*` (channels CRUD + `:id/test`, routes CRUD),
Kratos-session only. Every read/mutate is scoped to `claims.UserID` and returns **404, never
403**, for another user's id so channel existence never leaks. Create forces
`user_id = claims.UserID` (never from the body).

**The security crux — nothing tenant-facing ships until all of these land (phases 3–5):**

1. **Secrets at rest.** Channel secrets (bearer/HMAC/SMTP password/bot token) are sealed with
   the SSO key (`internal/notifsecret`, `enc:1:<b64url>` marker) on write, opened only just
   before a sender runs, redacted on every GET, and edited write-only (an empty secret keeps
   the stored one). Legacy plaintext rows keep working until a one-time backfill re-seals them.
2. **SSRF.** A tenant channel's outbound HTTP (ntfy/discord/webhook/slack/sms) uses a guarded
   client (`internal/ssrfguard`) that resolves, range-checks and **pins the dial to a validated
   public IP**, refusing loopback / link-local (cloud metadata) / private space at connect time
   (defeats DNS rebinding). Selected per channel (`UserID != nil`), so admin channels — which
   may legitimately target an internal host — are unaffected.
3. **Admin-controlled kind allowlist**, default-off for risky kinds. `TenantNotificationKinds`
   on server settings; empty = the safe default set (ntfy/telegram/discord/webpush). Admins opt
   into webhook/slack/sms/email. Enforced on create AND at delivery (removing a kind stops
   existing tenant channels of that kind).
4. **Abuse limits.** Per-user channel quota (10) and a per-user test-send rate limit (5/min);
   the test endpoint is the only tenant-triggerable send.
5. **Email is own-address only.** A tenant email channel is forced (create + every edit) to
   `to_email = from_email = the caller's account email`, `smtp_mode = local` — no arbitrary
   destination (open relay) and no tenant-supplied SMTP host (SSRF).
6. **Master gate + kill switch.** `ServerSettings.TenantNotificationsEnabled` (default OFF) is
   the master switch, exposed to admins only after 1–5 landed. The dispatcher honours it as a
   **live delivery kill switch, fail-closed**: off (or a settings read error) → tenant channels
   receive nothing; admin channels are unaffected.

**Admin override.** The admin channel API is not ownership-scoped, so an admin lists (with an
owner column), disables, or deletes any tenant channel.

**CLI/UI.** Tenants manage their channels + routes at `/jabali-panel/notifications`; admins get
the toggle + allowlist in Server Settings → General and an owner column on the channels tab.
`jabali notification channels create --user <id>` provisions a tenant-owned channel from the box.

## Consequences / security (the load-bearing part)

- **Fail-closed everywhere it matters.** The master gate and the dispatcher policy both fail
  closed; the SSRF guard is selected by channel ownership (not a call-site flag) so it can't be
  forgotten; the allowlist governs delivery, not just creation.
- **Do-not-ship discipline.** The feature merged in slices, but each abuse control had to be in
  before the toggle became flippable — so at no point could an operator enable an unsafe surface.
- **Defense in depth over convenience.** Tenant email is deliberately own-address-only;
  arbitrary verified destinations are a future additive enhancement, not a launch requirement.
- **Migrations** `000237` (owner + routes table), `000238` (master flag), `000239` (kind
  allowlist) are schema-only and box-verified up/down.
