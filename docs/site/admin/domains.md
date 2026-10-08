# Domains (Admin)

`/jabali-admin/domains`. The cross-user view of every hosted domain on the panel.

## List

Columns: domain name, owner (username), package, primary / alias, PHP version, SSL state, DNSSEC state, listen IP, suspended flag, created.

Filters: by owner, by package, by SSL state (issued / pending / failed / off), by DNSSEC state, by listen IP, by free-text on the domain name.

## Actions per row

- **Edit** — opens the domain edit page (the same component the user sees, with additional admin-only fields).
- **Disable** — sets `is_disabled=1`; the reconciler returns 503 on every request to the domain.
- **Delete** — destructive; removes the vhost, revokes the SSL certificate, drops the Stalwart domain entry (if mail was enabled), removes DNS zone records. The domain's webmail and MTA-STS vhosts go with it. A server where a deleted domain's MTA-STS vhost was left behind by an earlier release has it removed by the reconciler within a minute of the update: that vhost names the deleted domain's certificate, so `nginx -t` failed for the whole server.

## Create

The admin create flow is identical to the user create flow except the admin chooses the owning user. Use this to provision a domain on behalf of a user during migration or onboarding.

The create form includes a **Mail** picker (Jabali mail / No mail / Microsoft 365 / Google Workspace). Choosing anything other than Jabali stops the panel acting as the mailserver, drops the mail SANs from the certificate, and (for the external providers) publishes that provider's DNS records. See [Mail provider (per domain)](../dns.md#mail-provider-per-domain). It's editable later under Domains → Edit → Email.

## Per-domain settings (admin-only fields)

In addition to the fields a user sees:

- **Force PHP version** — pin a PHP version even when the user's package would allow others.
- **Force listen IP** — pin a listen IP from the [IP pool](./ip-addresses.md) regardless of the user's pick.
- **Pin SSL on** — prevent the user from disabling SSL.
- **Quarantine** — soft-suspend; vhost serves a quarantine page citing the operator's note. Used during incident response.

## Convergence

Every change writes to the `domains` table and schedules `Reconciler.Schedule(<domain-id>)`. The reconciler:

1. Re-renders `/etc/nginx/sites-available/<domain>` from the template, atomically swaps the file, and reloads nginx.
2. Requests or revokes the certificate via the agent, as appropriate.
3. Updates the apex DNS `A` and `AAAA` records to reflect the listen IP.
4. Toggles DNSSEC signing.

Convergence latency is typically under 60 seconds.

## Ownership proof

A domain a tenant adds stays **pending** until its owner proves control of the name with a TXT record (`_jabali-challenge.<name>` = `jabali-verify=<token>`) at the domain's current DNS provider (GH #1816, ADR-0170). A pending domain has no published zone, no recursor forward, no mail, no trusted certificate and no MTA-STS policy, and every request for its real name gets no response (nginx 444). Only its preview URL serves the site, once the owner turns Preview URL on.

A name is **verified at once** when:

- an administrator adds it (the panel, `jabali domain create`, an admin docker app);
- it sits under a verified domain of the same owner, or under a domain whose owner allows subdomains by other accounts;
- an admin-run migration or backup restore brings it in (a row the archive recorded as pending stays pending);
- a billing system creates it with an automation token that holds `assert:domain_ownership`;
- proof is switched off.

Domains that existed before the update were marked verified (`legacy`).

**Web Domains → Ownership proof** (`/jabali-admin/domains/ownership`) lists the domains and web aliases waiting for proof, with the last check result and the date an unproven name is removed. From there you can:

- **Check now** — run the TXT check at once.
- **Approve** — verify a name without a DNS proof. Use it only when you know who owns the name, for example when its nameservers already point here (a DNS record cannot prove such a name, and the check says `ns_points_here`).
- **Require ownership proof** — the server-wide switch, on by default. Switching it off lets tenants claim any name, including names that belong to someone else; names added while it is off stay live when you switch it back on. Domains already waiting stay pending.

The domain's **Edit** page shows its ownership state. A verified domain has **Revoke verification**, which sends it back to pending (its zone, mail and certificate come down on the next reconcile pass, and its mailboxes can no longer sign in; their mail is kept) together with the subdomains and aliases that were verified through it. The panel's own domain and docker-app domains cannot be revoked. A revoked domain is never removed automatically.

A never-verified name is removed after **14 days** (its owner is told 4 days before); the site files are kept. Approvals, revokes and the switch are written to the audit log, and so are the changes the panel makes on its own (a name verified by its DNS record or through its parent, and a name removed after 14 days, recorded with the actor `system`), and the `domain.ownership.*` notification events report verifications, removals and names that need your approval.

## CLI

```bash
jabali domain list
jabali domain enable  <name|id>
jabali domain disable <name|id>
jabali domain delete  <name|id>

# Ownership proof (GH #1816)
jabali domain ownership status  <name|id>   # state and the TXT record
jabali domain ownership verify  <name|id>   # check now
jabali domain ownership approve <name|id>   # audited
jabali domain ownership revoke  <name|id>   # audited
jabali domain ownership pending
jabali domain ownership policy [on|off]     # 'off' needs --yes
```
