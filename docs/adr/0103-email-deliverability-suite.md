# ADR-0103: Email deliverability suite (queue/throttle/RBL/DMARC + MTA-STS/TLS-RPT)

**Status:** Proposed
**Date:** 2026-05-18
**Milestone:** M47

## Context

M6/M6.x shipped Stalwart + Bulwark with SPF/DKIM/DMARC, but the mail
queue is invisible, outbound abuse is undetected (a compromised account
blacklists the shared IP for every tenant), RBL listing is discovered
only via customer complaints, DMARC `rua` is write-only, and there is
no MTA-STS / TLS-RPT (a downgrade-attack + TLS-visibility gap that is
now a baseline expectation). This is the #1 hosting support-ticket
class.

## Decision

Build an operator+tenant deliverability surface **on top of** the
existing stack — not a new MTA:

- **Stalwart is the source of truth** for queue + delivery state, read
  via its admin API at the pinned **`http://127.0.0.1:8446`** (HTTP
  Basic, token from `stalwart-admin.token`; ADR-0045/0050 — the
  earlier `:8080` figure was a stale blueprint value). No new TCP
  loopback (M25 load-bearing). Wire contract pinned by a test against
  a live Stalwart (`feedback_verify_wire_contract`).
- **DB-as-truth (ADR-0002)** for policy: `mail_outbound_policy`,
  `mail_rbl_state`, `dmarc_aggregate`, `mta_sts_policy`,
  `tlsrpt_aggregate` in `jabali_panel`; reconciler/agent converge
  Stalwart config + DNS. Migration is schema-only; seeds are
  application-side (`feedback_migration_data_seed_ordering`).
- **Agent opens no outbound** (ADR-0001/0050): RBL lookups and
  DMARC/TLS-RPT report fetch run in panel-api.
- **Throttle is Stalwart-native** (its rate-limit/sieve), driven by the
  agent writing Stalwart config — not a jabali shim.
- **MTA-STS / TLS-RPT DNS** (`_mta-sts.<d>`, `mta-sts.<d>`,
  `_smtp._tls.<d>`) is emitted through the **existing M15 PDNS DNS
  reconciler** (DNSSEC-signed); the MTA-STS policy file is served by
  the existing nginx vhost stack at
  `https://mta-sts.<d>/.well-known/mta-sts.txt` with a cert from the
  existing per-domain LE path. No new DNS or web path.
- Abuse / RBL / STARTTLS-failure signals feed **M14** (ADR-0056) —
  reuse, don't rebuild.
- MTA-STS default `mode=testing`; promotion to `enforce` is
  operator-gated with a typed confirm + documented rollback
  (`mode=none`) — destructive-op discipline (M48 pattern).

## Consequences

- Operators see + control the queue, outbound abuse, RBL status, DMARC
  and TLS-RPT aggregates, and a per-domain deliverability score.
- Modern SMTP security (MTA-STS/TLS-RPT) without a new subsystem —
  reuses PDNS (M15), nginx, the LE path, and M14.
- Stalwart admin-API coupling is contained behind the agent + a
  contract test; the `:8446` pin is load-bearing and version-checked
  on a live Stalwart before Steps 1/4.
- Migration 000139 (re-checked next-free vs Gitea main, not the
  mirror — the collision scar).

## Advisor amendments (2026-05-18)

- **Stalwart wire surface is a Wave-1 gate, not assumed.** The
  `:8446` figure in this ADR is a candidate, not verified — Stalwart
  0.16.0 returns 404 to unauthenticated mgmt requests (anti-
  enumeration). Before Step 1, the M6 install code is read to find
  the `stalwart-admin.token` path + the listener carrying
  `protocol="http"`, then the surface is live-confirmed and this ADR
  is amended with a concrete **version + port + endpoint** table.
  Endpoint shape (Context7, 0.15; verify 0.16 prefix): `GET
  /queue/messages` → `{data:{items,total,status}}`, `POST
  /queue/retry`, DELETE cancel.
- **MTA-STS (Step 7) splits into 7a (per-domain DNS+vhost+LE-cert
  scaffolding — an LE-rate-limit growth axis) and 7b (policy state
  machine + promotion gating).** Rollback: `mode=none` is reversible
  but bounded by the cached `max_age` (RFC 8461 §5.1); default
  `max_age` starts small (86400).
- **Retention:** `dmarc_aggregate`/`tlsrpt_aggregate` default 90-day,
  pruned app-side via a timer (Steps 6/8); migration 000139 notes it.
- **RBL set is fixed at blueprint time** (Spamhaus ZEN, SpamCop,
  Barracuda BRBL, SURBL; paid via operator-configured creds only;
  cache ≥1 h, poll 4–6 h). **PTR is informational only** (provider-
  controlled; show current vs expected, no "fix" action).

## Stalwart 0.16 wire surface — PINNED (2026-05-18, gate resolved)

