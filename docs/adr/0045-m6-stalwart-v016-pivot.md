# ADR-0045 — M6: Pivot to Stalwart v0.16 management model

Status: **ACCEPTED** (supersedes parts of ADR-0041, amends ADR-0042)
Date: 2026-04-21
Milestone: M6 — Email via Stalwart

## Context

The M6 blueprint (plans/m6-email-stalwart.md, first drafted 2026-04-20) was
designed around Stalwart v0.15's architecture:

- **Config surface**: flat TOML files under `/etc/stalwart/config.toml`
  rendered by `install.sh` with `sed` substitution.
- **Management surface**: REST endpoints under `/api/principal/...` for
  cache invalidation + quota reads.
- **SQL directory**: configured via `[directory.<name>]` TOML block with
  keys like `query.name`, `query.auth`, `query.domains`, `query.emails`.
- **DKIM**: panel generates keys, pushes private material to Stalwart via
  a file path in TOML, pushes TXT record to PowerDNS.

Stalwart v0.16.0 shipped on 2026-04-20 (one day before we started M6
implementation). It is explicitly marked as "multiple breaking changes"
and the upstream `UPGRADING/v0_16.md` describes the shape of the break:

> In v0.16 there is a single small `config.json` on disk that describes
> only the datastore. Every other configuration and management setting
> is now stored inside that datastore as a JMAP object. [...] The
> `/api/...` endpoints from previous releases no longer exist. All
> management operations happen through JMAP objects reachable at `/jmap`.

Concretely:

1. **TOML is gone.** Only `config.json` (datastore wiring) on disk; every
   other setting is a JMAP object in the datastore, provisioned via
   `stalwart-cli apply --file <plan.json>`.
2. **REST `/api/*` is gone.** JMAP on `/jmap` is the management surface.
3. **`SqlDirectory` schema is still there** but expressed as a camelCase
   JMAP object (`queryLogin`, `queryRecipient`, `columnEmail`, ...) — same
   shape our ADR-0042 schema matches, just a different on-disk format.
4. **No more TTL cache on SQL directory lookups.** Verified by reading
   `crates/common/src/cache/directory.rs::synchronize_account` and
   `crates/common/src/auth/authentication.rs::build_directory_token`:
   every successful authentication re-runs `synchronize_account`, which
   upserts the external directory row into Stalwart's registry. There is
   no intermediate cache with a staleness window. Password/quota changes
   in the source-of-truth table propagate on the next auth.
   **Correction (2026-09-29):** the upsert only adds aliases; it never
   removes one, and the registry is consulted before the SQL directory.
   A moved or deleted alias stayed on its old account. See the ADR-0073
   amendment for what the panel does about it.
5. **DKIM** now a JMAP object (`DkimSignature`) that accepts externally
   generated `privateKey` — panel-side keygen + PowerDNS publishing still
   works, we just additionally push the `DkimSignature` JMAP object so
   Stalwart signs outgoing mail with our key.

## Decision

Pivot M6 to the v0.16 model rather than pinning to v0.15.x.

**Rejected alternatives**:

1. **Pin to v0.15.x and plan a later v0.16 migration milestone.** Would
   let us ship the original plan as-written; cost is building M6 on the
   deprecated architecture two days after v0.16 released. Would violate
   `feedback_default_to_latest_stable` (2026-04-21: Kratos + Hydra bumped
   explicitly to reverse this bias). Also: Stalwart v0.16 ships the
   auto-DNS + auto-DKIM features that would make a future migration more
   invasive, since we'd have settings in both places.
2. **Build against v0.16 but keep a TOML layer on top.** There isn't one
   to keep against — v0.15's TOML parser is gone from the v0.16 binary.
3. **Write a shim agent that emulates v0.15's REST `/api/*` against
   v0.16's JMAP surface.** Rejected: shims against a moving upstream are
   a debt trap, and we have no other consumer — the panel-agent is the
   sole Stalwart management client.

## Consequences

### Bootstrap flow

`install.sh::install_stalwart` writes three files on every install run
(idempotent — re-runs rewrite from current inputs):

