# ADR-0168: Dedicated authoritative DNS cluster (central authoritative store + API push)

**Status:** Proposed (2026-09-10), **revised 2026-09-16** to adopt Variant B per the
#1632 scope decision — design spike, no code. Each phase below is gated on separate approval.
**Driven by:** GH #1632.
**Related:** ADR-0011 (PowerDNS + MySQL backend), ADR-0076 (per-domain DNSSEC), ADR-0107 (operator edits authoritative), ADR-0150 (tenant record-type permissions). Runbook: `docs/runbooks/dns-secondary-nameserver.md`.

## Context

#1632 asks for authoritative DNS to be served by a **dedicated cluster**
(`ns1/ns2/ns3.domain.com`) that several Jabali panels feed, instead of each
panel being its own authoritative nameserver at `ns1.<panel-host>`.

The requester (johnnyq) confirmed the target on the issue (2026-09-10 and again
2026-09-15, "Option B is the right direction"): a **central authoritative DNS
tier that the panels push to over an API**, where

- the dedicated cluster is the **only** authoritative, publicly-serving,
  DNSSEC-signed infrastructure;
- panels are **management clients** that push their DNS changes to the central
  DNS API — they do **not** run their own authoritative nameservers and do
  **not** require AXFR *from* the panels;
- **DNSSEC is handled entirely by the cluster**;
- the panel still **retains the desired-state** it needs to generate/reconcile
  records (ACME, DKIM, the mail record set), but is no longer an authoritative
  DNS server.

What already exists today (so the ADR does not re-propose it):

- **NS names are already free-form.** `server_settings.NS1Name/NS2Name` are
  admin-editable varchars (`api/server_settings.go:347-354`, validated in
  `settingsops/validate.go:52`). An admin can set `ns1.domain.com` /
  `ns2.domain.com` right now; the apex NS + SOA MNAME are built from those
  values verbatim (`dnscompile/compile.go:71,90-95`).
- **The panel already computes and pushes zone desired-state.** The reconciler
  compiles each zone from the panel DB (source of truth) and pushes it to a
  PowerDNS backend via the agent's `dns.zone.upsert`. Variant B keeps this
  compile-and-push shape; it changes only *which* PowerDNS receives the push
  (a remote cluster API instead of the panel-local instance).
- **A single AXFR secondary is already supported** (`reconciler.go:2624-2626`,
  `NS2IPv4` → `ALLOW-AXFR-FROM`/`ALSO-NOTIFY`). Under Variant B this AXFR
  mechanism moves **inside the cluster** (member→member replication), not
  panel→cluster.

### Desired-state caveat (must be stated, not glossed)

Variant B is **not** a "stateless pass-through panel". The panel must keep
per-zone **desired-state** because several flows regenerate records from panel
config with no human in the loop:

- **ACME DNS-01.** Every SSL issue/renew writes then deletes an
  `_acme-challenge` TXT on the fly, waits for visibility, then completes.
  Time-critical and automatic.
- **The mail record set** — MX, SPF, DKIM (**the DKIM key lives in the panel**),
  DMARC, autodiscover/autoconfig, client SRVs, TLS-RPT, CAA — is derived from
  the mailbox/domain config (`GET /domains/:id/email` is the authoritative
  producer). A DKIM rotation or mail-host IP change regenerates these.
- **The reconciler self-heals** — SAN-drift reissue, the #1579 domain-rename
  zone re-key, and per-tick convergence all *rewrite* zone records from panel
  desired-state.

So the precise model is: **panel keeps desired-state (to compute records) →
pushes it to the central cluster's DNS API → the cluster holds the only
authoritative, publicly-served, DNSSEC-signed copy and is the single source of
truth for resolution.** We do not promise a stateless panel and then quietly
need local state for SSL and DKIM anyway.

## Industry standard (how other control panels do this)

Every mainstream panel converges on the same shape — **dedicated DNS-only
nodes that receive role-based zone pushes, with a cross-server uniqueness
guard**:

- **cPanel/WHM** — a *DNS cluster* of dedicated **cPanel DNSOnly** nodes. Web
  servers **push complete zone data over the WHM API** (a `dns-cluster` ACL
  token), not by being an AXFR primary. Per-peer **roles** (Standalone /
  Synchronize / Write-only) and a **reverse-trust** relationship stop one web
  server from creating a zone another already owns. This is API-push with a
  central-owned backend — i.e. Variant B — and is the mainstream reference for
  it.
