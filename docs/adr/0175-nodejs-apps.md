# ADR-0175: Node.js apps on the Application Manager (runtime `node`)

**Date**: 2026-10-20
**Status**: Proposed — open questions below need a decision before code
**Deciders**: shuki + Claude
**Related**: ADR-0131 (Python Application Manager), ADR-0084 (per-user
egress firewall), M18 (per-user cgroup slices), the Docker-app subsystem.

## Context

Users ask to host Node.js apps (Express, Fastify, NestJS, Next.js, Nuxt,
SvelteKit…) the way they host Python apps today, without Docker.

ADR-0131 planned for this: the `python_apps` table already has a `runtime`
column (`varchar(16)`, default `python`), and its consequences section names
Node as the next runtime, "same shape".

Three facts shape the decision:

- **Node 22 LTS is already on every host.** `install_base_packages` installs
  `nodejs` from NodeSource's `node_22.x` repo (`_install_nodesource_source`,
  signed-by keyring), for the panel UI build and the webmail. A Node app
  runtime needs no new package for its default version.
- **Python apps bind loopback TCP, not a unix socket.** ADR-0131 says
  `/run/jabali-app/<id>.sock`, but the agent starts gunicorn/uvicorn on
  `127.0.0.1:<port>` and nginx proxies to that port
  (`python_app_apply.go`). Node apps would follow the code, not the ADR text.
- **Node apps choose their own listen call.** Most read `process.env.PORT`;
  some frameworks take only a number (Next.js `next start -p`), and some
  read their own variables (SvelteKit adapter-node: `PORT`/`HOST` or
  `SOCKET_PATH`).

## Decision (proposed)

Add `runtime = node` to the Application Manager, reusing what Python apps
built:

- **Registry**: the same `python_apps` / `python_app_env` tables with
  `runtime = 'node'`. No rename now (a rename touches every Python query;
  revisit if a third runtime lands).
- **Process**: the same `jabali-app@<id>.service` shape: `User=<owner>`, the
  owner's slice (CPU, memory, pids limits) and egress firewall. `ExecStart`
  is one of two app types:
  - `node-script`: `node <entrypoint>`
  - `npm-start`: `npm run start` (for frameworks whose start script wraps the
    server).
- **Listening**: the panel allocates a loopback port as for Python apps and
  passes `PORT=<port>` and `HOST=127.0.0.1`. An app that ignores both does
  not come up. The health check reports "not listening on PORT", the same
  way a Python app that fails to import is reported.
- **Dependencies**: `npm ci` when `package-lock.json` exists, otherwise
  `npm install`, with `--omit=dev` unless the app has a build step. It runs as
  the tenant in the app root, with the npm cache under the tenant's home.
  An optional build command (default `npm run build` when `package.json` has
  one) runs after install and before start.
- **nginx**: the existing per-domain `proxy_pass` rule to
  `127.0.0.1:<port>`, with WebSocket upgrade on (Node apps use it more often
  than Python ones).
- **Opt-in**: `server_settings.node_apps_enabled`, default off, next to the
  Python toggle on Server Settings → Apps. Turning it on installs nothing by
  default, since Node 22 is already present.
- **Later, not in the first slice**: a framework catalog like JAB-164's
  (Express, Fastify, NestJS, Next.js, Nuxt, SvelteKit, Astro) with starter
  templates and per-framework start commands.

## Open questions (need a decision)

1. **Node versions.** Only the host's Node 22? Or also 20 / 24 as
   per-version installs from nodejs.org tarballs, checked against
   `SHASUMS256.txt` signed by the Node release keys and unpacked under
   `/opt/jabali/node/<major>`? More versions mean more runtimes to keep
   patched.
2. **npm install scripts.** Run them (needed by native modules such as
   `bcrypt`, `sharp`, `sqlite3`) or pass `--ignore-scripts`? They run as the
   tenant, inside the slice and the egress firewall, which is the same trust
   the app itself has. Recommendation: run them.
3. **Registry egress.** The tenant's egress firewall allows `:443` to
   anywhere, so `registry.npmjs.org` is reachable. A tenant on a stricter
   custom policy needs it allowlisted. Document it, or add it to the
   built-in allowlist?
4. **Loopback isolation (to verify before Accepted).** Can one tenant's
   process connect to another tenant's app port on `127.0.0.1`? That holds
   for Python apps today if it holds at all; a Node runtime does not change
   it, but the verification belongs here. If it is reachable, the fix (an
   nftables rule letting only nginx's uid reach the app port range) covers
   both runtimes.
5. **UI**: one Applications page with a runtime selector, or a separate
   Node.js Apps page next to Python Apps?

## Alternatives considered

- **Phusion Passenger**: rejected for the reasons in ADR-0131 (an nginx
  module coupled to Debian-native nginx).
- **PM2 inside the app**: a second process supervisor under systemd, with
  its own daemon, logs and restart policy. systemd already supervises,
  restarts, limits and logs; rejected.
- **Per-user nvm / Volta**: every tenant would get their own unpatched
  toolchain, outside the panel's view. Rejected; versions stay admin-provided.
- **Docker only**: already available through Docker apps, but that asks the
  tenant for an image or compose file, not "point at my code". Kept as a
  separate offering.

## Consequences

### Positive
- Node hosting with the same create / start / logs / env UX as Python apps,
  inside the same isolation (user, slice, egress) and reconciler model.
- No new default runtime to install or patch: Node 22 is already on every host.

### Negative
- More agent code: npm install/build steps, start-command handling, and the
  health message for apps that do not listen on `PORT`.
- Apps that hard-code their port, or cannot listen on a given one, need
  changes before they run here.
