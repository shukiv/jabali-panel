# Notifications

M14. Events go to the in-app bell and to every enabled channel. A Redis stream feeds a dispatcher that runs inside the panel process.

## Channels

Admins configure the server-wide channels at `/jabali-admin/notifications/channels`: Email, Slack, Discord, Telegram, ntfy, Webhook, SMS and Web Push. The in-app bell always gets every event that is on. See [Channels](./admin/notifications-channels.md).

## Events

`/jabali-admin/notifications/events` lists every event the panel can send (64 today) and turns each one on or off for the whole server. See [Events](./admin/notifications-events.md) for the full catalog.

## Where an event goes

There are no routing rules. An event that is on goes to the bell and to every enabled server-wide channel. An event about one tenant also goes to that tenant's own channels that they routed it to. See [Routing](./admin/notifications-routing.md).

## Test

Click **Test** on a channel's row, or run `jabali notification broadcast --title "…"` to reach every enabled channel. See [Test](./admin/notifications-test.md).

## Architecture

- Producers add each event to the Redis stream `jabali:notifications:queue`.
- The dispatcher inside the panel process reads the stream and checks whether the event is on. It writes the bell row, then calls the sender for each target channel.
- A failed delivery is retried. After 5 tries, the event moves to `jabali:notifications:dlq` (the **Dead Letter** tab). A channel that fails 3 times in a row is disabled.
- Senders are adapters. Adding a new channel kind means one Go file under `panel-api/internal/notifications/senders/`.
- ADRs 0056-0059 cover the data model, the sender interface, Web Push, and the bell dropdown.

## Per-user (tenant) channels — JAB-171

Tenants can own their own channels and route their own events to them at
**`/jabali-panel/notifications`**. This is separate from (and additive to) the
server-wide admin channels above. See ADR-0159 for the full design + security model.

**Off by default.** The whole surface is gated behind a master switch an admin flips at
**Server Settings → General → Tenant notifications**. Until then every `/me/notifications/*`
call returns 403 and the tenant page shows a "not enabled" state. Turning it off again is a
**live kill switch** — tenant channels immediately stop receiving anything.

**What a tenant can do (when enabled):**

- Create/edit/delete their own channels (name, kind, config, enabled), and send a test.
- Route an event kind to one of their own channels; the event then also fans out to that
  channel when it fires *for that tenant*.

**Guardrails (all enforced server-side):**

| Control | Behaviour |
|---|---|
| **Ownership** | A tenant only ever sees/manages their own channels + routes; another user's id returns 404, never 403. |
| **Kind allowlist** | Admin-controlled (Server Settings → General). Empty = safe default set (ntfy/telegram/discord/webpush). webhook/slack/sms/email are admin opt-in. Governs creation **and** live delivery. |
| **SSRF** | Tenant channel URLs (ntfy/discord/webhook/slack/sms) resolve-and-pin their dial and refuse loopback / cloud-metadata / private ranges. |
| **Secrets** | Encrypted at rest, never returned on GET, write-only on edit. |
| **Email** | Forced to the tenant's **own account address** over local delivery — no arbitrary destination, no custom SMTP host. |
| **Limits** | 10 channels/user; 5 test-sends/min/user. |

**Admin view.** The admin channels tab (`/jabali-admin/notifications/channels`) shows an
**Owner** column (Tenant vs Server-wide) and can disable or delete any tenant channel.

**CLI.** `jabali notification channels create --user <id> …` provisions a tenant-owned channel
from the box (omit `--user` for a server-wide channel).
