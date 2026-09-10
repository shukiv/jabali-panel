# ADR-0168: Dedicated authoritative DNS cluster (hidden primary + AXFR fan-out)

**Status:** Proposed (2026-09-10) — design spike, no code. Each phase below is gated on separate approval.
**Driven by:** GH #1632.
**Related:** ADR-0011 (PowerDNS + MySQL backend), ADR-0076 (per-domain DNSSEC), ADR-0107 (operator edits authoritative), ADR-0150 (tenant record-type permissions). Runbook: `docs/runbooks/dns-secondary-nameserver.md`.

## Context

#1632 asks for authoritative DNS to be served by a **dedicated cluster**
(`ns1/ns2/ns3.domain.com`) that several Jabali panels feed, instead of each
panel being its own authoritative nameserver at `ns1.<panel-host>`.

What already exists today (so the ADR does not re-propose it):

- **NS names are already free-form.** `server_settings.NS1Name/NS2Name` are
  admin-editable varchars (`api/server_settings.go:347-354`, validated in
  `settingsops/validate.go:52`). An admin can set `ns1.domain.com` /
  `ns2.domain.com` right now; the apex NS + SOA MNAME are built from those
  values verbatim (`dnscompile/compile.go:71,90-95`).
- **A single AXFR secondary is already supported.** The reconciler derives
  `ALLOW-AXFR-FROM` / `ALSO-NOTIFY` from **`NS2IPv4` only**
  (`reconciler.go:~2570`); the agent writes those as `domainmetadata` on each
  `dns.zone.upsert`; ns2 slaves via NOTIFY→AXFR (runbook Option A/B).
- Panel DB is the source of truth; the reconciler pushes each zone to the
  panel-local PowerDNS.

So the genuinely new work is **not** NS naming. It is (a) letting the panel's
own PowerDNS be a *hidden* primary that is **not** in the delegation, (b)
fanning AXFR/NOTIFY out to **N** cluster members rather than one ns2, and (c)
the cross-panel trust/ownership model that lets one cluster serve many panels.

## Industry standard (how other control panels do this)

Every mainstream panel converges on the same shape — **dedicated DNS-only
nodes that receive role-based zone pushes, with a cross-server uniqueness
guard, and AXFR as the DNSSEC-capable transport**:

- **cPanel/WHM** — a *DNS cluster* of dedicated **cPanel DNSOnly** nodes
  (free, DNS-only image). Web servers **push complete zone data over the WHM
  API** (a `dns-cluster` ACL token), not by being an AXFR primary. Per-peer
  **roles**: *Standalone* (receive only), *Synchronize* (two-way, SOA-serial
  wins), *Write-only* (one-way push, no checks). A **reverse-trust**
  relationship stops one web server from creating a zone another already owns
  (their cross-panel hijack guard). No master; conflicts by SOA serial. Three
  nodes is the common production shape.
- **HestiaCP** (closest to Jabali — PowerDNS/BIND) — a *DNS cluster* over the
  Hestia API (`v-add-remote-dns-host` / `v-sync-dns-cluster`) with a dedicated
  `dns-cluster` sync user. Two modes: **Master↔Master** (API push;
  **DNSSEC unsupported**) and **Master→Slave** (real BIND `allow-transfer` +
  `also-notify` **AXFR**; **DNSSEC supported**) — directly corroborating the
  DNSSEC×AXFR constraint below.
- **DirectAdmin** — *Multi-Server Setup* pushes zones A→B on save (*Zone
  Transfer*) with a remote-uniqueness *Domain Check* guard and a task-queue
  retry.