- **HestiaCP** (PowerDNS/BIND) — a *DNS cluster* over the Hestia API. Two modes:
  **Master↔Master** (bidirectional API push; **DNSSEC unsupported**, because N
  masters each try to sign) and **Master→Slave AXFR** (**DNSSEC supported**).
  The DNSSEC×AXFR constraint that bit Hestia is a **two-way-signing** problem —
  see the DNSSEC note below for why it does **not** apply to Variant B here.
- **DirectAdmin** — *Multi-Server Setup* pushes zones on save with a
  remote-uniqueness *Domain Check* guard and a task-queue retry.
- **Plesk** — *Slave DNS Manager*; notably refuses to let several masters share
  one secondary set (a shared control channel lets any master delete any
  master's zones) — the exact cross-panel trust problem Phase 3 must solve.

**Where Jabali already sits:** the panel already *compiles and pushes* zone
data (`dns.zone.upsert`). Variant B redirects that push from the panel-local
PowerDNS to a central cluster API and adds the ownership guard — it is the
cPanel-DNSOnly model on Jabali's existing compile-and-push mechanics.

## Decision

Adopt **Variant B — a central authoritative DNS store fed by panel API push**,
built in phases. Concretely:

- The advertised nameservers are the dedicated cluster members
  (`ns1/ns2/ns3.domain.com`), which run PowerDNS (Jabali already speaks
  PowerDNS, so this is the least-new-code authoritative tier). The cluster is
  the **sole** authoritative, publicly-served, DNSSEC-signing infrastructure.
- **The panel runs no advertised authoritative nameserver.** It keeps
  desired-state and pushes each zone's records to **one** cluster API endpoint;
  its local authoritative PowerDNS is retired on migrated panels (the recursor
  is a separate concern and is unaffected).
- **Intra-cluster replication is the cluster's own job**, not the panel's: the
  panel pushes to a single endpoint; cluster members replicate among themselves
  via native PowerDNS primary→secondary AXFR/NOTIFY. This keeps a single
  authoritative store and avoids the split-brain of the panel writing N members
  independently. **Note this diverges from johnnyq's install sketch** (one API
  secret per NS server: "ns1 + key, ns2 + key, ns3 + key"): a push-to-one model
  needs **one per-panel credential to one cluster endpoint**, not a key per
  member. Which of the two is the intended shape is called out in Open questions
  and must be confirmed with johnnyq before Phase 3.
- One domain is owned by exactly **one** panel at a time, enforced at the
  cluster API layer (the zone-ownership invariant, below).

### The new deployable: a Jabali DNS API in front of cluster PowerDNS

PowerDNS Authoritative's HTTP API authenticates with a **single, server-wide
`api-key`** and has **no per-zone ACL** (confirm against the deployed PowerDNS
version before Phase 3; this is the standard behaviour). Therefore per-panel
credentials cannot isolate zones inside PowerDNS itself. Variant B needs a thin
**Jabali-owned DNS API service** sitting in front of the cluster's PowerDNS:

- Per-panel credentials — **one secret per panel to one cluster endpoint**.
  (johnnyq's install sketch instead lists one secret per NS *server*; the two
  models diverge — see Open questions.)
- A **zone-owner table** mapping each zone to its owning panel. It lives on
  **one designated cluster member** (the write endpoint), not replicated across
  every member — a per-member owner table would itself need cross-member
  consistency, which push-to-one exists to avoid. Writes are a SPOF on that one
  member; reads/serving stay HA across all members via AXFR. (See Open questions.)
- **Refuse-create-if-owned / refuse-write-if-not-owner**, failing **closed** —
  this is the cPanel reverse-trust guard and the Plesk "don't share a secondary
  set" lesson, enforced in Jabali code, not delegated to PowerDNS.
- It is this service (not raw PowerDNS) that the installer's "Authoritative DNS
  Server Only" mode installs and that mints the per-panel secret.

This is the one genuinely new network service Variant B introduces, and its
security model is the crux of the design — it is where multi-panel trust lives.

### DNSSEC — why Variant B keeps it working

