# ADR-0066: Let's Encrypt cert for the panel hostname

**Date**: 2026-04-26
**Status**: accepted
**Deciders**: shukiv

## Context

Until M32, the panel hostname's TLS cert at `/etc/jabali/tls/panel.{crt,key}` was always a self-signed openssl-generated cert (created by `provision_tls_cert` in `install.sh`, ADR-0048). Browsers showed "Not Secure" on `https://<panel-hostname>:8443/` (admin panel) and `https://mail.<panel-hostname>/` (Bulwark webmail). Operators running Jabali on a public VPS reasonably expected an automatic Let's Encrypt cert there, the same way every customer-domain SSL cert is auto-issued via the existing certbot plumbing.

There was no flow to issue LE on the panel hostname because:
- Panel runs on `:8443`, but LE HTTP-01 challenges land on `:80`.
- Historical assumption: panel hostname might be private/lab (e.g. `panel.local`).
- The mail SAN (`mail.<hostname>`) covered Bulwark via the same self-signed cert; LE would need to cover both via SAN.

## Decision

Add an opt-in Let's Encrypt issuance flow for the panel hostname covering `<hostname>` + `mail.<hostname>` SAN, controlled by a `panel_certificate.use_le` admin toggle gated behind a publicly-routable hostname check. Self-signed remains the silent fallback.

Implementation (M32, 6 steps):

1. **Singleton `panel_certificate` table** (migration `000075`) tracks status state-machine + `use_le` + `staging` + `attempt_count` + `next_retry_at` + `last_error`. CHECK constraint enforces `id = 1`.
2. **`PanelCertRoutability` service** gates issuance: skips `localhost` / `*.local` / `*.localdomain`, skips when DNS A-records don't include `server_settings.public_ipv4`. Port-80 reachability is **not** probed from panel-api because a local TCP probe doesn't prove what the LE validation server can reach.
3. **`ssl.panel.issue` agent command** wraps the existing `certbot.Runner.Issue` against `/var/www/jabali-panel-acme` webroot, then invokes the deploy-hook script directly so first-issue runs the same copy + reload chain that certbot's daily timer fires on every renewal.
4. **`/etc/letsencrypt/renewal-hooks/deploy/jabali-panel-cert.sh`** copies `fullchain.pem` and `privkey.pem` into `/etc/jabali/tls/panel.{crt,key}` (root:jabali, 0640) atomically via `install -m`, then reloads nginx → restarts jabali-panel → restarts jabali-bulwark.
5. **Admin REST**:
    - `GET /admin/panel-certificate` — singleton row + live routability decision.
    - `POST /admin/panel-certificate/toggle` — PATCH-style update of `use_le` / `staging`.
    - `POST /admin/panel-certificate/issue` — force an immediate dispatch.