- **Plesk** — *Slave DNS Manager* pushes via `rndc addzone` to a stock-BIND
  secondary that then pulls the zone by **AXFR**. Notably, Plesk **refuses to
  let several masters share one secondary set** for security (a shared control
  channel lets any master delete any master's zones) — the exact cross-panel
  trust problem Phase 3 must solve.

**Where Jabali already sits:** the agent's `dns.zone.upsert` (panel pushes
zone data; secondary pulls via AXFR) is *already* the industry pattern for a
single panel + one secondary. The gap to the cPanel/Hestia cluster model is
exactly (a) a dedicated DNS-only node role, (b) N nodes not one, and (c) the
reverse-trust / zone-ownership guard for multiple panels — i.e. the three
items above, not a new transport.

## Decision

Adopt a **hidden-primary + AXFR fan-out** architecture, built in phases:

- The panel-local PowerDNS remains the authoritative primary for the zones of
  domains hosted on that panel, but is marked **not advertised** — it never
  appears in the apex NS set, in-zone glue, or DNS-01 "our NS" routing.
- The advertised nameservers are the dedicated cluster members
  (`ns1/ns2/ns3.domain.com`). Each is an AXFR secondary that slaves the zone
  from the owning panel's hidden primary (NOTIFY-driven, superslave per the
  runbook).
- One domain is owned by exactly **one** panel's hidden primary at a time
  (the zone-ownership invariant, below).

This **is** the cPanel/Hestia DNS-cluster model expressed in Jabali's existing
mechanics: a dedicated DNS-only node role (the advertised members), role-based
distribution, and a reverse-trust guard — with **AXFR as the transport**
because it is what Jabali already ships and, unlike Master↔Master API push, it
keeps DNSSEC (ADR-0076) working (Hestia reaches the same conclusion). It
reuses the shipped AXFR/NOTIFY path — the delta is a data-model change and
generalizing single-`ns2` to a list — and introduces **no** new network
service. A central "Jabali DNS API" push plane (the issue's literal diagram,
and how cPanel/Hestia move the zone *data*) is Alternative B, layered on top
only if central cross-panel management is wanted.

### Phase 1 — N-nameserver + hidden-primary data model

**This is the real Phase 1** (NS-name decoupling is already done). Introduce a
dedicated `dns_nameservers` table — **not** more `server_settings` columns
(`server_settings` is at the VARCHAR row-size ceiling; new NS3Name/NS3IPv4
varchars are exactly how `ERROR 1118` gets tripped — see
`feedback_server_settings_row_ceiling`). Columns: `name`, `ipv4`, **`ipv6`**
(there is no AAAA glue at all today — add it from the start), `advertised`
(bool; the panel-host hidden-primary row is `advertised=false`), `sort_order`.

Every current NS1/NS2 consumer becomes a query over the advertised set. That
list **is** the Phase-1 blast radius:

- `dnscompile/compile.go:71` (SOA MNAME) and `:90-95` (apex NS records)
- `dnscompile/bootstrap.go:121-131` (in-zone `ns*.<zone>` glue seeding)
- `reconciler.go:3681-3694` (glue A-record backfill for `ns*` labels)
- `reconciler.go:~2570` (`ALLOW-AXFR-FROM` / `ALSO-NOTIFY` — the single→list change)
- `reconciler/dns01_routing.go:98` (`OurNSHosts` hardcoded 2-slice)
- `settingsops/validate.go:52`, `config/config.go:90-92`, `serve.go:733` (fillIfEmpty seed)
- `api/server_settings.go:347-354` + the panel-ui Server Settings NS fields

Migration numbering trap applies when this ships (contiguous version, renumber
at merge — the completeness test enforces it).

### Phase 2 — AXFR fan-out + TSIG

Generalize `ALLOW-AXFR-FROM` / `ALSO-NOTIFY` to every advertised member's IP.
Add **TSIG**: today AXFR trust is source-IP allowlist only — acceptable for a
single trusted ns2 on the same operator's network, **not** acceptable for a
shared multi-panel cluster. Phase 2 gates multi-panel on writing a per-panel
TSIG key into `TSIG-ALLOW-AXFR` domainmetadata (the agent's `dns.zone.upsert`
writes only ALLOW-AXFR-FROM/ALSO-NOTIFY now — this is a new metadata write).

