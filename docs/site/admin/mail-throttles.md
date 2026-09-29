# Mail Throttles

`/jabali-admin/mail/throttles`. Caps on how much mail a sender, a domain or the whole server can send (M47 Wave 3). Each cap becomes a Stalwart outbound throttle (`MtaOutboundThrottle`).

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

About once a minute, the reconciler makes Stalwart match every row. The hourly cap and the daily cap become two separate throttles. The panel makes each change through Stalwart's management API, signed in with its Stalwart admin token (`/etc/jabali-panel/stalwart-admin.token`), and reloads Stalwart's settings afterwards.

- A `user` row counts per sender and applies only to that address.
- A `domain` row counts per sender domain and applies only to that domain.
- A `global` row is one count for all outbound mail.

A throttle that someone changed or deleted in Stalwart by hand is put back on the next tick. Turning a row off, or setting a cap to `0`, removes that throttle.

Every throttle the panel creates has a description that starts with `jabali `. On each tick, the panel removes any Stalwart throttle with that prefix that no row uses. If you create throttles in Stalwart yourself, do not start their descriptions with `jabali `.

The **Stalwart sync** column shows the row's state:

- **synced**: Stalwart holds exactly what the row asks for: one throttle for each enabled cap, and none when the row is off.
- **pending**: Stalwart does not match the row yet. The next tick fixes it.
- **error**: the last push failed. Hover over the tag to see the error. The next tick tries again.

Deleting a row removes both of its throttles from Stalwart first. If Stalwart cannot remove one, the delete fails with `stalwart_delete_failed` and the row stays, turned off. The next ticks keep trying to remove its throttles. Delete the row again when the error is gone.

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
