# Notifications

M14. Redis Streams dispatcher → 6 channels → in-app + admin event sources.

## Channels

Admin configures channels at `/jabali-admin/notifications/channels`:

| Channel | Config |
|---|---|
| **In-app** | Always-on. Bell dropdown top-right. |
| **Email** | SMTP submission via the panel's own Stalwart. |
| **Slack** | Webhook URL. |
| **Telegram** | Bot token + chat ID. |
| **ntfy.sh** | Topic URL (works against `ntfy.sh` or a self-hosted ntfy server). |
| **Web Push** | VAPID keys auto-generated on first install; users opt-in per browser via the bell. |

## Event sources

Built-in (M14 Step 4 and later):

- `cert_renew` — Let's Encrypt issuance / renewal success or failure.
- `disk_full` — quota high-water-mark hit per user.
- `service_down` — any of the watched services failed to start, restarted unexpectedly, or is in `failed` state.
- `crowdsec_spike` — sudden spike in decisions or alerts.
- `domain_expiry` — re-interpreted as cert expiry (no WHOIS in scope).
- `aide_diff` — host-integrity drift detected.
- `cron_failed` — a systemd-user cron timer's service unit returned non-zero.
- `backup_succeeded` / `backup_failed`.
- `mail_quarantined` — Stalwart / async YARA quarantined a message.
- `malware_file_hit` — M33 detector hit.
- `db_root_rotated` — admin rotated DB root password.

Stub sources defined but not yet wired: `domain-registrar`, `backup-future-warnings`.

## Routing

`/jabali-admin/notifications/routing` — per-event-source → per-channel mapping with a severity threshold. Examples:

- `cert_renew` failures → Email + In-app (admins).
- `cert_renew` success → In-app only.
- `crowdsec_spike` → Slack #ops + ntfy.

## Test

`/jabali-admin/notifications/test` — fire a test event of any kind to verify routing.

## Architecture

- Producers emit a row into Redis Streams `jabali:notifications`.
- The dispatcher (in-process, single consumer per panel instance) reads the stream, looks up routing rules, calls each enabled sender.
- Senders are pure adapters; adding a new channel is one Go file under `panel-api/internal/notifications/senders/`.
- ADRs 0056-0059 cover the data model, sender interface, Web Push, and the bell dropdown.

## End-user opt-in

Users can opt **in** for `cron_failed`, `backup_succeeded`, `backup_failed`, `mail_quarantined` notifications to their own email — `/jabali-panel/profile` → Notifications.

Per-event subscription scope is bounded by ownership: a user cannot subscribe to `crowdsec_spike` for the whole server, only to events affecting their own account.

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