**DNSSEC × AXFR constraint to confirm before this phase:** a live-signing
primary (ADR-0076, `pdnsutil` per-domain keys) requires secondaries to carry
`PRESIGNED` metadata to serve transferred RRSIGs, and the runbook's
`launch=bind` superslave likely cannot — cluster nodes should be gmysql-backed.
The registrar DS record still derives from the hidden primary's keys. Confirm
against a live pair; do not assume.

### Phase 3 — cross-panel ownership + migration

- **Zone-ownership invariant:** one domain, one owning panel. Confirm and
  document PowerDNS behaviour: a superslave pins `domains.master` at
  auto-create and ignores NOTIFY for an existing zone from a different source
  IP. If that is **not** true for the chosen backend, two panels pushing the
  same name is a last-NOTIFY-wins cross-panel hijack (same failure class as the
  `aliasCollision` closed in #1625). Verify before enabling multi-panel.
- **Moving a domain between panels** (the #954 Jabali→Jabali migration path)
  requires re-pinning `domains.master` on every cluster node — a runbook step,
  not an automatic transfer.

### Phase 4 (optional) — central Jabali DNS API (Alternative B)

Only if central cross-panel visibility/management is required. Layers on
Phases 1-3; see Alternatives.

## Alternatives considered

- **B — API-push to a central DNS control plane.** Panels POST zone changes to
  a Jabali DNS API that owns the cluster backend and replicates. This is how
  cPanel/Hestia move the zone *data* (WHM API / Hestia API push), so it is
  mainstream, not exotic — and it matches the issue's literal diagram plus
  gives true central management. Not the *starting* point: it is a new
  deployable with a new auth surface and a cross-panel conflict model, it
  breaks DNSSEC in the bidirectional (Master↔Master) form (Hestia), and the
  panel already exposes zone data (`GET /dns/zones`, the record API) — Option A
  reaches the same advertised-NS-set outcome, keeps DNSSEC, and adds no
  service. Kept as Phase 4 if central cross-panel management is the goal.
  **Open question for #1632 (scope-defining): is the target a shared *advertised
  NS set* where each panel still owns its own zones (Option A / cPanel
  Synchronize model), or *central cross-panel management* — one place to
  see/edit every zone across panels (Option B / Plesk Multi Server "Centralized
  DNS")? That is the fork that decides whether Phase 4 is ever built.**
- **C — shared/replicated PowerDNS MySQL backend across all panels + cluster.**
  Rejected: multi-master DB coupling, split-brain, and it discards the
  panel-DB-is-truth model (ADR-0011). No isolation between panels.

## Consequences

- **Propagation lag on DNS-01.** Today ns1 is panel-local: a challenge TXT is
  served the instant it is written. With a hidden primary, the public NS lags
  by NOTIFY→AXFR. `OurNSHosts` still marks the zone "ours", but the CA may
  query a cluster member before the TXT has transferred. Mitigation: gate
  validation on polling the SOA serial on each advertised NS, or rely on the
  existing certbot retry (DNS-01 first-try timeout → retry converges, per
  `feedback_san_drift...`). Phase 2 must name which.
- **NOTIFY storm on settings save.** `desiredDNSZoneHash(compiled, allowAXFR,
  alsoNotify)` means editing the NS list re-pushes every zone; with N members
  that is N×zones NOTIFYs per save. Acceptable, but expected.
- **Security:** Phase 1 changes no trust boundary. Multi-panel (Phase 3) is
  **blocked** on TSIG (Phase 2) — IP-allowlist AXFR trust does not extend to a
  shared cluster.
- **Registrar glue** for `ns1/ns2/ns3.domain.com` is an operator step, out of
  panel scope; documented in an updated runbook.
- Backwards compatible: with a single `advertised` row = the panel host, the
  system behaves exactly as today.
