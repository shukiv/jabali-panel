# ADR-0131: Python Application Manager via native per-user systemd + nginx proxy (not Passenger)

**Date**: 2026-06-16
**Status**: Proposed
**Deciders**: shuki + Claude
**Related**: ADR-0023 (M9 PHP-FPM per-user pools), M18 (per-user cgroup
slices), M25 (unix-socket lockdown), M34 (per-user egress firewall), the
Docker-app subsystem. Implements GH #203.

## Context

jabali runs only PHP today (PHP-FPM per-user pools). Operators want to host
**Python** web apps (Django/Flask/FastAPI) the way cPanel's Application
Manager does: register an app, point it at a domain/path, pick a runtime, set
env vars, start/stop — no shell. cPanel implements this with **Phusion
Passenger** (a web-server module that spawns and supervises the app).

## Decision

Run each app as a **per-user systemd service on a unix socket**, reverse-
proxied by nginx `proxy_pass` — NOT via a Passenger nginx module.

- Python WSGI apps → **gunicorn**; ASGI apps → **uvicorn**, bound to
  `/run/jabali-app/<app_id>.sock`.
- The service runs `User=<owner>` inside the owner's **user slice** (cgroup
  cpu/mem/pids limits, M18) and **egress firewall** (M34).
- nginx serves it via a per-domain include (`location <base_uri> { proxy_pass
  http://unix:<sock>; }`) — the existing per-domain include dir + proxy
  directives, `nginx -t`-gated.
- DB-as-truth: a `python_apps` registry (mirroring `docker_apps`) + a
  reconciler hook converge the venv, the systemd unit, and the nginx include.
- **Opt-in**: `server_settings.python_apps_enabled` (default off); a Server
  Settings → Apps tab toggle installs the runtimes and reveals the feature.

## Rationale

- **Reuses jabali's machinery.** Per-user systemd services, the user slice,
  the egress firewall, per-domain nginx includes, and the reconciler already
  exist (FPM pools + Docker apps). The native model drops straight in; a
  Passenger module would be a parallel, foreign mechanism.
- **No nginx coupling.** jabali deliberately runs Debian-native nginx
  (`feedback_nginx_debian_native_not_sury`). Passenger needs its apt repo +
  a dynamic nginx module — a standing coupling and upgrade-fragility we avoid.
- **Same containment as PHP.** Apps run as the user, slice-limited and
  egress-filtered — identical blast radius to PHP-FPM, not a new root-adjacent
  surface.
- **Same UX.** Register → domain/path, runtime+version, entrypoint, env,
  restart, logs — delivered without Passenger.

## Alternatives considered

- **Phusion Passenger (cPanel-identical).** Closest to cPanel and less
  process-management code, but couples nginx to a Passenger module on
  Debian-native nginx and bypasses jabali's per-user-systemd/reconciler/slice
  model. Rejected for coupling + architectural mismatch.
- **Docker-only (run Python apps as containers).** The Docker-app subsystem
  already exists, but it's a different UX (operator supplies an image/compose)
  and not the native "App Manager" experience #203 asks for. Kept as a
  separate offering, not the answer here.

## Consequences

### Positive
- Native Python hosting with the cPanel App-Manager UX, fully inside jabali's
  existing isolation + convergence model; no new web-server module.
- Extends cleanly to Node (NodeSource + `node` service) and Ruby (puma) by
  adding `runtime` enum values + per-runtime installers — same shape.

### Negative
- jabali owns the process-management glue (venv build, gunicorn/uvicorn unit,
  health) that Passenger would otherwise provide — more agent/reconciler code.
- A new always-available runtime to keep patched (offered Python versions).
  Mitigated: opt-in (default off), per-venv server installs, resource caps on.

## Implementation

Blueprint `plans/m-python-app-manager.md`. Migration `000168`
(`python_apps` + `python_app_env`); `server_settings.python_apps_enabled`;
`app.python.*` agent commands; `jabali-app@<id>.service` template;
`reconcilePythonApps`; Application Manager UI + Server Settings → Apps tab.
Waves A–F; live-verify a Flask + a FastAPI app before Accepted.

## Addendum — Framework marketplace (JAB-164, 2026-07)

A one-click framework marketplace layered on the runtime. Each entry is
`install/py-frameworks/<slug>/framework.yaml` plus a `template/` starter or a
`patch.py` (Flask, FastAPI, Django, Starlette, Litestar, Quart, Dash, Bottle,
Django Channels, Wagtail). `pyframeworks.LoadDir` loads the catalog from
`/usr/local/share/jabali/py-frameworks` (dev fallback `install/py-frameworks`).

The create flow — CLI `python-app create --framework <slug>` and
`POST /python-apps {framework}` — derives `app_type` + `entrypoint` from the
entry and stamps `python_apps.framework` (+ `scaffolded_at`, migration
`000229`). Before the first `apply`, the reconciler runs a one-shot
`app.python.scaffold`: venv + pinned deps + starter (`django-admin` /
`wagtail start`, or template files) + settings hardening + `migrate` /
`collectstatic`, all AS THE TENANT. Generated secrets (Django `SECRET_KEY`,
minted with stdlib `secrets`) go to the app env, never to source.

Settings hardening is either the generic in-agent patch (a plain
`config/settings.py`: `SECRET_KEY`/`ALLOWED_HOSTS` from env, `STATIC_ROOT`) or,
for frameworks whose layout differs, a per-entry `patch.py` run as the tenant
(Wagtail's `config/settings/{base,production}.py` package; Channels'
`INSTALLED_APPS`/`ASGI_APPLICATION` + a `ProtocolTypeRouter` asgi.py). Django
static is served by an admin-only nginx `static_alias` rule (alias constrained
under `/home/`) attached alongside `proxy_pass`. Scaffold file writes go through
`sudo -u <tenant> tee` (symlink-TOCTOU-safe). UI: the Python Apps page gains a
Catalog tab (framework cards + one-click install); brand icons are vendored
under `panel-ui/public/framework-icons`.

Known limit: an ASGI framework mounted at a sub-path `base_uri` relies on
`uvicorn --root-path`, which the framework may not strip before routing (Django
ASGI/Channels, Starlette, Litestar, Quart); root (`/`) mounts are unaffected.
WSGI apps (gunicorn `SCRIPT_NAME`: Flask, Django, Wagtail, Dash, Bottle) handle
sub-paths cleanly.