The advisor-flagged hard blocker is resolved by introspecting the
installed `stalwart-cli` (ADR-0045: "stalwart-cli is the v0.16
management surface"). Stalwart 0.16 has **no `/api/queue/messages`
HTTP route and no `queue` cli subcommand** (those were 0.15; live
mx returns 404 for the path, `unrecognized subcommand` for the cli).
0.16 exposes a **generic typed-object API**; jabali's agent shells
`stalwart-cli` exactly as it shells `cscli` for crowdsec — NOT a
hand-rolled HTTP client (no contract to drift).

**Pinned contract (verified on mx Stalwart 0.16.0):**

- Object type: **`QueuedMessage`**. Fields: `recipients`
  (`map<emailAddress, QueuedRecipient>` — envelope recipients +
  per-recipient delivery status), `returnPath` (MAIL FROM), `size`,
  `nextRetry` (datetime), `priority`, `receivedFromIp`,
  `receivedViaPort` (+ server `id`). Sibling types: `MtaStageMail`,
  `MtaVirtualQueue`, `MtaQueueQuota`.
- Verbs (Wave 1):
  - list  → `stalwart-cli query QueuedMessage --json [--where f=v]`
  - cancel→ `stalwart-cli delete QueuedMessage <id>`
  - retry → `stalwart-cli update QueuedMessage <id> nextRetry=<RFC3339-now>`
- Auth: env `STALWART_URL` (jabali: `http://127.0.0.1:8446`),
  `STALWART_USER=admin`, `STALWART_PASSWORD` = contents of
  `/etc/jabali-panel/stalwart-admin.token` (0640 jabali:jabali-mail;
  same credential mailbox_jmap.go uses; Basic, verified via
  `/jmap/session` 200).
- `query` supports `--where field=value|>=|<=|>|<`, `--fields`,
  `--json` — drives Wave 2 pagination/per-domain scoping.

The agent's `mail.queue.*` handlers wrap these cli calls + a contract
test pins the `QueuedMessage` field KINDS
(`feedback_schema_enumerate_kinds_not_names`). Supersedes the
`/api/queue/messages` figure in §Decision (0.15-only, never on 0.16).

## Wave 3 Stalwart pin (2026-05-18) — outbound throttle

Per-user/domain outbound throttle maps to Stalwart 0.16 object
**`MtaOutboundThrottle`** (verified via `stalwart-cli describe`):
`enable` bool, `key` `set<MtaOutboundThrottleKey>`, `match`
`object<Expression>`, `rate` `object<Rate>`, `description`. Agent
`mail.throttle.apply` will `stalwart-cli create|update
MtaOutboundThrottle` (env Basic-auth as Wave 1).

**Still to pin before Wave-3 agent code (verify-wire-contract gate):**
`MtaOutboundThrottleKey` enum values (decides sender vs
recipient-domain scoping — security-load-bearing: wrong key = no
protection OR locks out all senders), the `Rate` object shape
(requests/period), and whether scope is best expressed via `key` vs
a `match` Expression. Pin all three via `stalwart-cli describe`
before writing the apply; do NOT guess (the queue-gate lesson).
Wave-3 DB half (`mail_outbound_policy` repo + /admin/mail/throttle
CRUD + reconciler loop) is Stalwart-independent and can land first.

## Amendment 2026-09-29 — panel-api writes the throttles, over JMAP (GH #1936)

The Wave 3 pin above planned an agent verb, `mail.throttle.apply`. What
shipped differs: panel-api writes the throttles itself
(`stalwartadmin.Throttles`), and there is no `mail.throttle.*` verb.

- **Credential.** The panel signs in as `admin` with
  `/etc/jabali-panel/stalwart-admin.token` (0640 jabali:jabali-mail), read
  on every call. ADR-0142 names this token the panel's management
  credential, and mailscan already uses it. The panel never had the
  recovery-admin line in `stalwart.env` (0640 root:jabali-mail); reading
  that file is why the throttle reconciler never ran before this change.
- **Why not an agent verb.** The panel holds the admin token either way,
  so a root verb would add a privileged surface without taking any
  authority from the panel. The operator chose the panel path. This is
  not root work in the sense of ADR-0001 (the agent runs as root, the
  API as an unprivileged user): it is an HTTP call to a loopback service
  with a credential the panel user owns, as mailscan already makes.
- **Transport.** JMAP on `127.0.0.1:8446`: `x:MtaOutboundThrottle/query`,
  `/get` and `/set`, then `x:Action/set` with `ReloadSettings`. The
  panel's AppArmor profile does not allow it to exec `stalwart-cli`, and
  the profile is unchanged.
- **Pinned on the .60 test box.** A missing id comes back as `notFound`
  on get and as `notUpdated`/`notDestroyed` of type `notFound` on set. A
  bad payload comes back as `notCreated` of type `invalidPatch`. A wrong
  token is HTTP 401.
- **Pinned object.** `key` is `{"sender": true}` (user scope),
  `{"senderDomain": true}` (domain scope) or `{}` (global). A scoped row
  fires only when `sender == '<address>'` or
  `sender_domain == '<domain>'`. The sender or domain is validated before
  it goes into that expression. `rate` is `{count, period}`, with the
  period in milliseconds. The hourly and daily caps are separate objects.
- **Ownership.** Every panel-made throttle's description starts with
  `jabali `. The reconciler removes such a throttle when no row references
  it. Throttles with any other description are left alone.
