# Setting up a secondary nameserver (ns2)

Jabali Panel runs the primary authoritative nameserver (ns1) on the
panel host. A secondary nameserver (ns2) is strongly recommended — most
registrars require at least two NS records with different IPs before
they'll accept NS delegation.

## Option A — another plain PowerDNS box

Any Debian/Ubuntu server with public DNS ports (53 UDP+TCP) open.

1. Install PowerDNS:
   ```bash
   apt install pdns-server pdns-backend-bind
   ```

2. Create a minimal `/etc/powerdns/pdns.d/slave.conf`:
   ```
   launch=bind
   slave=yes
   superslave=yes
   trusted-notification-proxy=<NS1_IPV4>
   supermasters=/etc/powerdns/supermasters
   ```

3. Create `/etc/powerdns/supermasters`:
   ```
   <NS1_IPV4>  ns1.<yourhostname>  admin@example.com
   ```

4. Start: `systemctl enable --now pdns`.

5. Back in the Jabali admin panel, visit Server Settings and set:
   - `ns2 hostname` = `ns2.<yourhostname>`
   - `ns2 IPv4` = this box's public IPv4

   Saving triggers a zone re-push across every hosted domain. The
   secondary will receive NOTIFY for each zone and pull it via AXFR.

6. At your registrar, add a second NS glue record pointing to ns2.

## Option B — third-party secondary DNS

Services like Hurricane Electric's free DNS, buddyns.com, or nsupdate
offer secondary-only DNS. Configure them to slave from `<NS1_IPV4>`.
A provider usually transfers from more than one address, and those are
often not the addresses of the nameservers it gives you. Enter the
provider's transfer addresses (its documentation lists them) under
**Server Settings → DNS → Secondary DNS servers (AXFR + NOTIFY)**.

## Option C — hidden primary

The panel keeps the zones, and only the provider's nameservers answer
the public: the registrar delegates to them, never to this server.

1. Every zone's NS records come from the `ns1 hostname` and `ns2
   hostname` fields in **Server Settings → DNS**. Set them to two of the
   provider's nameservers. The panel puts only these two names in each
   zone.
2. List every transfer address of the provider under **Secondary DNS
   servers (AXFR + NOTIFY)**. `ns2 IPv4` can stay empty.
3. At the provider, add each zone as a secondary with this server's
   public IP as its primary.

## Secondary DNS servers (AXFR + NOTIFY)

Up to 16 IPv4 or IPv6 addresses, besides ns2 (GH #2131). Every address
listed may transfer every zone on this server, and is sent a NOTIFY
when a zone changes. The panel refuses loopback, unspecified,
multicast and link-local addresses, and an address listed twice.
`ns2 IPv4` is always included; don't list it again.

From the shell:

```bash
jabali settings set dns_secondary_ips="192.0.2.53,2001:db8::53"
jabali settings set dns_secondary_ips=""    # clear the list
```

After a save, the reconciler re-pushes every zone once with the new
lists.

## Verifying it works

From the ns2 host:

```bash
dig AXFR <hosted-domain> @127.0.0.1
```

Should return the full zone. If it returns `REFUSED` or empty, check:

- Did you save Server Settings in the admin panel after installing ns2?
  (That's what pushes the `ALLOW-AXFR-FROM` metadata to ns1.)
- Is `ns2_ipv4`, or an entry under **Secondary DNS servers**, exactly
  the IP the secondary will send AXFR requests from? (No NAT or
  source-address translation in the path.)
- On ns1, run `pdns_control notify <zone>`. It should answer `Added to
  queue`. `PowerDNS not configured as primary` means the host has not
  run `jabali update` since GH #2131; `jabali update` adds
  `primary=yes` to `/etc/powerdns/pdns.d/01-jabali-mysql.conf`.
- `journalctl -u pdns` shows each NOTIFY sent, and any error from it.

## How the pieces fit

- Panel DB is the source of truth for every zone's records.
- On any change to a domain or its records, the panel reconciler calls
  the agent's `dns.zone.upsert` command.
- The agent rewrites PowerDNS's native `records` table inside a
  transaction and updates the per-zone `domainmetadata` rows for
  `ALLOW-AXFR-FROM` (ns2 IPv4, each secondary DNS server, and
  `127.0.0.1`) and `ALSO-NOTIFY` (ns2 IPv4 and each secondary DNS
  server).
- The agent then runs `pdns_control notify <zone>`. PowerDNS sends a
  NOTIFY to each `ALSO-NOTIFY` address, and each secondary pulls the
  fresh zone via AXFR.
- PowerDNS runs with `primary=yes` and an empty `only-notify=`. The
  empty `only-notify` means NOTIFY goes only to the `ALSO-NOTIFY`
  addresses, never to the hosts in a zone's NS records, which a
  customer can edit.
- The global `allow-axfr-ips` stays `127.0.0.0/8,::1` (JAB-350). Only
  the per-zone `ALLOW-AXFR-FROM` rows open transfers to a secondary.