- `/etc/stalwart/config.json` — **the registry store** (singular).
  v0.16's `config.json` is a single tagged-enum `DataStore` descriptor
  with `@type: RocksDb` / `MySql` / `Sqlite` / `PostgreSql` /
  `FoundationDb` at the top level — NOT a map of named stores plus a
  `storage` routing block like v0.15. It points at where the
  registry itself lives; mail/blob/fts storage and the SQL directory
  are JMAP objects inside that registry, applied via `stalwart-cli`.
  Template is therefore static (no mustaches) — the MariaDB password
  is only substituted into `stalwart-apply-plan.json` (which contains
  the `x:Directory` SqlAuthStore MySQL connection).
- `/etc/jabali-panel/stalwart-apply-plan.json` — declarative JMAP
  object plan (SqlDirectory, NetworkListeners for 25/465/587/993 +
  loopback JMAP, Authentication.directoryId pointer) rendered from
  `install/stalwart/apply-plan.json.tmpl`.
- `/etc/jabali-panel/stalwart.env` — contains
  `STALWART_RECOVERY_ADMIN=admin:<token>` where `<token>` is the
  contents of `/etc/jabali-panel/stalwart-admin.token`. Paired so
  Stalwart accepts Basic-auth calls from the panel-agent against /jmap.

Then:

1. `systemctl enable --now jabali-stalwart` (idempotent — no-op if
   already enabled and active).
2. Poll `http://127.0.0.1:8446/jmap/session` until a non-5xx HTTP
   status (401 counts as "ready": the HTTP layer is up and refusing
   our deliberately-unauthenticated GET). Typical ≤ 10s on a healthy
   host; install aborts after 30s with a `journalctl` hint.
3. Run `stalwart-cli apply --file /etc/jabali-panel/stalwart-apply-plan.json`
   with `STALWART_USER=admin STALWART_PASSWORD=<token>`. Plan seeds
   SqlDirectory + listeners + Authentication pointer.
4. `STALWART_RECOVERY_ADMIN` stays in stalwart.env permanently — it's
   the only admin credential the panel-agent uses. (Upstream docs call
   it "temporary" in the migration narrative, but explicitly allow
   permanent use when no WebUI-created admin exists. For our automation
   path, the env-seeded admin IS the production credential.)

### Two-phase install ordering (amendment, 2026-04-21)

The steps above are split across **two functions** in install.sh for
ordering reasons — the SqlDirectory targets need to exist before the
GRANT can succeed.

- `install_stalwart` (phase 1, called in `main()` before
  `start_and_verify`): downloads binaries + CLI, creates service user
  and paths, generates admin token + MariaDB password, renders
  config.json / stalwart.env / apply-plan.json, installs systemd unit.
  Does NOT run GRANT or start Stalwart.
- `install_stalwart_apply` (phase 2, called in `main()` after
  `start_and_verify`): runs the MariaDB `CREATE USER ... GRANT SELECT
  ON jabali_panel.mailboxes, jabali_panel.domains`, then
  `systemctl enable --now jabali-stalwart`, then polls /jmap, then
  `stalwart-cli apply`.

The GRANT cannot live in phase 1 because `jabali_panel.mailboxes` is
created by migration `000054_create_mailboxes.up.sql`, which runs
inside `jabali-panel.service` on first start — which happens inside
`start_and_verify`. Running the GRANT before that point fails with
`ERROR 1146 (42S02): Table 'jabali_panel.mailboxes' doesn't exist`,
aborting install on a fresh VM.

Splitting also keeps the artefacts rendered in phase 1 available for
manual recovery: if phase 2 fails (e.g. stalwart.service crash-loops),
an operator can fix the underlying cause and re-run
`install_stalwart_apply` (or `stalwart-cli apply` by hand) without
re-downloading or re-rendering anything.

The recovery-mode foreground dance (upgrade doc §5–7) is NOT used —
it's required only for migrating an existing v0.15 deployment. Fresh
installs use normal mode with seeded admin, which is simpler and has
no state-transition cliffs.

