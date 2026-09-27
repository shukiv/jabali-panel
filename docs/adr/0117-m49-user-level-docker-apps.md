# ADR-0117: M49 User-Level Docker Apps — Architectural Decisions

**Date:** 2026-06-21
**Status:** Accepted (2026-06-21) — all 7 phases landed; Phase 0 cgroup nesting + Phase 2 userns-remap/hardening live-verified on mx. Remaining: full tenant-install Playwright E2E on a tenant-docker-enabled host.
**Owner:** shuki
**Companion plan:** [`plans/m49-user-docker-apps.md`](../../plans/m49-user-docker-apps.md)
**Supersedes scope of:** ADR-0116 Decision 1 (which scoped Docker apps admin-only and queued per-tenant)

---

## Context

GH #170 asks to let **tenants** install Docker apps, not just admins. ADR-0116
Decision 1 deferred this as needing "rootless podman + per-tenant socket
scoping." The headline problem is isolation: tenants are semi-trusted, the
daemon is rootful, and a careless or hostile install must not reach the host or
another tenant. These decisions define the boundaries for a v1 that reuses the
M48 substrate rather than rewriting it.

A reframe drives everything: these tenants **already run arbitrary code as their
unix user** (M12 SFTP, M13 SSH sandbox, per-user PHP-FPM). So a Docker app is
not a tenant's first foothold — the only *new* risk is **escape-to-root beyond
the uid the tenant already has**. That makes the feature more defensible (we
contain an existing trust level) and makes userns-remap the load-bearing control.

---

## Decision 1 — Ownership: `docker_apps.user_id`, NULL = admin

Add `user_id` to `docker_apps`. NULL = admin/server-level (M48 behaviour,
preserved). Set = tenant-owned; every tenant verb filters on it; `ON DELETE
CASCADE` so a user delete drops the row (host teardown enqueued by the delete
path before the cascade — Phase 6). The M48 global `uniq(slug,name)` becomes
owner-scoped `uniq(user_id,slug,name)` so two tenants can install the same
catalog app; tenant `instance_slug` is namespaced `<slug>-<short_uid>-<name>`.

## Decision 2 — Engine unchanged: rootful Docker, agent-only socket

No change to ADR-0116 Decision 2. The agent remains the only holder of
`docker.sock`; panel-api and tenants never touch it. Tenant REST speaks only to
the agent's mediated verbs.

## Decision 3 — Isolation: userns-remap MANDATORY on tenant-enabled hosts

Tenant-enabled hosts run dockerd with daemon-wide `userns-remap` ("default" →
the `dockremap` subuid range). A container-root escape lands as an unprivileged
host subuid, never host root — the one new risk (Context) neutralised. Gated by
the host flag `/etc/jabali/docker-tenant-enabled`, written only after remap is
live and the existing-admin-app data retrofit succeeds. `max_docker_apps>0` has
no effect without that flag (install 403s).

**Cost owned, not waved:** enabling remap daemon-wide breaks existing M48
admin-app data ownership. A one-time `jabali docker enable-tenant` migration
(down → write daemon.json remap → chown app data trees into the subuid range →
up → health → set flag), with rollback if any app fails health (Phase 2).

**Rejected for v1:** rootless Podman per tenant (strongest isolation but a large
infra rewrite — deferred as the v2 ceiling); per-tenant subuid ranges (v1 uses
the single shared `dockremap` range — enough for escape-to-root containment;
per-tenant ranges land with the podman v2).

## Decision 4 — Resource binding: nest in the M18 per-user slice

Tenant containers render with `cgroup_parent: jabali-user-<username>.slice` so
their cgroup nests under the tenant's existing M18 slice and the package
CPU/memory ceiling caps the tenant's containers + shell + FPM **in aggregate** —
no new per-app resource accounting.

**Verified (Phase 0 spike, mx, 2026-06-21):** a rootful container with
`--cgroup-parent` nests as a systemd `docker-<id>.scope` under the slice,
**survives `systemctl daemon-reload`** (systemd does not evict it — the gating
risk, disproven), and the slice `MemoryMax` caps it (a memory hog hit OOM,
`memory.peak` pinned at the cap, host unaffected). Requires the systemd cgroup
driver (install.sh must guarantee it on tenant-enabled hosts).

## Decision 5 — Mandatory hardening profile for tenant installs

For tenant installs the agent merges a `compose.tenant-hardening.yml` overlay:
`no-new-privileges:true`, `cap_drop: ALL` + the app's verified `tenant_caps`
allowlist, `pids_limit`, `cgroup_parent` (Decision 4). The agent validates the
merged `docker compose config` and **rejects** `privileged`, any `cap_add`
beyond `tenant_caps`, and any bind-mount outside the app data tree.

