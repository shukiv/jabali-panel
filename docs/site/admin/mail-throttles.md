# Mail Throttles

`/jabali-admin/mail/throttles`. Caps on how much mail a sender, a domain or the whole server can send (M47 Wave 3). Stalwart enforces each cap.

## Why throttle outbound

A compromised mailbox or a runaway PHP script can send thousands of messages per minute. Without per-sender caps, the server's outbound IP quickly damages its reputation, and that takes weeks to recover. Throttles limit the worst case before the mail leaves the network.

## Rows

Each row is one cap:

| Field | Meaning |
|---|---|
| **Scope** | `global`: one cap on all outbound mail from the server. `user`: one sender address. `domain`: one sender domain. |
| **Sender address** / **Sender domain** | For `user`, the full address, for example `alice@example.com`. For `domain`, the domain, for example `example.com`. The panel rejects anything else, including quotes and backslashes. Scope and sender cannot change after the row is created. |
| **Max per hour** | Messages per hour. `0` means no hourly cap. |
| **Max per day** | Messages per day. `0` means no daily cap. |
| **Enabled** | Turning a row off removes its caps from Stalwart. |

The panel ships with no rows. Until you add one, it sets no outbound cap.

## How the caps are applied

On each reconciler tick, the panel turns every enabled row into Stalwart outbound throttles. The hourly cap and the daily cap become two separate throttles.

- A `user` row counts per sender and applies only to that address.
- A `domain` row counts per sender domain and applies only to that domain.
- A `global` row is one count for all outbound mail.

The **Stalwart sync** column shows the row's state:

- **pending**: the row has not reached Stalwart yet.
- **synced**: Stalwart has it.
- **error**: the last push failed. Hover over the tag to see the error. The next tick tries again.

Deleting a row removes both of its throttles from Stalwart at once. If Stalwart cannot be reached at that moment, the row is still deleted but its throttles stay in Stalwart. In that case, delete them with `stalwart-cli`.

## What the panel does not do

- It does not suspend a sender that keeps hitting a cap.
- It sends no notification about throttle hits.
- It shows no history of throttle hits.

To stop a mailbox from sending, disable it or change its password.

## API

```
GET    /api/v1/admin/mail/throttles
POST   /api/v1/admin/mail/throttles        {"scope":"user","scope_ref":"alice@example.com","max_per_hour":100,"max_per_day":1000,"enabled":true}
PUT    /api/v1/admin/mail/throttles/{id}   {"scope":"user","max_per_hour":200,"max_per_day":2000,"enabled":true}
DELETE /api/v1/admin/mail/throttles/{id}
```

`PUT` needs `scope` in the body, but it changes only the two caps and `enabled`. It replaces both caps, so send both. An omitted cap becomes `0`, which means no cap.
