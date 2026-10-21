# Firewall

UFW + CrowdSec, with a strict division of labor since M43 (ADR-0089).

## Division of labor

| Concern | Owner |
|---|---|
| Which ports are open at all | **UFW** |
| Which IPs may reach an open port | **CrowdSec** decisions |
| Which IPs are temporarily blocked because of bad behavior | **CrowdSec** scenarios + decisions |
| WAF (request-content inspection) | **CrowdSec AppSec** |
| Per-user outbound egress | **nftables + cgroup v2 vmap** (M34) |

UFW is **demoted**: no `ufw deny from <ip>` rules. The CrowdSec bouncers consult decisions in real time and reject at the IP layer (nftables for the network bouncer, app-level for nginx/Stalwart bouncers).

## Port baseline

Standard rules the installer applies:

```
ALLOW 22/tcp     # SSH / SFTP
ALLOW 25/tcp     # SMTP MTA
ALLOW 53/tcp     # PDNS
ALLOW 53/udp     # PDNS
ALLOW 80/tcp     # HTTP (incl. ACME HTTP-01)
ALLOW 443/tcp    # HTTPS
ALLOW 465/tcp    # SMTP submission TLS
ALLOW 587/tcp    # SMTP submission STARTTLS
ALLOW 993/tcp    # IMAPS
ALLOW 995/tcp    # POP3S (only if POP3 is enabled in Stalwart)
DENY  IN ALL     # default deny
ALLOW OUT ALL    # outbound left open by default (constrained per-user by nftables M34)
```

Add / remove ports at `/jabali-admin/security` → UFW.

## Blocklists

Besides its own detections, CrowdSec pulls the free CrowdSec community
blocklist (CAPI) and the bouncers drop the IPs on it. The admin can add more
lists (see [Extra blocklists](#extra-blocklists)). Security → CrowdSec →
**Blocklists** shows how many active decisions each source holds.

The panel does not use the CrowdSec console (app.crowdsec.net): console
enrolment was removed, and `jabali update` turns console alert forwarding off
on hosts that had enrolled (ADR-0113). Blocklists that are subscribed through
the console are not received.

IPs imported with `cscli decisions import` are enforced like any other
decision and show under **Blocklists** as `cscli-import`. The panel does not
remove them; they expire with the duration given at import.

### Extra blocklists

The **Extra blocklists** card on the same tab adds public lists on top of
the community blocklist. All are off by default.

- **FireHOL level 1**: Spamhaus DROP hijacked networks, DShield's top
  attacking networks and Feodo botnet controllers. Very few false positives.
- **FireHOL level 2**: addresses seen attacking in about the last 48 hours
  (blocklist.de, DShield, GreenSnow). Some false positives.
- **FireHOL level 3**: addresses reported in the last 30 days
  (BruteForceBlocker, CI Army, DShield, VXVault and others). Catches more,
  with more false positives.
- **IPsum level 3**: addresses found on at least 3 of the 30 or more public
  attack and malware lists [IPsum](https://github.com/stamparm/ipsum) merges
  each day. Few false positives.
- **Spamhaus DROP (IPv6)**: IPv6 networks hijacked or run by spammers and
  cybercriminals. The IPv4 DROP list is part of FireHOL level 1. Very few
  false positives.
- **Your own lists**: up to 10 https URLs of plain-text lists, one IP or
  CIDR per line (`#` and `;` comments are skipped). The URL may not point
  at a private address or at this server.

The panel downloads each list that is on once a day and bans every entry,
whole networks included, on every port (websites, mail and SSH) for 48 hours
from that download, so a host whose panel is down for a day keeps its lists.
These are never banned: private and reserved ranges, entries wider than /8
(IPv4) or /29 (IPv6), this server's own addresses, CrowdSec allowlist
entries, and the addresses Google and Bing publish for their crawlers. A
listed network that holds one of these addresses is left out whole. If the
crawler addresses cannot be loaded and no earlier copy is on the server, the
lists are not refreshed that day and their bans stay until they expire.

Turning a list off, or removing one of your own, lifts its bans when the
run finishes, within a minute or two. An address added to a CrowdSec
allowlist is let in within a few seconds, and banned again within a few
seconds of its removal if a list still holds it; an entry that expires is
banned again on the next refresh. A visitor wrongly listed stays
blocked until you turn the list off or allowlist them. The card shows, for
each list, how many entries the last run banned and how many it left out
and why.

These bans are not CrowdSec decisions, so they do not show under
**Blocklists**. The agent loads them into its own nftables table, which
drops traffic from the listed addresses on input and forward:

```bash
nft list table inet jabali_lists
```

The agent rebuilds the table when it starts, so the lists survive a reboot.
If the CrowdSec allowlists cannot be read, the table is left as it was, and
the card says the firewall was not updated.

Earlier versions imported the lists as CrowdSec decisions (origin
`cscli-import`, scenario `jabali-list:<list>`), which banned only the first
address of each network. After the update, those of a list that is still on
expire within 48 hours; those of a list that is off are deleted.

## Migrating from old `ufw deny` rules

If you migrated from a setup that used UFW for IP banning:

```bash
jabali ufw migrate-ip-bans
```

This walks `ufw status numbered`, lifts every `from <ip>` deny rule into a CrowdSec decision with reason `migrated-from-ufw`, then deletes the UFW rule. Idempotent — safe to re-run.

## Per-user egress

See [security.md](./security.md#per-user-egress-firewall-m34). The default policy is "outbound 443 + mail submission to localhost only, everything else dropped"; admin overrides per-user.

## What about IPv6?

Same port rules apply (`ufw default deny incoming`, manual allow per port). CrowdSec scenarios match v4 and v6. AppSec is protocol-agnostic.