`cap_drop: ALL` alone breaks apps that chown-then-drop (vaultwarden, linkwarden,
ghost), so each `tenant_installable` app declares a **verified minimal
`tenant_caps`** — established by actually running it under drop-ALL, not guessed.

### Amendment 2026-09-27 — project, networks and service set pinned (GH #1903)

The agent gate also refuses, on every tenant bring-up:

- a compose project name other than the app directory's own (a top-level
  `name:` moves `up`/`down` onto another project's containers);
- any network but the project's own default one: no external or extra
  networks, no custom name, driver options or IPAM, and no `network_mode`
  other than `none` or a sibling service (`bridge` is docker's shared bridge);
- `cgroup: host`, device grants (`gpus`, `deploy.resources.reservations`),
  another `runtime`, OOM and sysctl knobs, and any limit outside
  `deploy.resources.limits`.

On the doors that write a compose (install, re-render update, env edit,
domain/port edit, and their CLI twins), panel-api also sends
`tenant_services`: the service names, images and limits of a second render of
the catalog template with every tenant value (env, domain, name, ports)
removed. The agent requires the resolved compose to match it exactly and
refuses a tenant compose write without it. Doors that bring up the on-disk
compose (start, restart, recovery, restore, image-only update) apply the rules
above but not the service-set match: that file was pinned when written, and
pinning it to the *current* catalog would break every restart after a catalog
bump and every restore of an older snapshot. The legacy top-level
`pids_limit`, which the M49 overlay wrote before GH #284, is refused only on a
fresh render for the same reason.

## Decision 6 — Curated catalog: `tenant_installable` + loopback-only

`app.yaml` gains `tenant_installable` (default **false** — admin-only) and
`tenant_caps`. The tenant catalog returns only `tenant_installable: true`
entries that declare no `default_bind: public` port. Tenant installs are
**loopback-only**, auto-allocated 10000–19999, reverse-proxied through nginx; no
public bind, no host-port pinning. (Phase 1 lands the fields with every app
still `false`; an app is flipped true only after its `tenant_caps` are verified —
before Phase 3 exposes the tenant catalog.)

## Decision 7 — Package gate: `hosting_packages.max_docker_apps`

`max_docker_apps` (default **0** = docker apps not included — opt-in per plan,
no surprise grant on existing packages). >0 = max simultaneous tenant installs.
Install checks: host flag present, package value >0, live count < value, disk
footprint vs `disk_quota_mb` (metered via mig-162 `data_bytes`; hard FS quota is
a follow-up). Per-app cpu/mem clamp to the package budget.

## Decision 8 — No tenant exec shell / edit compose (ADR-0116 D10 stands)

Shell-in-container ≈ code-exec as the container user with breakout risk; edit-
compose lets a tenant write arbitrary compose. Both remain admin-only, permanent.
Tenant lifecycle is install / start / stop / restart / logs / env / delete /
backup only.

## Decision 9 — Domain + backup reuse, tenant-scoped

Domain attach reuses the M48 path (`managed_by='docker_app'`, `docker_app_id`,
`user_id=caller`) and the existing guard that refuses a domain owned by another
user. Tenant app backups (Phase 3) must target a tenant-owned backup
destination, or fall back to the default repo only — never the admin's or
another tenant's destination.

---

## Consequences

**Positive**
- Reuses the entire M48 substrate (domains + nginx + LE + restic + reconciler);
  net-new surface is `user_id`, a hardening overlay, a quota gate, and a tenant
  catalog filter.
- The single new risk (escape-to-host-root) is covered by mandatory userns-remap;
  resource fairness is free via the proven M18 slice nesting.

**Negative**
- Daemon-wide userns-remap forces a one-time retrofit of existing admin-app data
  ownership on hosts that turn tenant docker on.
- v1's single shared `dockremap` range gives no cross-container uid isolation
  between tenants (apps are still separated + slice-capped); per-tenant ranges
  wait for the podman v2.
- Curated `tenant_installable` set is small at first (each app needs empirical
  `tenant_caps` verification before exposure).

**Operational**
- New host prerequisite chain on tenant-enabled hosts: systemd cgroup driver +
  userns-remap + `/etc/jabali/docker-tenant-enabled`. install.sh + the
  `jabali docker enable-tenant` migration own it (Phase 2).

---

## Reviewed
- 2026-06-21 — drafted; Phase 0 cgroup spike PASSED on mx; userns-remap decided
  mandatory. Advisor-reviewed blueprint findings folded.
