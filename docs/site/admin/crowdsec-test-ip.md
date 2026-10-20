# CrowdSec — Test IP Card

Security → CrowdSec → **Test IP**. Verify what would happen to a given IP address right now without waiting for the IP to attempt a connection.

## Inputs

- An IPv4 or IPv6 address.

## Outputs

- Whether the IP is currently subject to any decision (ban, captcha, allowlist) and, if so, the scenario that produced it.
- Whether the IP is on any allowlist entry, and which one.
- Reverse DNS, ASN, country (from the local GeoIP database, if installed).

## When to use

- After an end-user report of "I cannot reach my site" — pasting the user's source IP immediately reveals whether the panel itself is dropping them.
- During allowlist setup — verify that a candidate IP is currently clean before adding it.
- After a manual decision — confirm the addition or removal landed.

## Implementation

The card calls a single API endpoint that wraps `cscli decisions list --ip <ip>` plus a series of allowlist lookups. The endpoint is admin-only; tenants cannot enumerate other users' source IPs.

## Operator note

The check reads CrowdSec's current state. A decision that was active a minute ago and has since expired returns "clean". When investigating a historical block, consult [Audit Log](./audit-log.md) for the original decision row instead.
