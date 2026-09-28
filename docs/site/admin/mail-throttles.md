# Mail Throttles

`/jabali-admin/mail/throttles`. Outbound mail rate-limit policy enforced by Bulwark and Stalwart (M47 Wave 3).

## Why throttle outbound

A compromised mailbox or runaway PHP script can generate thousands of messages per minute. Without per-sender caps, the panel's outbound IP rapidly accumulates reputation damage that takes weeks to recover. Throttles bound the worst case before it leaves the network.

## Configurable limits

| Scope | Units | Default |
|---|---|---|
| Per mailbox | messages / minute | 30 |
| Per mailbox | messages / hour | 500 |
| Per mailbox | recipients / message | 100 |
| Per domain | messages / minute | 300 |
| Per domain | messages / hour | 5000 |
| Per IP | messages / minute | 1000 |

Override per-mailbox or per-domain by adding a row in the **Overrides** tab.

## Enforcement

- Bulwark intercepts SMTP submission on `:587` / `:465`, checks the per-sender counter against the limit, and returns `421 4.7.0 throttled, try later` when exceeded.
- Stalwart maintains the per-IP counter and applies the policy on outbound MTA delivery.

## Excluded paths

- System-generated mail (recovery emails, notifications from the panel itself) is exempt.
- Mailing-list expansion (if implemented in a future release) will count once per outbound recipient batch, not once per list member.

## Suspending a sender

The panel does not suspend a sender on its own, and sends no notification about throttle hits. To stop a mailbox from sending, disable it or change its password.

## Monitoring

The page renders the past 24 hours of throttle hits as a per-sender heatmap. Click a sender to drill into the per-minute history.
