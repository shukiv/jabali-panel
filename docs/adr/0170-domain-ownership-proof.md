# ADR-0170: Domain ownership proof before a tenant domain goes live

**Status:** Accepted (2026-09-28). Built in PR #1923 (GH #1816). The five open decisions were answered as recommended (see "Decisions"), and the build changed some details of this design (see "Corrections from the build"). Where the two disagree, the corrections win.
**Driven by:** GH #1816 (follow-up to GH #1789).
**Related:** ADR-0047 (pdns-recursor forwards hosted zones to pdns-auth), ADR-0073 (Stalwart directory converger), ADR-0157 / ADR-0164 (automation and billing API), ADR-0168 (dedicated authoritative DNS cluster), GH #1789 (cross-tenant suffix collision), GH #1812 (subdomain delegation), GH #1898 (restore runs the name guards), JAB-236 (domain delete tombstones).

## Context

A tenant can add any domain name that no other row already holds. `domainops.Create` checks:

- the syntax;
- alias collisions (GH #1625);
- the cross-tenant suffix rule (GH #1789);
- the mail-hostname collision (JAB-390);
- the package quota.

It never checks that the tenant controls the name in public DNS. DNS management is on by default. Once the row exists, the reconciler publishes the zone, adds a recursor forward, issues a certificate, writes the vhost, and (with mail on) registers the domain with Stalwart.

### Threats

A row without proof leads to these outcomes:

- **T1. Dangling delegation, public takeover.** At the registrar, the domain's NS records still point at this server's nameservers. Either a former customer left, or the NS were set before anyone added the domain here. A tenant who adds the name gets this box's pdns-auth answering the public for it. Let's Encrypt HTTP-01 then succeeds, because the A record this box publishes points at itself. The result is a public takeover with a valid certificate.
- **T2. Box-local resolution hijack (cross-tenant, no external condition).** Under ADR-0047, pdns-recursor owns `127.0.0.1:53`. It forwards every hosted zone to the local pdns-auth through `/etc/powerdns/recursor.forwards` (`reconcileRecursorForward`, one line per zone). Suppose a tenant adds `api.stripe.com` or `gmail.com`. Every process on the box that resolves that name then gets the tenant's records, including:
  - other tenants' PHP apps calling that API;
  - Stalwart looking up MX records for outbound mail;
  - the panel itself.

  The real domain's public DNS does not need to be touched. This is the worst threat. GH #1789 does not stop it, because the suffix rule only protects names another tenant already hosts here.
- **T3. Local mail interception (cross-tenant).** With mail on, the domain's mailboxes become local recipients for Stalwart. The `queryRecipient` SQL (install.sh converger, ADR-0073) selects from `mailboxes` directly and never joins `domains`. Mail that another tenant, or a web app on the box, sends to that domain is delivered locally to the claimant instead of to the real MX. I don't know whether Stalwart checks its own domain list before the directory lookup. This design does not rely on it and filters inside the directory query.
- **T4. Dangling A record.** The name's public A record still points at this server's IP. Adding the name gives the tenant the vhost `server_name` and a certificate through HTTP-01. The impact is lower, because the real owner left the record dangling, but it is the same class of problem.
- **T5. Shared-certificate poisoning (availability).** An unproven name added to a shared SAN certificate can make the Let's Encrypt order fail for every name on that certificate.

### What exists already

`internal/dnsverify` queries 1.1.1.1, 9.9.9.9 and 8.8.8.8 directly (UDP, then TCP) and never goes through the local recursor. The helpers are:

- `LookupHostExternalResult`, which reports `queried=false` when no resolver gave a usable answer;
- `LookupNSExternal`.

There is no public-resolver TXT helper yet. `LookupTXTOnServer` asks one named server, for the ACME pre-check.

### The limit of any DNS-based proof

A proof through public DNS works only while someone other than this server serves the domain's public DNS. Consider a domain whose public NS already point here, as in the usual "point the nameservers first, then add the domain" onboarding. For that domain, the only DNS the claimant can change is the DNS on this box, and that is the thing being proven. DNS alone cannot tell the real owner who pointed their NS here apart from an attacker who found a dangling delegation (T1).

An HTTP proof fails the same way whenever the A record points here. Those domains need another path: admin approval, or an explicit assertion from a trusted billing system (decision 1). This limit drives most of the decisions below.

## Decision

### 1. Ownership state on each domain row

Add these columns to `domains`:

| Column | Type | Meaning |
|---|---|---|
| `ownership_status` | `varchar(16) NOT NULL DEFAULT 'pending'` | `pending` or `verified`. Any other value is treated as `pending`. |
| `ownership_method` | `varchar(16) NOT NULL DEFAULT ''` | `dns_txt`, `admin`, `parent`, `legacy`, `migration`, `restore`, `automation`. |
| `ownership_verified_at` | `datetime NULL` | When the row became verified. |
| `ownership_token` | `varchar(64) NOT NULL DEFAULT ''` | 32 random bytes from `crypto/rand`, hex-encoded. A new token is made for every row and on every rename, and a token is never reused. It is stored in plain text because it becomes public once the tenant publishes it. |
| `ownership_checked_at` | `datetime NULL` | The last check, used for backoff. |
| `ownership_last_result` | `varchar(64) NOT NULL DEFAULT ''` | For example `not_found`, `mismatch`, `resolvers_unreachable`. It is shown to the tenant. |

The database default is `pending`. Any door that inserts a row without setting the status therefore fails closed, including the raw `Domains.Create` doors listed in section 6. The GORM tag carries `default:pending`, so an empty Go string lets the database default apply.

`domain_repository`'s generic update is a Select allow-list (`domain_repository.go:450`). The new columns get dedicated setters and never go through that update, so no write can be dropped silently.

Migration: add the columns, then backfill every existing row to `verified` / `legacy`. The backfill DML is the last statement of the migration, so an older binary never meets a half-migrated table. Without the backfill, every live site would go dark on update.

### 2. One predicate, every gate routed through it

`domainops.OwnershipVerified(d)` returns true only for `ownership_status = 'verified'`. The posture helpers that already decide "zone on", "mail on" and "SSL on" consult this predicate, and every gate point below calls it. A missed gate is then something a grep finds.

For a pending row, the desired state is that each item below is **absent**. The reconciler converges to that state as it converges everything else (ADR-0004). It does not merely skip the pending row. As a result:

- a row that goes back to pending is cleaned up by the next tick;
- a teardown that half-failed heals on a later tick;
- drift heals too.

The gate covers every pending row, including a domain with DNS management off (`dns_disabled`). T3 and T4 do not need the panel to serve the zone. A DNS-off domain is also the easy case for the TXT proof, because its DNS is already hosted elsewhere. This is deliberately wider than the issue's "before DNS management is enabled".

While a domain is pending:

- **No zone is published to authoritative DNS.** This covers local pdns-auth and, once ADR-0168 lands, the dedicated cluster. `reconcileDNSZone` makes sure no zone is published for it. The tenant can still edit records in the panel; they are stored but not published.
- **No recursor forward exists.** `ReconcileOne` and the enabled-domains loop in `ReconcileAll` both remove any forward for a pending row, because a pending row is still an enabled row today.
- **No mail:**
  - The `EnableMail` hook is skipped.
  - The mailbox, forwarder and mail-group create endpoints refuse with 409 `domain_ownership_pending`.
  - The Stalwart directory queries (`queryRecipient`, `queryEmailAliases`, and `queryLogin`, see Corrections) join `domains` and require `ownership_status = 'verified'`. They are edited in BOTH `apply-plan.json.tmpl` and the install.sh ADR-0073 converger, and the CI parity guard covers the change.

  The query filter is what closes T3 for a row that goes from verified back to pending.
- **No certificate:**
  - The `InlineSSL` hook is skipped.
  - The reconciler's SSL step skips the domain.
  - The name is never attached to a shared certificate: create-time attach and `ReconcileSSLSANDrift` both leave pending names out (T5).
- **No real-name vhost.** The vhost does not carry the name in `server_name`. The site is served on its preview URL only, so the tenant can build the site while waiting.
- **Web aliases** (`POST /domains/:id/aliases`) are not added to `server_name` or the certificate SAN while the alias's own name is unproven (section 5).

What stays available: the row, the docroot, the PHP pool, files, databases and the preview URL. The name is also reserved, so no other tenant can take it while it is pending. Decision 4 covers expiry, so that nobody can squat names.

Unknown or unreadable status is treated as pending. No gate fails open.

### 3. Proof: a DNS TXT record checked through public resolvers

- **Record:** `_jabali-challenge.<name>` with the value `jabali-verify=<token>`. The domain page shows it with a copy button. The tenant adds it at the domain's current DNS provider. Records added in the panel do not count, because a pending zone is not published.
- **Lookup:** a new `dnsverify.LookupTXTExternal(ctx, name) (txts []string, queried bool)`, modelled on `LookupHostExternalResult`. It asks each public resolver directly and never uses the local recursor.
- **Match rule:**
  - The domain is verified only when at least two of the three resolvers return the exact value, compared as a whole string.
  - If fewer than two resolvers answer (`queried=false`, timeouts, SERVFAIL), the domain stays pending with `resolvers_unreachable`.
  - If the resolvers answer without the value, the domain stays pending with `not_found` or `mismatch`.
- **Triggers:**
  - The tenant's **Verify now** button: `POST /domains/:id/ownership/verify`, owner or admin only. It is rate-limited per user, because each click fans out to three public resolvers.
  - A reconciler re-check with backoff, so a domain goes live without a click. The checks run after 1, 5 and 15 minutes, then hourly for 7 days, then daily until the pending expiry.
- **On success:**
  - The status becomes `verified` with method `dns_txt`, and `verified_at` is set.
  - `ReconcileOne` runs for the domain. The zone, the forward, the certificate, the vhost name and the mail steps that were skipped at create time now run.
  - The tenant gets a notification, and the admin bell records it.
- **Nameservers already point here.** `LookupNSExternal` may show that the name's public NS are this server's `NS1Name`/`NS2Name`. In that case the TXT check can never pass (see "The limit of any DNS-based proof"). The check records `ns_points_here` and stops the scheduled re-checks for that row. The tenant sees "Your nameservers already point to this server. An administrator must approve this domain."
- **One-time proof.** The TXT record can be removed after verification. There is no re-check once the domain is verified (decision 5).

### 4. Rows verified at create time without a TXT proof

- **Admin actor:** REST with `claims.IsAdmin`, and `jabali domain create`. The method is `admin`. Admins already bypass GH #1789 and are trusted to know what they add.
- **Parent rule:** the rule looks at the **nearest hosted ancestor** of the name, the closest parent that has a row. The name is verified with method `parent` when both of these hold:
  - that ancestor is verified;
  - the same user owns it, or its owner set `allow_subdomain_delegation` (GH #1812).

  Consent from someone who proved control of the parent counts as proof. When the nearest hosted ancestor is pending, the name stays pending, even if a verified domain sits further up. So a pending `b.a.com` never lets `c.b.a.com` skip it.
- **Panel's own row** (`cmd/server/panel_primary_cmd.go`) and **admin docker apps** (`api/docker_apps.go`, both create sites): the method is `admin`.
- **Migration pull and backup restore:** see decision 2.

The caller states the ownership outcome through a new field, `CreateInput.Ownership{Verified bool; Method string}`. It is separate from `ActorIsAdmin`. The migration and restore doors deliberately pass `ActorIsAdmin=false` so that the name guards still run (GH #1898). Reusing that flag would either skip the guards or wrongly deny verification.

### 5. Rename, aliases, admin approval and revocation

- **Rename** (`domains_rename.go`) applies the section 4 rules to the new name, with a new token. When the new name is not covered, the row goes to pending and is torn down as described below.
- **Web aliases:** an alias is covered when it sits under a verified domain of the same owner. Otherwise it needs its own TXT proof, with the same flow keyed to the alias.
- **Admin approval.** An admin page lists pending domains with the last check result. **Approve** sets `verified` / `admin` and is audited. This is the path for NS-first onboarding and for boxes whose outbound DNS is blocked.
- **Revoke:** an admin action, audited, that sets `pending`.
- **Going from verified back to pending** (revoke, a rename that is not covered, or expiry). The status flips, and the next reconcile tick converges the row to the pending state from section 2:
  - the zone is unpublished;
  - the recursor forward is removed (`reconcileRecursorForwardRemove`);
  - the name leaves the vhost and the certificate SAN.

  The Stalwart directory filter stops local delivery the moment the status flips. The asynchronous pieces (deleting the Stalwart domain, certificate cleanup) reuse the domain-teardown machinery (JAB-236). Without this convergence, the gate would protect only new domains.
- **Cascade to `parent` rows.** A row verified with method `parent` got its proof from its ancestor. When that ancestor goes back to pending, every descendant row whose `parent` proof runs through it goes pending in the same transaction. Descendants proven by their own TXT record or by an admin keep their status.

### 6. Every door that creates a row

| Door | Where | Outcome |
|---|---|---|
| REST create | `api/domain_create_op.go` via `createDomainOp` | Tenant: pending. Admin: `admin`. |
| Automation / billing API | `api/automation_billing.go` `createAutomationDomain` → `createDomainOp` (`ActorIsAdmin` false) | Decision 1 |
| CLI `jabali domain create` | `cmd/server/cli_create.go` | `admin` |
| Migration pull | `cmd/server/migrate_pull_cmd.go` | Decision 2 |
| Backup restore (raw insert) | `backupmetadata/apply.go` | Decision 2 |
| Panel's own row (raw insert) | `cmd/server/panel_primary_cmd.go` | `admin` |
| Admin docker apps (raw insert) | `api/docker_apps.go` (two sites) | `admin` |
| Tenant docker apps (raw insert) | `api/docker_apps_user.go` | Pending. Reroute it through `domainops.Create`, since a raw tenant door is the gap GH #1898 closed elsewhere. |
| Rename | `api/domains_rename.go` | Section 5 |
| Web aliases | `api/domain_aliases.go` | Section 5 |

Admin-only hostnames (the panel hostname, the JAB-390 mail hostname, webmail hostnames) are outside this ADR.

### 7. Policy switch

A server setting `domains.require_ownership_proof`, on by default on fresh and existing boxes. Existing rows are grandfathered by the backfill. Decision 3 covers whether to ship an off switch at all.

### 8. Build order

All slices ship in the same release. Slice 1 must never be released alone, because tenants would then have no way to verify except admin approval.

1. Schema and backfill, the predicate, every gate in section 2, the `Ownership` field on each door, and the admin approve and revoke API and CLI. The tests, each of which fails on the code before this change, show that:
   - a pending row gets no zone, no forward, no Stalwart recipient and no certificate;
   - a verified row flipped to pending loses its forward on the next `ReconcileAll` with no other action.
2. `dnsverify.LookupTXTExternal`, the verify endpoint, the reconciler re-check with backoff, and notifications.
3. UI: a pending banner with the TXT instructions and **Verify now** on the tenant domain page, and the admin pending list.
4. Rename, aliases, and teardown on revoke.

## Consequences

**Positive**

- T1 to T5 are closed for every new name.
- T2 and T3, the cross-tenant threats, no longer depend on the real domain's DNS being dangling.
- A single predicate plus a fail-closed database default means a forgotten door or gate stays pending rather than going live.

**Negative**

- NS-first onboarding needs admin approval or a billing assertion, which is an extra step for real customers.
- Tenants see a new step and a waiting state, and support will get questions about it.
- Pending names reserve a name until they expire.
- The Stalwart directory queries gain a join, in two templates that must stay identical.

## Alternatives considered

- **NS delegation as proof** ("the public NS point at us"). The dangling-delegation attacker in T1 has exactly that. Rejected.
- **HTTP proof** (`/.well-known/jabali-challenge/<token>` fetched through the name's public A record). It fails whenever the A record points here, which is what the T4 attacker has. It proves control only when the A record points elsewhere, and then the tenant could add a TXT record just as easily. It might be offered as a second method later; it is not part of v1.
- **Publish a challenge-only zone for pending domains.** This brings back T1 for the challenge name and adds no strength over a TXT record at the current provider. Rejected.
- **Gate DNS publishing only, keep the recursor forward.** That leaves T2, the worst threat, open. Rejected.
- **Drop `recursor.forwards` entirely.** ADR-0047 exists so the box resolves its own hosted domains before the public delegation exists, and so local mail between hosted domains works. Gating the forward per domain keeps that for verified names. Rejected.
- **Periodic re-verification after go-live.** It would catch a domain that changes hands after its registration lapses, but it risks taking a live site dark over a resolver hiccup. Left as decision 5.

## Decisions (accepted 2026-09-28)

1. **Automation / billing API creates:** option B. A domain an automation token creates is pending unless the token holds `assert:domain_ownership`. That scope is its own family; no wildcard, `write:*` included, implies it. The token form lists it, and it is off unless an admin ticks it.
2. **Migration pull and backup restore:** admin-run migrations create `verified` / `migration` rows, and admin-run restores create `verified` / `restore` rows. Every door that restores a domain row is admin-run today, so no tenant restore creates a verified row. A row the archive records as pending stays pending.
3. **Default on everywhere, with an off switch.** Existing rows are grandfathered as `legacy`. The admin off switch exists on every box, with a warning, and rows created while it is off get `policy_off`. They never go back to pending when proof is switched on again.
4. **Pending expiry:** 14 days, with a notice at day 10. The release goes through `domainops.Delete`, and the site files stay.
5. **No re-verification after go-live.** The proof is one-time; admin revoke covers disputes.

## Corrections from the build

- **Schema.** Besides the section 1 columns, each row has `ownership_pending_since`, `ownership_next_check_at` and `ownership_expiry_notified_at` (migration 000311). `web_domain_aliases` carries the same state. The switch lives in its own table, `domain_ownership_settings` (a missing row means proof is required), not in server settings. The method list gains `policy_off`.
- **No real-name vhost** (section 2) is built as a gate, not by leaving the name out of `server_name`. The pending vhost keeps the real name. Its :80 and :443 blocks return 444 for every request unless the header `X-Jabali-Preview-Gate` carries the domain's gate value: a hash of the row id and the challenge token, which changes with the token. Only the preview URL's proxy sends that header. A browser never sends it on its own, so knowing the value does not help a visitor.
- **More gates than section 2 lists.**
  - A published MTA-STS policy is disabled, and re-applied after verification.
  - The sendmail relay identity is retired: the credential file is removed and the relay mailbox password rotated. It is restored after verification.
  - A pending docker-app domain's proxy vhost is removed.
  - The DNS-01 ACME hook refuses to push a pending domain's zone (a lookup error refuses too).
  - The recursor backfill command plans a forward only for an enabled, verified domain.
  - The mailbox, shared-resource, mail-group, forwarder and shared-certificate doors refuse with 409 `domain_ownership_pending`, and so does mail enable.
- **Stalwart `queryLogin` is filtered too** (decided 2026-09-28, after the box test). Without it, a mailbox on a domain an administrator sends back to pending could still sign in and send as that name. Now it cannot sign in at all (IMAP, POP3, SMTP submission, JMAP, webmail). Its mail stays stored and is reachable again once the domain is verified. Send-only accounts still sign in on verified domains, as before (GH #371). New pending domains have no mailboxes, because the mailbox door refuses them, so this only affects a revoked domain.
- **The JMAP login cache.** Stalwart 0.16 answers an HTTP (JMAP, webmail) login from its "HTTP Authorization" cache, which has no expiry, without asking the SQL directory again. IMAP and SMTP refuse a revoked domain's mailbox at once; JMAP kept accepting a cached login. The same cache kept an old password and a disabled mailbox working over JMAP, on main before this change. So a revoke that sent any domain back to pending calls the agent verb `mail.auth_cache.flush` (Stalwart `InvalidateCaches`), after the rows are pending. The flush is best-effort: the revoke already happened, and an agent that predates the verb only logs. The CLI revoke sets up the agent client for it. The verb and the flushes on a password change, disable and delete ship separately, as a security fix on main.
- **Directory queries and the update order.** On `jabali update`, `provision_new_software` runs the Directory converger before `migrate up`. A query naming `domains.ownership_status` before migration 000311 has run fails every login and delivery, and stays broken if the update stops in between. So `converge_stalwart_directory_queries` writes the filtered queries only when the column exists, and otherwise keeps the pre-#1816 queries. `jabali update` then runs it again in a step right after the migrations. The converger reloads Stalwart's settings itself, because an updated Directory takes effect only after a reload.
- **Combined with the directory rules main gained meanwhile** (2026-10-01, when this change was brought up to date). The filtered queries keep every rule main's queries have, and add the ownership filter to each branch:
  - `queryLogin` keeps the suspended-owner check (a NOT EXISTS on `users.suspended`).
  - The alias branches of `queryRecipient` and `queryEmailAliases` keep the rule that an alias at a mailbox's own address is ignored (migration 000306).
  - The postmaster fallback (ADR-0110, migration 000309) requires the domain whose `postmaster@` is asked for to be verified. Otherwise mail to `postmaster@` of an unproven name would land in the admin's mailbox instead of going to the real MX.
  - On a box that has not run migration 000311 yet, the converger keeps main's queries, these rules included, without the ownership filter.
- **Check backoff.** Every minute for the first 5 minutes, then every 5 minutes to 30 minutes, every 15 minutes to an hour, hourly to 7 days, and daily until the name expires. The ticker runs every minute, on a primary only. A standby, or a server whose role cannot be read, skips the tick.
- **Nameservers that point here.** A check that finds `ns_points_here` backs off to one check a day; it does not stop checking, so a later nameserver change is still noticed. Administrators get one `domain.ownership.needs_admin` notification each time a row's result becomes `ns_points_here` or `dns_unresolvable`.
- **Rename.** The new name gets the section 4 decision. The domain's own current name never counts as its parent. When the new name is not covered:
  - the row is set pending with a new token before the rename, and restored if the rename fails;
  - the rename is refused with 409 `rename_requires_proven_name` when the domain's mail is registered with Stalwart (a DKIM selector is set), because the mail would move to an unproven name.
- **Web aliases.** An alias gets the create-time decision against its domain's owner. A pending alias is left out of `server_name` and the certificate SAN until its own TXT record proves it. Aliases have the same verify, approve, revoke and 14-day expiry as domains. They have no tenant UI yet (API only). The admin pending page lists them.
- **Tenant docker apps** keep their raw insert, stamped with the ownership decision (pending for a tenant), instead of being rerouted through `domainops.Create`. A docker-app domain never expires and cannot be revoked, because either would strand the running app.
- **Revoke.**
  - It does not revoke the Let's Encrypt certificate. The pending shape swaps in a self-signed certificate on the next pass.
  - The panel's own domain is refused (its pending shape would unpublish the panel's zone and stop its mail), and so are docker-app domains.
  - A revoked row keeps `verified_at`, so it never expires; an administrator resolves it.
- **Audit.** Section 5 audits only the administrator's approve and revoke. The build also records the changes the service makes on its own as `system` audit events on the name's owner: `domain.ownership.verify` (DNS proof or parent rule, with the method) and `domain.ownership.expire`, and the `domain.alias.ownership.*` forms. An approval is recorded once, by its caller.
- **Cascade.** The cascade does not run in one transaction. It runs one conditional update per row, shallowest name first, re-checking the parent rule against fresh rows. It still runs when the revoked row was already pending, so repeating a revoke finishes a cascade that an earlier call could not complete (the API answers 500 `cascade_incomplete`). The rows it sends back also keep `verified_at`.
- **Verification cascades too** (not in the original design). When a name is verified by proof or approval, the pending domains and aliases under it that the parent rule now covers are verified with method `parent`, shallowest first. So a `www` alias or a subdomain added before its parent was proven goes live with it.
- **Switching proof off** leaves the rows already pending as they are: they still need proof or an approval. The CLI's `policy off` needs `--yes`.
- **Expiry and verification cannot race.** The expiry sweep deletes a row only while it is still an expired claim. The delete is a conditional `DELETE`: the row must be pending, never verified, pending since the cutoff or earlier, not the panel's row and not a docker app's. A verification that wins the race keeps the row, and `domainops.Delete` then drops its tombstone and runs nothing host-side.
- **Notifications.** Five event kinds: `domain.ownership.verified`, `.revoked`, `.expiring`, `.expired` and `.needs_admin`. The owner can route all but `needs_admin`, which goes to admins only. A verification and a removal are also recorded in the admin bell.
