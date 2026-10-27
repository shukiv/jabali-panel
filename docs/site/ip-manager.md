# IP Manager

`/jabali-admin/ips`.

## What it does

Lets the admin curate the pool of IPv4 addresses available for **per-domain listen IP** selection.

## Use cases

- **Per-tenant IP** — assign a dedicated IP to a domain so its mail / SSL / branding sits on its own.
- **SEO** — historically only modest signal, but some still want it.
- **Multiple SSL on legacy clients** — SNI handles this on modern stacks; per-domain IP is the fallback.

## Adding an IP

The admin assigns IPs to the panel host via the hosting provider's control panel (or `ip addr add` for bare metal). Once the IP is on the host's network interface, add it to Jabali:

`/jabali-admin/ips` → **Add** → IP, label, optional default-gateway override.

Jabali stores the IP in `managed_ips` and exposes it in the **Listen IP** dropdown on Domain Edit.

## Per-domain assignment

Domains → Edit → **Listen IP** → pick from the pool (or "Default" = server primary IP).

On change, the reconciler:

1. Updates the vhost `listen <ip>:80;` / `listen <ip>:443 ssl;` lines.
2. Updates the apex `@ A` record for the zone to the new IP (so `example.com` actually resolves to the right address).
3. Reloads nginx.

## Apex DNS auto-update

When a domain's listen IP changes, the `@ A` record (and `@ AAAA` if IPv6 is configured) updates automatically. This is the load-bearing detail — without it, the new IP serves the right vhost but DNS still points elsewhere.

## Caveats

- **MariaDB 11.4+ reserved word `dual`**: an early migration used `dual` as a derived-table alias and broke fresh MariaDB 11.8 installs. Fixed in `7a8a1ff` — pinned MariaDB CI to 11.x specifically (not latest or 10.x).
- **Seeding rule**: migration 000057 used to seed `server_settings` rows; migrations are now schema-only. Data seeds happen in `ManagedIPRepository.EnsureDefault` from `serve.go` on first boot.

## CLI

(IP management is currently UI-only; CLI verbs may follow.)
