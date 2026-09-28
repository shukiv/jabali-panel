# Email Queue (Admin)

Stalwart's outbound queue. Reachable from [Mail Deliverability](./mail-deliverability.md) → **Queue**.

## What lives in the queue

- Messages awaiting first delivery attempt.
- Messages deferred by the recipient (4xx response): retry intervals follow Stalwart's exponential schedule.
- Messages held by an administrator (`paused` flag).

Successfully delivered or permanently bounced messages leave the queue immediately and surface in [Email Logs](./email-logs.md).

## Columns

- Queue ID
- Submitted at
- Next attempt at
- From, To
- Size
- Reason held (last 4xx response, or `paused-by-admin`)
- Attempt count
- Age

## Per-row actions

- **Retry now** — moves the next-attempt timestamp to "now"; Stalwart picks it up on the next queue tick.
- **Bounce** — generates an immediate permanent bounce to the sender; removes from queue.
- **Hold** — pause without bouncing; admin returns later to retry or bounce.
- **View headers** — show the full message headers without exposing the body.

## Bulk actions

Selecting multiple rows enables bulk retry, bulk bounce, bulk hold. Useful after fixing a transient outbound problem (DNS, MX block, certificate expiry on the receiving end).

## Why a queue page exists

Stalwart's CLI supports the same operations, but during an incident an operator wants a single screen that shows what is stuck, what the recipient said, and what the next attempt looks like. The queue page is that screen.

## Suspended senders

If [Mail Throttles](./mail-throttles.md) suspended a sender mid-batch, queued messages from that sender appear with reason `sender-suspended`. They remain in the queue until the suspension is cleared.

## Health

The panel sends no notification about queue depth or message age. Check this page.
