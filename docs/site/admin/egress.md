# Per-User Egress Firewall

Security → Egress. nftables + cgroup v2 vmap rules that restrict each user's outbound traffic. M34, ADR-0084.

## Why per-user egress matters

A compromised tenant whose PHP scripts can call any external host becomes an excellent platform for outbound abuse (command-and-control, credential stuffing, spam-relay). Restricting outbound at the per-user cgroup level bounds the damage even when the host application is owned.

## Default policy

Per-user, the nftables ruleset:

- **Allows** `:443/tcp` to anywhere (HTTPS — legitimate API use).
- **Allows** `:587/tcp` and `:465/tcp` to the panel's own mail host (so PHP scripts can submit mail).
- **Allows** `:993/tcp` to the panel's own mail host (IMAP submission of replies, if needed).
- **Allows** `:53/udp` and `:53/tcp` to `127.0.0.1` (the loopback recursor).
- **Drops** everything else.

The package's `egress_policy` field selects this default or `unrestricted` (no per-user filtering).

## Per-user overrides

Users → Edit → Egress allows the admin to:

- Permit specific destination CIDRs on specific ports.
- Permit FQDN-based destinations (resolved at apply time; refreshed on a daily timer).
- Block specific ports / destinations even when otherwise allowed.

Each rule is namespaced per-user; no rule from one user can affect another.

## Implementation

- Each user runs in their own systemd slice (`user-<UID>.slice`).
- nftables uses a `meta cgroupv2` match against the slice id.
- A vmap maps `cgroupv2-id → ruleset` for O(1) classification.
- The reconciler converges the ruleset on each tick.

The setup was live-verified with the drop counter incrementing on a blocked port and `:443` continuing to work.

## Per-page surface

- **Per-user table** — current policy, exception count, drop count (last hour, 24 h).
- **Per-user drill-in** — the active ruleset rendered as a readable summary plus the raw nftables fragment.
- **Test connectivity** — pick a user and a destination; the page invokes a one-shot agent call that runs the same nftables decision against the simulated socket and returns "allowed" or "dropped".

## Notes

- nftables rules are stateless at the layer the panel manages; established connections are not interrupted when a rule changes.
- A user's per-user PHP-FPM pool inherits the slice automatically — no extra wiring per pool.
- The egress firewall does not interpose between the user and inbound traffic; inbound firewalling is [UFW](./ufw-baseline.md) plus [CrowdSec Decisions](./crowdsec-decisions.md).

## CLI

```bash
jabali per-user-egress summary                  # policy state counts and pending requests
jabali per-user-egress get <email-or-id>        # one user's policy
jabali per-user-egress set-policy <email-or-id> --state enforced --allow 140.82.112.0/20,443,tcp
jabali per-user-egress requests                 # requests tenants sent from the panel
jabali per-user-egress approve <request-id>
jabali per-user-egress deny <request-id>
```

`set-policy` replaces the user's whole allow list. `--state` is `off`, `learning` or `enforced`, and each `--allow` is `CIDR[,PORT[,PROTO]]`.
