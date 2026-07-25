# Jabali public demo — nginx-overlay recipe (JAB-159)

A public demo where **panel-api runs 100% stock `main`** (so `jabali update` keeps
it current) and ALL demo behaviour lives in one reviewable nginx vhost. No demo
code in the program.

> **Read this first.** A public demo hands anonymous visitors an admin session on
> a real panel. The nginx write-block is roughly **30%** of the safety. The rest is
> the host being **disposable** with the hardening below. Do **not** point this at a
> panel that holds anything real.

## Files
- `jabali-demo-vhost.conf.tmpl` — drop-in REPLACEMENT for the panel vhost on the
  demo host. Substitutes `${SSL_CERT_PATH}` `${SSL_KEY_PATH}` `${PANEL_DIST_DIR}`
  `${NGX_H2_PARAM}` `${NGX_H2_DIR}` `${DEMO_BANNER_HTML}`.

## What the vhost enforces
1. `/api/v1/` — writes (POST/PUT/PATCH/DELETE) → 403. Covers every mutation,
   including the POST SSO-mint routes (`/api/v1/sso/{phpmyadmin,adminer,webmail}`).
2. `/phpmyadmin/` + `/jabali-adminer/` → 403 (DB consoles off; not `include`d).
3. Kratos `/self-service/{settings,recovery}` submit (non-GET) → 403 (no account
   takeover); login stays open.
4. DEMO banner injected into the SPA HTML via `sub_filter`.
5. Anon per-IP rate-limit.

## Host hardening — the other 70% (do ALL of these)
- [ ] **Disposable box.** Dedicated VM, no real tenants, no real data, NO secrets
      shared with production. Treat it as hostile-facing; rebuild freely. This is
      the primary control.
- [ ] **Read-only MySQL user for panel-api.** THE key backstop: point
      `DATABASE_URL` at a user with `SELECT`-only grants on `jabali_panel`. Any
      write that slips the nginx block (a side-effect GET, a path we missed) hits
      `Access denied` instead of succeeding. Grant example:
      `GRANT SELECT ON jabali_panel.* TO 'jabali_demo_ro'@'localhost';`
      (Seed the DB first with a normal user, then switch panel-api to the RO user.)
- [ ] **Neuter the agent.** The demo box's jabali-agent must do no real OS/service
      work — stop/mask it or firewall its socket, so a slipped mutation can't pivot
      to the host. Reads that need the agent will error; that's fine for a demo.
- [ ] **Seed two Kratos identities** — a demo admin + a demo user with throwaway
      passwords. Put those creds in `${DEMO_BANNER_HTML}` so visitors can log in.
- [ ] **Network isolation** — the demo host must reach nothing internal.

## Apply (on the demo host)
1. Install the panel normally (stock `main`). Confirm `/run/jabali-panel/api.sock`.
2. Render the template (substitute the vars) to
   `/etc/nginx/sites-available/jabali-panel-vhost.conf` (replacing the stock one),
   e.g. with the same `envsubst`/sed the installer uses, setting `DEMO_BANNER_HTML`
   to a fixed-position banner, for example:
   ```
   <div style="position:fixed;bottom:0;left:0;right:0;z-index:99999;background:#b45309;color:#fff;text-align:center;padding:8px;font:14px sans-serif">
     DEMO — read-only. Log in: admin@demo / demo1234 · user@demo / demo1234
   </div>
   ```
3. `nginx -t && systemctl reload nginx`.
4. Do the hardening checklist above (RO DB user + neutered agent especially).

## Verify (must all pass)
- `curl -sk -X POST https://<demo>:8443/api/v1/domains` → **403**
- `curl -sk https://<demo>:8443/phpmyadmin/` → **403**
- `curl -sk -X POST 'https://<demo>:8443/.ory/self-service/settings?flow=x'` → **403**
- login as the demo account works; changing its password is refused
- a slipped write (if any) fails at the DB with `Access denied` (RO user)
- the DEMO banner shows on every page

## Residual risk (accepted)
GET endpoints with side effects can't be enumerated at nginx — mitigated by the
RO DB user + neutered agent + disposable box. Admin reads expose config/log shape;
acceptable with no real data on the box.

## Update
`jabali update` on the demo host tracks `main` and rebuilds stock panel-api. This
vhost is independent — it only changes if you edit it here. That's the whole point:
**the demo updates like any install; the demo behaviour never touches the program.**