In the Master↔Master API-push model, whoever *signs* must be a primary, so N
masters signing fights "no authoritative copy on the panel" — that is the
constraint that bit HestiaCP. In Variant B the **cluster is both the sole
signer and the sole authoritative store**, so signing is centralized and the
constraint does not apply. Consequence to design for (ADR-0076 is impacted):

- Per-domain DNSSEC keys (`pdnsutil`, ADR-0076) **move to the cluster**. The
  registrar **DS record derives from the cluster's keys**, not the panel's.
- The panel's DNSSEC enable/rollover and DS-display flow becomes an **API
  round-trip** to the cluster (fetch DS/DNSKEY to show the operator), rather
  than a local `pdnsutil` call. ADR-0076 must be amended when Phase 3 lands.

### Phase 1 — advertised-member data model

Introduce a dedicated `dns_nameservers` table — **not** more `server_settings`
columns (`server_settings` is at the VARCHAR row-size ceiling; new
NS3Name/NS3IPv4 varchars are exactly how `ERROR 1118` gets tripped — see
`feedback_server_settings_row_ceiling`). Columns: `name`, `ipv4`, **`ipv6`**
(there is no AAAA glue at all today — add it from the start), `sort_order`.
Unlike the earlier hidden-primary draft, there is **no `advertised=false`
panel-host row** — in **External DNS** mode the panel is never an advertised
authoritative server and every row is a cluster member. (In *Local/On-Host*
mode the rows remain the panel host's own ns1/ns2, exactly as today; the table
replaces the fixed `server_settings` NS columns in both modes.)

Every current NS1/NS2 consumer becomes a query over the member set. The panel
still *compiles* apex NS + SOA MNAME + glue from this list (the records it
pushes to the cluster name the cluster members). Blast radius (spot-checked on
`1ee70b5e1`; line numbers approximate — re-confirm at implementation):

- `dnscompile/compile.go:71` (SOA MNAME) and `:90-95` (apex NS records)
- `dnscompile/bootstrap.go` (in-zone `ns*.<zone>` glue seeding)
- `reconciler.go:~3772` (glue A-record backfill for `ns*` labels)
- `reconciler/dns01_routing.go:98` (`OurNSHosts` hardcoded 2-slice)
- `settingsops/validate.go:52`, `config/config.go`, `serve.go` (fillIfEmpty seed)
- `api/server_settings.go:347-354` + the panel-ui Server Settings NS fields

Migration numbering trap applies when this ships (contiguous version, renumber
at merge — the completeness test enforces it).

### Phase 2 — pluggable zone-push backend