**Note on ExecStartPre.** `ExecStartPre=stalwart-cli apply` is NOT
wired into the systemd unit, even though that would match the
reconciler pattern we use elsewhere (nginx reload on vhost write, PHP
pool regen on phpext install). ExecStartPre fires before ExecStart
binds /jmap, so the CLI would always hit a dead port and fail the
unit on every boot. Reconciliation lives in install.sh (first apply)
and will move to the panel-api reconciler (plan-file-change trigger)
when domain.email_enable grows beyond one-shot semantics.

### Reconciliation

After first install, plan changes (jabali update, panel reconciler
reacting to a new domain) call `stalwart-cli apply` against the already-
running Stalwart (real admin creds from `stalwart-admin.token`). This
matches the existing reconciler pattern (nginx reload on vhost write,
PHP pool regen, DNS zone sync).

`ExecStartPre=stalwart-cli apply` is NOT used: ExecStartPre fires before
`/jmap` is bound, so the CLI call would always fail on boot. The
reconciler lives in panel-api, not in the unit file.

### Agent-side command surface (amends Step 3 of the plan)

Because v0.16's SqlDirectory syncs on every auth (no cache window), the
cache-invalidation commands collapse to no-ops on the Stalwart side:

- `mailbox.create` — panel writes to `jabali_panel.mailboxes`. Agent
  returns `{ok: true}` without calling Stalwart. Stalwart picks up the
  row on the mailbox owner's first auth.
- `mailbox.set_quota` / `mailbox.set_password` — same shape. Panel DB
  write is authoritative; agent is a no-op wrt Stalwart; registry sync
  on next auth.
