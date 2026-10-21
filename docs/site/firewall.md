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
blocklist (CAPI) and the bouncers drop the IPs on it. That is the only list
the panel subscribes to. Security → CrowdSec → **Blocklists** shows how many
active decisions each source holds.

The panel does not use the CrowdSec console (app.crowdsec.net): console
enrolment was removed, and `jabali update` turns console alert forwarding off
on hosts that had enrolled (ADR-0113). Blocklists that are subscribed through
the console are not received.

IPs imported with `cscli decisions import` are enforced like any other
decision and show under **Blocklists** as `cscli-import`. The panel does not
remove them; they expire with the duration given at import.

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
