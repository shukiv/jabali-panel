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

## Changing the server's main IP

Moving the server to a new address takes two steps, in this order. Put the new address on a network interface first (your provider's panel, netplan or `/etc/network/interfaces`); Jabali does not configure interfaces.

**1. Make the new address the pool default.** Add it to the pool and promote it (**Edit → Default**, or `jabali ip add <ip>` then `jabali ip update <id> --default`). At once, Jabali moves:

- the panel's default server in `jabali-default.conf` (the site an unknown name gets, and where the panel hostname's certificate challenge lands);
- the `/etc/hosts` lines on the old address (the panel hostname and `mail.`/`autoconfig.<hostname>`, which webmail uses);
- the CrowdSec self-whitelist: the new address is added. The old one stays while it is still on an interface, and is removed once it is not.

Within one reconcile pass (about a minute) every domain on **Default** moves its vhost and its apex `A` record to the new address. Wait for that before step 2.

If the address is not on an interface yet, the promotion is saved but nothing on the server moves; the CLI says so, and the panel log records it. Put the address on the interface and promote it again.

**2. Change the addresses in Settings** (`public_ipv4`, `public_ipv6`, `ns1_ipv4`, `ns2_ipv4`). The reconciler then moves the DNS records that name the server itself, in every zone:

- the `mail` A/AAAA record (the MX target);
- the default SPF record (`v=spf1 mx ip4:… ip6:… ~all`);
- the `ns1`/`ns2` A records in the zone that holds the nameserver names.

It moves only records the panel created and nobody edited. A record you changed by hand, or an SPF you added an `include:` to, stays as you left it.

A new nameserver address that is on this server and that PowerDNS does not listen on yet makes Jabali restart PowerDNS once, so it answers there. Doing step 2 last matters: by then the apex records already name the new address, so the restarted PowerDNS does not hand out the old one.

## Removing an IP

Forbidden if any domain is currently using the IP. Reassign affected domains first.

## Operator notes

- **Apex DNS auto-update is load-bearing.** Without it, the domain would still resolve to the previous IP; the new vhost would never be reached.
- **MariaDB 11.4+ reserved word `dual`** — an early migration used `dual` as a derived-table alias and broke fresh MariaDB 11.8 installs. The fix landed in commit `7a8a1ff`; CI now pins MariaDB to 11.x specifically.
- **Migrations seed no rows.** Default IP rows are seeded by `ManagedIPRepository.EnsureDefault` from `serve.go` on first boot, not by the migration itself.

## CLI

`jabali ip list`, `jabali ip add <address>`, `jabali ip update <id> [--label …] [--user-selectable] [--default]`, `jabali ip delete <id> --force`.