- `mailbox.usage` — JMAP `Account/get` by the account email (resolved
  to the registry's internal id via `Account/query`). Returns
  `quotaUsed` + `messageCount` + `lastAuthenticatedAt`.
- `mailbox.delete` — asymmetric. Panel deletes the row, but the
  registry record survives. Agent calls JMAP `Account/set` destroy to
  clean up the registry.
- `domain.email_enable` — agent generates the DKIM key (ADR-0043),
  JMAP `Domain/set` create + JMAP `DkimSignature/set` create with our
  `privateKey`, `systemctl enable --now`.
- `domain.email_disable` — agent JMAP `Domain/set` destroy, removes
  the keyfile, `systemctl reload` (doesn't stop the unit — other
  domains may still be active).

Wire contracts (the JSON shape on the panel ↔ agent socket) are
unchanged; only the agent's internal impl pivots from REST to JMAP.
Golden fixtures in `panel-api/internal/agent/testdata/` survive.

### DKIM

ADR-0043 stands: panel owns DKIM generation, PowerDNS owns DNS.
Addition for v0.16: the panel also pushes a `DkimSignature` JMAP
object with the `privateKey` field so Stalwart signs outgoing mail
with the key whose public half lives in our zone. Stalwart's own
DKIM auto-rotation feature (new in v0.16) is disabled via the
management plan — the "automatic DNS management" it implies would
race PowerDNS.

### Directory ownership

`jabali_panel.mailboxes` remains the source of truth for mailbox
metadata (ADR-0042). The registry is a synchronized cache updated by
Stalwart on auth. The invariant is one-way: panel writes → registry
reads-through-sync. The panel does NOT read from the registry (that
would create two views of truth).

### Files / touchpoints

- **Delete**: `install/stalwart/config.toml.tmpl`.
- **New**: `install/stalwart/config.json.tmpl`, `install/stalwart/apply-plan.json.tmpl`.
- **Rewrite**: `install.sh::install_stalwart`, `install.sh::install_stalwart_cli` (new),
  `install/systemd/jabali-stalwart.service` (config path change).
- **Rewrite**: `panel-agent/internal/commands/mailbox_jmap.go`, all
  `mailbox_*.go` command handlers, `domain_email_enable.go`, `domain_email_disable.go`.
- **Unchanged**: migration 000054 (schema is directory-shape-agnostic),
  `internal/dkim` (still generates the material we hand to Stalwart),
  `internal/mailaddr` (address validation is independent),
  panel-api wire contracts + golden fixtures.

## Validation

At install time, we cannot validate the apply-plan offline:
`stalwart-cli apply --dry-run` still fetches schema from the server URL
(verified by reading `cli/src/app/context.rs`). The CLI caches schema by
URL hash locally but always fetches once against a live server.

Validation is therefore deferred to the first real `apply` call, which
runs against the freshly-started Stalwart in bootstrap-admin mode. Plan
errors surface in `journalctl -u jabali-stalwart` + the install.sh exit
code, not at file-write time. Accept this: it's the same failure mode
operators already expect from other start-up reconcile steps.

## Schema-pull procedure

Stalwart publishes the full JMAP schema as a checked-in artifact at
`resources/schema/schema.json.gz` in the upstream `stalwartlabs/stalwart`
repo. To re-verify field names before writing apply-plan / JMAP payloads:

```bash
curl -fsSL https://raw.githubusercontent.com/stalwartlabs/stalwart/main/resources/schema/schema.json.gz \
  | gunzip > /tmp/stalwart-schema.json
# URL-safe base64 of the .gz sha256 should match resources/schema/schema.json.sha256
python3 -c "import hashlib, base64, sys; b=sys.stdin.buffer.read(); print(base64.urlsafe_b64encode(hashlib.sha256(b).digest()).rstrip(b'=').decode())" \
  < <(curl -fsSL https://raw.githubusercontent.com/stalwartlabs/stalwart/main/resources/schema/schema.json.gz)
# Grep properties, variants, enums:
jq '.fields["x:Domain"].properties | keys' /tmp/stalwart-schema.json
jq '.schemas["x:DkimSignature"]' /tmp/stalwart-schema.json
jq '.enums.NetworkListenerProtocol' /tmp/stalwart-schema.json
```

The 2026-04-21 schema pull (URL-safe b64 sha256
`aJJKvnpsjjwAEzJ0eKDWdfSLK_RvBKsLhk8BwCq-7qA`) informed:

- JMAP method names are namespaced `x:`: `x:Account/query`, `x:Domain/set`.
- `x:Account` variant tags are `User` / `Group`.
- `x:DkimSignature` variant tags are `Dkim1Ed25519Sha256` / `Dkim1RsaSha256`.
- `x:Directory` variant tags are `Ldap` / `Sql` / `Oidc`.
- `x:UserAccount.emailAddress` is `serverSet` (derived from name + '@' + domain).
- `x:UserAccount.usedDiskQuota` is the sole per-account usage property.
- `messageCount` / `lastAuthenticatedAt` / `lastUsedAt` do not exist in the
  v0.16 account schema — mailbox.usage zero-pins these fields.
- `NetworkListenerProtocol` enum values are lower-case: `smtp`, `lmtp`,
  `http`, `imap`, `pop3`, `manageSieve`. (JMAP is served over `http`.)
- `DkimManagementType` / `DnsManagementType` enums are `Manual` / `Automatic`.
- `x:Dkim1Signature.privateKey` is `x:SecretText` (variants `Text`,
  `EnvironmentVariable`, `File`); the `Text` variant's `secret` field
  is a string and v0.16 parses it as PEM-wrapped PKCS#8 DER for Ed25519
  (source: `crates/common/src/config/smtp/auth.rs` —
  `Ed25519Key::from_pkcs8_maybe_unchecked_der`).

## References

- Upstream upgrade guide: `UPGRADING/v0_16.md` at github.com/stalwartlabs/stalwart
- `stalwart-cli` README: github.com/stalwartlabs/cli
- Schema artifact: `resources/schema/schema.json.gz` in the same repo.
- `SqlDirectory` schema: `crates/registry/src/schema/structs.rs` (pinned
  to commit referenced in `install/stalwart.sha256` once Step 1 is re-tagged)
- Amends: ADR-0041 (storage), ADR-0042 (SQL directory)
- Supersedes: parts of ADR-0041 that describe TOML-based config
