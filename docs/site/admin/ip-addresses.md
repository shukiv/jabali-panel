# IP Addresses

`/jabali-admin/ips`. The pool of IPv4 (and optionally IPv6) addresses available for per-domain listen-IP selection.

## When to use

Most installations are fine with a single primary IP and rely on TLS SNI to host multiple domains. The IP pool matters when:

- A tenant requires a dedicated IP for branding or reputation reasons (mail reputation in particular).
- A legacy client without SNI support must reach a specific domain over HTTPS.
- The operator wants to separate mail outbound from web outbound for IP-reputation segmentation.

## Adding an IP

The IP must first be present on a network interface of the host. Bring it up via the hosting provider's control panel or:

```bash
ip addr add 198.51.100.42/24 dev ens3
```

Then in the panel: **Add** → enter the IP, an optional label, and an optional default-gateway override (for asymmetric routing).

The agent verifies the IP is reachable on the interface and records the row in `managed_ips`. The new IP appears in the Listen IP dropdown on the [Domain Edit](./domains.md) page.

## Assigning to a domain

Domains → Edit → **Listen IP** → pick from the pool. The reconciler:

1. Updates the vhost `listen <ip>:80;` and `listen <ip>:443 ssl;` directives.
2. Updates the apex DNS `A` (and `AAAA`) record to the new IP.
3. Reloads nginx.

Convergence latency is typically under 60 seconds.

## Removing an IP

Forbidden if any domain is currently using the IP. Reassign affected domains first.

## Operator notes

- **Apex DNS auto-update is load-bearing.** Without it, the domain would still resolve to the previous IP; the new vhost would never be reached.
- **MariaDB 11.4+ reserved word `dual`** — an early migration used `dual` as a derived-table alias and broke fresh MariaDB 11.8 installs. The fix landed in commit `7a8a1ff`; CI now pins MariaDB to 11.x specifically.
- **Migrations seed no rows.** Default IP rows are seeded by `ManagedIPRepository.EnsureDefault` from `serve.go` on first boot, not by the migration itself.

## CLI

IP management is currently UI-driven; CLI verbs are planned but not yet exposed.
