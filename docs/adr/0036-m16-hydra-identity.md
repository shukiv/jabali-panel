# ADR-0036: M16 — Ory Hydra as self-hosted OAuth 2 / OIDC provider

**Date**: 2026-04-20 (original); amended 2026-04-20 to record MariaDB→SQLite pivot for Hydra's state
**Status**: superseded by ADR-0038 (2026-04-21) — M16 rolled back due to PKCE incompatibility with WordPress plugin; replaced by M22 magic-link
**Deciders**: shuki + Claude
**Related**: ADR-0034 (M20 Kratos identity), ADR-0033 (applications framework), M7/ADR-0021 (phpMyAdmin SSO — explicitly *not* migrated), [ory/hydra#3387](https://github.com/ory/hydra/issues/3387)

## Amendment 2026-04-20 — Hydra persistence moved from MariaDB to SQLite

The original decision put Hydra's state in a MariaDB schema `jabali_hydra` for parity with Kratos. First-host validation on a clean Debian 13 VM surfaced that this path doesn't actually work: Hydra's migration `20220513000001000001_string_slice_json.mysql.up.sql` uses `CAST(... AS JSON)`, which MariaDB 11.8.6 rejects as a syntax error. The MariaDB `.mysql.up.sql` migration has shipped broken since 2022 (see [ory/hydra#3387](https://github.com/ory/hydra/issues/3387)); Ory closed the issue as "MariaDB is not officially supported." Verified via GitHub API: v26.2.0 (latest as of 2026-03-20) has the byte-identical broken migration, so version-bumping Hydra doesn't fix it.

Four workaround options were weighed (full notes in conversation log and `plans/m16-hydra-oauth.md` design discussion):

1. **Version bump** — dead end, upstream hasn't fixed in 4 years.
2. **Switch to Postgres** — works but adds a new daemon, new backup ritual, new permissions surface. Overweight for single-operator panel.
3. **MariaDB + manual SQL workaround** (the `JSON_VALUE(json_object('tmp',...),'$.tmp')` pattern from issue #3387) — works today on MariaDB 11 + Hydra v25.4.0 per community validation, but accumulates perpetual maintenance: every Hydra upgrade may add a new MariaDB-hostile migration and require extending the workaround.
4. **SQLite** — Hydra's `.sqlite.up.sql` migrations are clean and upstream-tested. One file under systemd's `StateDirectory=`. Zero new daemons.

We picked SQLite. Key insight: Hydra's state for our use case is nearly ephemeral. Access/refresh tokens are short-TTL and naturally recycle; consent sessions auto-accept for trusted first-party clients and re-prompt is harmless for anyone else; auth codes and login flow state are measured in minutes. The one category of permanent state — per-install OIDC clients — is already duplicated in `application_installs.oidc_client_id + oidc_client_secret_enc` on the panel DB side. Losing Hydra's SQLite is recoverable: users re-login once, WP plugins re-fetch JWKS, and panel-side data has the material to re-register every client if needed.

**What changed in the code:**

- `install/hydra.yml.tmpl`: DSN line from `mysql://{{.HydraDatabaseUser}}:...@tcp(127.0.0.1:3306)/{{.HydraDatabaseName}}?...` → `sqlite:///var/lib/jabali-hydra/db.sqlite3?_fk=true`. The three `HydraDatabase*` mustache slots are gone.
- `install.sh install_hydra()`: dropped the `jabali_hydra` MariaDB schema + user + password-file provisioning block. Added `install -d -o jabali -g jabali -m 0750 /var/lib/jabali-hydra` so `hydra migrate sql` has a writable parent before the systemd unit first starts.
- `install/systemd/jabali-hydra.service`: removed `After=mariadb.service`, added `StateDirectory=jabali-hydra` + `StateDirectoryMode=0750`, extended `ReadWritePaths=` to cover `/var/lib/jabali-hydra`.
- `plans/m16-hydra-runbook.md`: backup uses `sqlite3 ... .backup` instead of `mysqldump jabali_hydra`; restore procedure is a file drop + chown + restart.

**Operator action on existing installs that hit the MariaDB blocker:** the half-populated `jabali_hydra` schema and the `/etc/jabali-panel/hydra-db-password` file are now unused. One-liner cleanup, idempotent and safe:

```
sudo mysql -e "DROP DATABASE IF EXISTS jabali_hydra; DROP USER IF EXISTS 'jabali_hydra'@'localhost'; FLUSH PRIVILEGES;"
sudo rm -f /etc/jabali-panel/hydra-db-password
```

All other decisions in the ADR still apply. The DB-choice text in the main Decision section below is superseded by this amendment — read any "MariaDB schema jabali_hydra" as "SQLite at /var/lib/jabali-hydra/db.sqlite3" post-2026-04-20.

---

## Context

M20 shipped Kratos as the panel's identity provider, but Kratos alone can't front third-party apps — it doesn't speak OAuth 2 or OIDC, only its own self-service API. WordPress (and the coming Automation API, M15) need a standards-compliant token issuer so per-install apps can trust the panel as an IdP without each app reinventing the login handshake. The two choices are (a) bake a minimal OIDC issuer into panel-api, or (b) layer Ory Hydra on top of Kratos in the documented login-consent pattern. We're taking (b).

The first consumer is WordPress SSO via the "OpenID Connect Generic" plugin. The architecture scales to any OIDC-capable app and (follow-up PR) client-credentials grants for the Automation API.

## Decision

Deploy **Ory Hydra v2.4.x** as a separate systemd unit under `jabali.slice`, bound to `127.0.0.1:4444` (public) and `127.0.0.1:4445` (admin), with its own MariaDB schema `jabali_hydra` (`sql_mode=NO_ENGINE_SUBSTITUTION` in the DSN, same K-2 workaround as Kratos). Panel-api proxies `/oauth2/*`, `/.well-known/openid-configuration`, `/.well-known/jwks.json`, and `/userinfo` in-process — same pattern as the M20 `kratos_proxy.go`. Hydra's login-consent flow delegates authentication to Kratos via the panel's `/oauth2-login` + `/oauth2-consent` handlers.

The apps framework mints a per-install OIDC client when a WordPress install is created: `client_id` + AES-GCM-sealed `client_secret` persist on `application_installs` (migration 000050). The installer auto-provisions the OpenID Connect Generic WP plugin (SHA-256 pinned) with the rendered callback URI. Trusted first-party clients (`metadata.trusted=true`, set server-side only by the apps framework) auto-accept consent; untrusted clients render the AntD consent screen at `/oauth2-consent`.

Full 16-decision matrix: plans/m16-hydra-oauth.md (archived on M16 rollback).

## Alternatives Considered

### Alternative 1: Hand-roll a minimal OIDC issuer inside panel-api
- **Pros**: No extra process, no extra DB schema, no extra systemd unit.
- **Cons**: Reinvents PKCE, refresh-token rotation, JWKS rotation, token revocation, introspection, consent persistence, client CRUD, all the edge cases. Every one is a security minefield.
- **Why not**: Hydra is 10+ years of hardening by a vendor focused exclusively on this problem. The "simpler" bespoke path is simpler only until the first CVE in our implementation.

### Alternative 2: External OIDC provider (Auth0, Keycloak SaaS, etc.)
- **Pros**: Zero ops burden.
- **Cons**: Breaks the "single-operator self-hosted" thesis of the panel. Adds a runtime dependency on a third party for every WP login. Per-tenant billing surprises.
- **Why not**: Panel ethos is "everything runs on your box." External IdP is for follow-up work (federation into Kratos), not a replacement for the token issuer.

### Alternative 3: Ory Hydra embedded in the panel-api process
- **Pros**: One fewer systemd unit.
- **Cons**: Hydra is distributed as a binary, not a Go library (its public API is CLI + HTTP). Embedding would mean vendoring an unsupported integration path and rebuilding on every Hydra release.
- **Why not**: Same reason we don't embed Kratos — separate lifecycle, separate release cadence, independent restart.

### Alternative 4: Migrate phpMyAdmin to Hydra too (unified SSO)
- **Pros**: One auth pattern instead of two (nonce + OIDC).
- **Cons**: phpMyAdmin isn't OIDC-native; its `signon.php` hook still needs the shadow-password exchange over UDS. Migration adds a Hydra client + OIDC flow on top of, not instead of, the existing plumbing. The nonce pattern works, is atomic (consume-by-hash), and is isolated to one app.
- **Why not**: Hydra pays off where new apps arrive — WordPress, Automation API, future multi-install apps. phpMyAdmin is a solved problem. Revisit only if phpMyAdmin ever moves to a separate subdomain or external-IdP federation lands.

## Consequences

### Positive
- WordPress SSO ships with a small panel surface area: two new HTTP handlers (`/oauth2-login`, `/oauth2-consent`) + one consent page.
- Future OIDC-speaking apps (GitLab, Gitea, Grafana, Nextcloud, anything) are one installer-descriptor addition away — no per-app auth glue.
- Automation API (M15) inherits a standards-compliant token issuer for free; just needs client-credentials grant enabled + scope middleware.
- Per-install OIDC clients give fine-grained revocation: delete one WP install, its client dies with it. No cross-install token contamination.
- Audit log parity with Kratos — both emit the same `event=hydra_* / event=kratos_*` slog lines.

### Negative
- One more systemd unit, one more MariaDB schema, one more binary to keep updated. Maintenance cost is the dominant trade-off.
- The login-consent flow adds two redirects to every WP login (panel → Hydra → panel → Hydra → WP). Latency is dominated by Hydra's admin-API round-trips; acceptable for interactive SSO but noted.
- Trusted-client auto-consent is convenient for panel-managed installs but means `metadata.trusted=true` must be unforgeable — set server-side only by the apps framework, never accepted from caller input. This is the single security-critical invariant of M16; a regression here grants silent consent for any scope to any registered client.

### Risks
- **R1**: Hydra's admin API accepts `metadata.trusted` from request body at client-create time. Mitigation: panel-api's `applications_service.go` strips the field from any caller-supplied payload before calling `hydraclient.CreateClient` and sets it server-side. Unit-tested.
- **R2**: Kratos session revocation doesn't auto-kill outstanding Hydra access tokens. Mitigation: every login-accept call includes `identity_provider_session_id = <kratos-session-token-hash>`; when Kratos revokes a session, a panel-side hook calls Hydra's `/oauth2/auth/sessions/logout` with that IdP session id to invalidate derived tokens.
- **R3**: DB growth from `hydra_oauth2_access` / `hydra_oauth2_refresh` tables under heavy SSO traffic. Mitigation: `hydra janitor` cron every 6h purges expired grants; documented in the runbook.
- **R4**: WP OpenID Connect Generic plugin CVE. Mitigation: SHA-256 pinned in `install/openid-connect-generic.sha256`; upgrades reviewed + re-pinned; plugin is GPL + widely deployed so upstream responsiveness is predictable.
- **R5**: Migration 000050 adds columns to `application_installs` — a non-breaking additive migration. Rollback is `git revert` + a column drop; existing rows stay valid (NULL oidc_client_id means "no SSO configured yet").

## Rollback

Hydra is additive. Turning it off leaves every existing auth path working: panel login stays on Kratos, WP installs without OIDC configured keep working via classic `wp-login`. WP installs with OIDC see "SSO unavailable" and fall through to classic login (the OpenID Connect Generic plugin's default "OIDC optional" behavior, which we enable explicitly). Full rollback:

```
systemctl stop jabali-hydra
systemctl disable jabali-hydra
git revert <M16 merge SHA>
# optional: ALTER TABLE application_installs DROP COLUMN oidc_client_id, DROP COLUMN oidc_client_secret_enc;
```

No data loss. Re-enabling = `systemctl enable --now jabali-hydra` + migration re-run. Orphan Hydra clients on already-deleted installs are harmless (their `redirect_uris` point nowhere).

## Scope fences (not in M16)

- phpMyAdmin SSO migration — stays on nonce pattern. Do not touch `panel-api/internal/api/sso_phpmyadmin*.go`.
- Automation API (client_credentials grant) — follow-up PR; Hydra foundation + WP SSO first.
- External IdP federation (Google/GitHub login) — Kratos `selfservice.methods.oidc` config flip; not in M16.
- Dynamic client registration (`/oauth2/register`) — disabled; all clients minted server-side by the apps framework.
- User-facing "my authorized apps" management UI — deferred.

## Acceptance criteria

- Green `make test` (Go + vitest).
- New Playwright E2E spec: full WP SSO flow (panel login → click "Login with Jabali" on WP → auto-consent → WP session established → logout drops both sessions).
- `hydra migrate sql` runs clean against MariaDB with `sql_mode=NO_ENGINE_SUBSTITUTION`.
- Install on fresh VM via `install.sh`: Hydra starts, `/oauth2/.well-known/openid-configuration` returns 200 through panel-api's proxy, admin endpoint is NOT reachable off-host.
- WP install via apps framework creates a working OIDC client end-to-end; plugin auto-configured; first login succeeds.
- Runbook (`plans/m16-hydra-runbook.md`) covers client CRUD via `hydra` CLI, token revocation, DB backup/restore, janitor schedule, CVE-response procedure.
- ADR superseded notes added to ADR-0034 (Kratos now has a peer) and to this ADR's `Related` list.

---

**Status change**: `proposed` → `accepted` on 2026-04-20 when Waves A–E shipped (Wave F Automation API remains deferred — separate ADR when scheduled).