Abstract the reconciler's zone push behind a small backend interface with two
implementations: **local PowerDNS** (today's `dns.zone.upsert`, the default)
and **external cluster DNS API** (the new push-to-one client). Selected by the
DNS mode chosen at install (below). Desired-state compilation is unchanged; only
the sink differs. This is where the durable push queue + fail-closed behaviour
(Consequences) lives.

### Phase 3 — cluster deployable + cross-panel ownership + install modes

- **The Jabali DNS API service** (above): installed by the installer's
  **"Authoritative DNS Server Only"** mode — installs PowerDNS + the Jabali DNS
  API, generates/prints a per-panel API secret, functions as a dedicated
  external authoritative server for one or more panels.
- **Panel install DNS selection:** *Local/On-Host DNS* (today) vs *Jabali
  External DNS* — the latter takes the authoritative NS FQDNs + per-server API
  secret and installs the panel **without a local authoritative PowerDNS**.
- **Add-more-members from the panel:** admins register additional external DNS
  servers (`ns3.example.com` + API key) after deploy; optionally distributed to
  all panels via Jabali Sounder as **global DNS-server config** (this is server
  config, not zone data — see Open questions).
- **Zone-ownership invariant:** one zone, one owning panel, enforced in the
  Jabali DNS API's zone-owner table, fail-closed. Two panels pushing the same
  name must be refused, not last-write-wins (same failure class as the
  `aliasCollision` closed in #1625).
- **Cross-panel domain move (#954)** becomes an **ownership-transfer** operation
  on the zone-owner table (re-point the row to the new panel), not a re-pinning
  of `domains.master` on every node as the hidden-primary/AXFR shape required.
  This is cleaner under B and is where #954's live migration work meets this ADR.

### Phase 4 — on-host → external migration

For panels currently running local PowerDNS: an operator-initiated **"Migrate
to External DNS"** flow that copies all existing zones/records to the selected
external cluster, **verifies** the copy, then disables the local authoritative
PowerDNS and switches the panel's push backend to the cluster. The recursor is
untouched. Verify-before-disable is mandatory — a half-migrated zone must not
drop authoritative service.

## Alternatives considered

- **A — hidden-primary + AXFR fan-out.** The panel-local PowerDNS stays the
  authoritative primary but is not advertised; the cluster members are AXFR
  secondaries slaving from the panel. This was the earlier recommendation and is
  a **smaller step** from what ships today (reuses the AXFR path, adds no new
  service). **Rejected as the target** because the requester explicitly does
  **not** want the panel to remain an authoritative server or to require AXFR
  from panels; and a shared multi-panel cluster slaving from panels would need
  per-panel TSIG on panel→cluster AXFR anyway. Retained here only as the
  fallback if the central-API deployable proves too heavy for a first increment.
- **C — shared/replicated PowerDNS MySQL backend across all panels + cluster.**
  Rejected: multi-master DB coupling, split-brain, and it discards the
  panel-DB-is-truth model (ADR-0011). No isolation between panels.

## Open questions

- **Scope of "central management" (decides how far Phase 3/4 go).** johnnyq
  chose Variant B for *transport* (API push to a central authoritative tier)
  and described **per-panel** zone management ("multiple panels can manage
  **their own** zones through the same centralized infrastructure"), reserving
  "global" for **DNS-server configuration** via Sounder. It is **not** settled
  whether the goal also includes a **single cross-panel console to view/edit
  every zone across all panels** (the Plesk "Centralized DNS" surface). This
  ADR adopts per-panel ownership; a cross-panel management console would be an
  additional surface on top and is left open pending an explicit decision.
- **Credential model — per-panel vs per-server (decides the Phase 3 API shape).**
  This ADR's push-to-one model needs **one panel credential to one cluster
  endpoint**; johnnyq's install sketch describes **one API secret per NS server**
  ("ns1 + key, ns2 + key, ns3 + key"). Those are different write topologies —
  push-to-one with internal AXFR vs the panel writing each member. Confirm the
  intended shape with johnnyq before Phase 3; it also decides where the
  zone-owner table lives (one designated member, as above, vs every member).
- **PowerDNS API auth model** — confirm single-`api-key`/no-per-zone-ACL against
  the deployed PowerDNS version (the premise for the Jabali DNS API service).
- **Intra-cluster DNSSEC + replication** — confirm the primary→secondary AXFR
  inside the cluster carries RRSIGs correctly (`PRESIGNED` vs live-sign on the
  cluster primary) against a live pair; do not assume.

## Consequences

- **Write path is now a network dependency, including the time-critical ACME
  challenge.** Today a DNS write is local and always available. Under B, cert
  issuance depends on writing the `_acme-challenge` TXT to the cluster API and
  waiting for it to appear on ns1/ns2/ns3 before completing. Requires: a
  **durable panel-side push queue with retry** (an unreachable cluster API must
  not silently drop a record or a renewal), a **propagation-wait** in the
  DNS-01 flow (poll the TXT/SOA serial on each advertised member), and
  **fail-closed** behaviour on the cert path. DNS here is **not** an "optional
  component" that a tick may skip (contrast `feedback_per_tick_idempotent_loops`) —
  a failed push is a retriable error, never a silent success.
- **DNSSEC DS moves to the cluster** (above); ADR-0076 amended at Phase 3.
- **Cross-panel ownership** is the security crux — enforced in the Jabali DNS
  API, fail-closed, never delegated to PowerDNS's single-key API.
- **New auth surface:** the Jabali DNS API's per-panel secret is a new
  credential to issue, rotate, and revoke; treat it like the agent socket trust
  boundary, not a convenience token.
- **NOTIFY / re-push storm on settings save.** Editing the member list re-pushes
  every zone; expected, bounded by the push queue.
- **Registrar glue** for `ns1/ns2/ns3.domain.com` is an operator step, out of
  panel scope; documented in an updated runbook.
- Backwards compatible: with the DNS mode left *Local/On-Host*, the system
  behaves exactly as today; Variant B is opt-in per panel at install or via the
  Phase-4 migration.