6. **Reconciler hook** (`Reconciler.reconcilePanelCertificate`, called from `ReconcileAll`) drives the periodic state machine: dispatches on `self_signed → pending_acme`, retries every 3h on `pending_acme_retry`, leaves `issued` rows alone (certbot's daily timer + deploy-hook handle renewal).
7. **Admin UI** — new `PanelSSLCard` embedded in Server Settings → General tab, between SSH Access and the Save button. Status tag, routability badge, expiry hint, two switches (`use_le` and `staging`), and a "Retry now" button gated to failed/retry states.
   *Amended 2026-10-18 (GH #2166):* the controls moved to the **Panel SSL** tab of SSL Manager (`/jabali-admin/ssl?tab=panel`), with the served certificate's type, issuer, names, validity and fingerprint. The Server Settings card now shows each certificate's status and links there.

*Amended 2026-11-16 (new installs turn it on by themselves):* the toggle stays, and existing servers keep their setting, but a new install no longer waits for the admin to find it. Its hostname row is created with `le_auto_until` = now + 7 days (migration `000353`; a NULL column, so every existing server has no window). While the window is open and `use_le` is off, the reconciler asks public resolvers (not the box's own, which may answer from its own zones) at most every 10 minutes whether the panel hostname resolves to `server_settings.public_ipv4`, with every IPv4 record pointing there. It starts 15 minutes after the row is created, so install.sh has finished nginx, certbot and the agent, and it also needs the usual routability check the issue runs to agree. Once both do, it sets `use_le` (a guarded UPDATE, so an admin's choice made in between wins) and the next pass issues as usual. It doesn't for a free `<label>.jabalihosted.com` name, whose certificates come out of one Let's Encrypt allowance shared by every such server, or without an admin email. That first, automatic attempt is followed until it ends: issued closes the window; a failure (`pending_acme_retry` or `failed`) turns `use_le` back off, the panel stays self-signed and the reason is logged (the next self-signed pass clears `last_error`), with no 3-hourly retry the admin never asked for. When the window passes, Let's Encrypt is left to the admin. The mail certificate follows `use_le` as when the admin turns it on: requested once `mail.<hostname>` resolves publicly, parked with the reason and rechecked every 3 hours until then (no ACME request); it never decides the automatic attempt. Any admin choice ends the window: the toggle's `use_le`, and an issue with Let's Encrypt on (UI, API or `jabali panel-cert`), after which a failure is parked for a retry as before. Risk: each reinstall of a server on a public hostname draws a new certificate, against Let's Encrypt's limit of 5 duplicate certificates a week for the same name.

*Amended 2026-11-23 (free hostnames too):* the automatic first issue now also covers a free `<label>.jabalihosted.com` panel hostname. A new install on one stayed self-signed until its admin found the switch, which is the problem the 11-16 amendment set out to fix. `jabalihosted.com` is not on the Public Suffix List, so these certificates share Let's Encrypt's limit of 50 new certificates a week for the registered domain. Renewals are exempt, so the limit is spent only by new servers, reinstalls and the free hostnames' preview wildcards (`wildcard.<label>.jabalihosted.com`). That is the use the shared allowance exists for. If installs ever approach it, the answer is a Public Suffix List entry for `jabalihosted.com`, not leaving new servers self-signed.

## Alternatives Considered

### A. DNS-01 challenge instead of HTTP-01

- **Pros**: Works behind firewalls; supports wildcard certs; doesn't depend on `:80` reachability.
- **Cons**: Requires a DNS provider plugin per registrar; the panel's PowerDNS instance would need a credentialled hook script; significantly more moving parts. Most Jabali installs already serve `:80` for customer-domain ACME, so HTTP-01 reuse is essentially free.
- **Why not**: HTTP-01 reuses the existing `:80` listener and `certbot.Runner.Issue` path with zero new dependencies. M32.1 can add DNS-01 if firewalled installs ask for it.

### B. New separate `:80` listener for panel-hostname ACME only

- **Pros**: Cleaner separation from the customer-domain default vhost.
- **Cons**: Conflicts with the existing default `:80` server block; admins running an actual website on the panel hostname would have no escape; adds a maintenance burden (one more nginx server{} block to keep aligned with PHP/SSL conventions).
- **Why not**: Adding `location ^~ /.well-known/acme-challenge/` to the existing default `:80` server block (with `^~` to outrank regex locations and the catch-all `return 444`) is a 5-line change and reuses everything.

### C. Replace self-signed with a longer-lived in-house CA

- **Pros**: Avoids dependency on Let's Encrypt; works in airgapped environments.
- **Cons**: Doesn't solve the browser "Not Secure" warning unless every admin's machine trusts the in-house CA; managing a CA chain for a single-host panel is gross overkill.
- **Why not**: We want browser-acceptable certs out-of-the-box for public installs.

### D. Make Let's Encrypt mandatory; remove self-signed entirely

- **Pros**: Simpler code path; one cert source.
- **Cons**: Breaks lab/dev installs (`*.local`) that legitimately can't reach LE. install.sh would have to refuse to bring up the panel until a routable hostname is configured, which is a lousy first-run experience.
- **Why not**: Self-signed as a silent fallback keeps the simple-case install working; routable installs can opt into LE without ceremony.

## Consequences

### Positive

- Public installs get a proper LE cert on the panel hostname with one toggle click.
- Mail SAN coverage means `mail.<hostname>` Bulwark webmail also gets a Firefox-clean cert.
- Renewal runs through certbot's existing systemd timer; the deploy-hook is idempotent.
- Lab/dev installs (`*.local`) silently fall back to self-signed — no breaking change.
- The state machine and retry pacing pattern mirrors `ssl_certificates` so the reconciler logic is familiar.

### Negative

- `mail.<hostname>` SAN needs `mail.<hostname>` DNS to resolve to the same IP; if an operator splits webmail to a different host later, they'll have to detach this SAN. M32.1 follow-up if it shows up.
- Each renewal restarts jabali-bulwark (~3-5s webmail blip). Acceptable; documented in runbook.
- Brief ~100ms TLS-handshake gap on jabali-panel restart per renewal (panel-api caches the cert at startup and doesn't SIGHUP-reread).
- DNS-01 / wildcard not covered.

### Risks

- **LE rate-limit burned during testing.** Mitigated by a `staging` toggle separate from `use_le`. UI defaults staging OFF; runbook tells operators to flip it on for first-attempt rehearsal.
- **Hostname change while LE cert active.** Mitigated by the existing M6.4 hostname-drift detector + the routability gate re-running every reconciler tick. Stale cert remains until expiry.
- **nginx :80 ACME location collides with a customer domain hosted on the panel hostname.** Mitigated by `^~` modifier and by refusing to register a customer domain whose name matches `server_settings.hostname` (existing M6.4 guard).
