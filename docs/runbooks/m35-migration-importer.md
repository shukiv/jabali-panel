# M35 Migration Importer Runbook

**Scope**: operator-facing reference for the M35.1 / M35.2 migration
GUI shipped via ADR-0094 + ADR-0095.

---

## TL;DR — three flows

> **Fastest path — offline cpmove you already have on the box:**
> ```
> sudo jabali migrate restore --cpanel --file /path/cpmove-<user>.tar.gz \
>      [--target-email you@dom --target-password '<≥10 chars>']
> ```
> One command: creates the job, stages the tarball, runs the full
> pipeline. Source account read from the filename; target defaults to
> it. Re-run to resume. No UI / no job-id needed.


| When | Entry point | Endpoint shape |
|---|---|---|
| Single cPanel / DA / Hestia account, you already know host+user+password | List page → **New migration** | POST `/admin/migrations` + per-step driver |
| Bulk WHM, you have account list in hand | List page → **Bulk WHM (paste)** | POST `/admin/migrations/bulk` |
| Anything else (discover-driven, WHM with picker) | List page → **Wizard** | Draft → PATCH → discover → bulk/submit |

---

## URLs

| Path | Purpose |
|---|---|
| `/jabali-admin/migrations` | List view (stats + batch column + per-row actions) |
| `/jabali-admin/migrations/<id>` | Per-job detail (SSE-driven stages + retry buttons) |
| `/jabali-admin/migrations?wizard=<id>` | Resume a draft wizard (set automatically) |

---

## Wizard step → endpoint map

| Step | Action | Endpoint |
|---|---|---|
| 1 — Source | Pick `cpanel` / `whm_pkgacct` / `directadmin` / `hestiacp` | `POST /admin/migrations {state:"draft"}` |
| 2 — Connection | Fill host + admin user + password / SSH key | `PATCH /admin/migrations/:id` + `POST /admin/migrations/:id/secrets` |
| 3 — Accounts (WHM only) | Multi-select from live `whmapi1 listaccts` | `GET /admin/migrations/:id/discover-accounts` |
| 4 — Review & submit | WHM: bulk create. Other: submit draft. | `POST /admin/migrations/bulk` OR `POST /admin/migrations/:id/submit` |

---

## Backend endpoints

### Job lifecycle
```
POST   /admin/migrations                 create (state=pending OR draft)
PATCH  /admin/migrations/:id             update draft (state=draft only)
POST   /admin/migrations/:id/submit      draft → pending
POST   /admin/migrations/:id/secrets     write per-job .env
POST   /admin/migrations/:id/pull-source kick off SSH pull
POST   /admin/migrations/:id/import      kick off import runner
POST   /admin/migrations/:id/tarball     upload pre-built cpmove tar
GET    /admin/migrations/:id             one job + recent stages
GET    /admin/migrations/:id/stages      full stage timeline
GET    /admin/migrations/:id/stream      SSE — live snapshots (2s)
DELETE /admin/migrations/:id             soft-revoke
POST   /admin/migrations/:id/destroy     hard-delete row + secret + dir
POST   /admin/migrations/:id/retry       resume retry
POST   /admin/migrations/:id/retry?from_scratch=true   nuke stages + retry
```

### Bulk (WHM)
```
POST   /admin/migrations/bulk            N drafts sharing batch_id
DELETE /admin/migrations/batches/:id     cancel every non-terminal in batch
```

### Discovery (live source contact via stored secret)
```
GET /admin/migrations/:id/discover-accounts          list source accounts
GET /admin/migrations/:id/account-size/:user         live du -sh probe (caches 24h)
GET /admin/migrations/discover-accounts/:host/:user/size   cache-only lookup (503 if cold)
```

---

## SSRF guard (ADR-0095 decision 8)

Every outbound dial from `internal/migrate/` (discover, secrets push,
pull-source, account-size probe) is gated by
`migrate.ValidateHost` + `Dialer.Control`, which classify addresses
with `ssrfguard.CheckIP`. Always refused, override or not:

- 127.0.0.0/8, ::1 loopback
- 169.254.0.0/16, fe80::/10 link-local (AWS/GCP metadata)
- 0.0.0.0/8, ::/96 (IPv4-compatible IPv6), multicast

Refused unless the override is on:

- 10.0.0.0/8 + 172.16.0.0/12 + 192.168.0.0/16 RFC1918, fc00::/7 ULA
- 100.64.0.0/10 CGNAT, which includes Tailscale addresses
- 198.18.0.0/15, 240.0.0.0/4, fec0::/10, 64:ff9b:1::/48

A NAT64 (64:ff9b::/96) or 6to4 (2002::/16) address is held to the rule
of the IPv4 address it carries.

Override:
```
mysql -e "UPDATE server_settings SET migration_allow_private_hosts=1"
```
The setting is read on each request; no restart needed. The DNS-rebinding-protection
peer-IP check still applies — connections to public hostnames whose
record was swapped for an internal IP between resolve and dial still
fail.

---

## Retry semantics (ADR-0095 decision 7)

| Action | What runs | When to use |
|---|---|---|
| **Retry (resume)** | Re-runs stages whose state ≠ done. DB restore now wraps in `DROP DATABASE IF EXISTS ; CREATE DATABASE ;` so partial DB imports are idempotent. | Transient infra blip — SSH dropout, source rebooted, disk full |
| **Retry from scratch** | Wipes every stage row + re-runs from analyze. Existing `extracted/` is overwritten on next pull. | Source data changed; you re-uploaded a different cpmove tarball; mailbox-only sync re-do |
| **Destroy** | Hard-delete job row + secret + `/var/lib/jabali-migrations/<id>/`. | Job is unsalvageable + you want the (host, user, kind) unique slot back |

---

## Cancel batch

In the list view, click the purple **batch_id** tag (last 6 ULID
chars). Popconfirm asks for confirmation; OK fires
`DELETE /admin/migrations/batches/:id`. Backend transitions every
non-terminal job in the batch to `cancelled`. Terminal jobs (done /
failed / cancelled) stay untouched.

---

## Reaper

`jabali migrate reap-secrets` (run daily by
`jabali-migration-secrets-reap.timer` at 04:30 UTC) does FOUR
things:

1. Deletes `/etc/jabali-panel/migration-secrets/<id>.env` for any
   job in a terminal state.
2. Hard-deletes any `state=draft` job whose `updated_at` is older
   than 24h. Drafts hold no secrets (those are written at Step 2,
   which flips the row to a non-draft state) so deletion is safe.
3. Removes `/var/lib/jabali-migrations/<id>/{cpmove-*.tar.gz,extracted/}`
   for terminal jobs whose `ended_at + --staging-max-age (default
   7d)` has passed. Pass `--staging-max-age 0` to wipe immediately.
4. Source-side: `cpanel.RemoveRemote` ssh-rms the
   `/home/cpmove-<user>.tar.gz` that pkgacct produced. Runs in-
   line after `PullFile` so a multi-account WHM bulk doesn't
   accumulate GBs on the source.

Operator can `--dry-run` to preview without deleting.

## Wizard defaults (M35.6 — M35.8)

`jabali migrate import` with no `--target-*` flags resolves:

| Slot | Source | Stored where | Note |
|---|---|---|---|
| Username | `job.SourceUser` | panel `users.username` + Linux | same as source |
| Email | cpmove `contactemail` or `CONTACTEMAIL=` in `cp/<user>` userdata; falls back to `<sourceuser>@<sourcehost>` | panel `users.email` | |
| SSH/SFTP password | source `/etc/shadow` `$6$…` hash | dest `/etc/shadow` via `chpasswd -e` | same as source (M35.7) |
| Panel/Kratos login | random 16-char (printed once) | Kratos Argon2 | source crypt(3) hash format-incompatible — Kratos 409 conflict reuses existing identity (M35.8) |

## Restore stage area writers (in dispatch order)

| # | Area | Agent cmd | Effect |
|---|---|---|---|
| 1 | SSH keys | DB only | `.ssh/authorized_keys` rows |
| 2 | Cron | DB only | crontab rows |
| 3 | Databases | `db.create` + `db.restore` (idempotent reset) | MariaDB import; `Credentials` map captured for #8 |
| 4 | Home split | `migration.import_home` × N | per-domain rsync to `/home/<u>/domains/<dom>/public_html/` with nested-domain excludes + final rest-of-homedir rsync (M35.8 P7) |
| 5 | Domains | `domain.create` | nginx vhost with docroot `/home/<u>/domains/<dom>/public_html`; email enable + DKIM regen |
| 6 | Mailboxes | `migration.import_mailboxes` | JMAP push of INBOX + Maildir+ subfolders (Drafts/Junk/Sent/Trash/Spam/Archive) + cpanel owner mailbox at `<u>@<primary-domain>` |
| 7 | SSL custom | `ssl.install_custom` | source `apache_tls/<dom>/` → `/etc/letsencrypt/live/<dom>/{fullchain,privkey}.pem` + nginx reload |
| 8 | App configs | `files.read` + `files.write` | rewrites wp-config.php / configuration.php / sites/default/settings.php / app/etc/env.php with new `(db_name, db_user, db_pass)` from #3 |
| 9 | Extras | mixed | catch-all + subdomains + forwarders + autoresponders + sieve filters + per-domain PHP pools + DKIM legacy key preserve + FTP observation |
| 10 | Kratos | `kratos.create_identity` | panel-login identity; 409 conflict reuses existing |

## Override knobs

- UI **Allow private IPs** toggle → flips `server_settings.migration_allow_private_hosts` (SSRF override applied at request-time)
- `--target-user / --target-email / --target-password` flags override auto-detect
- **Re-kick** button on pending rows = re-POST `/admin/migrations/:id/pull-source` (rows stuck pre-auto-kick deploy)
- Hidden states: drafts (wizard-internal scratchpad) never appear in UI; daily reaper sweeps after 24h
- `FindBySource` collision check (M35.8) skips drafts + terminal rows — only blocks fresh wizard runs on actively-running jobs

---

## Where things live

| Component | Path |
|---|---|
| Frontend | `panel-ui/src/shells/admin/migrations/` |
| Wizard | `panel-ui/src/shells/admin/migrations/CreateMigrationWizard.tsx` |
| SSE hook | `panel-ui/src/hooks/useMigrationStream.ts` |
| Backend routes | `panel-api/internal/api/admin_migrations.go` |
| Discoverer registry | `panel-api/internal/migrate/registry.go` |
| Per-source impl | `panel-api/internal/migrate/{cpanel,directadmin,hestiacp}/` |
| SSRF helper | `panel-api/internal/migrate/ssrf.go` |
| Agent DB restore | `panel-agent/internal/commands/db_restore.go` |
| Agent runner glue | `panel-agent/internal/commands/migration_admin_run.go` |
| Secrets dir | `/etc/jabali-panel/migration-secrets/` (root:jabali 0640) |
| Per-job staging | `/var/lib/jabali-migrations/<job-id>/` |
| Reaper unit | `jabali-migration-secrets-reap.timer` (daily 04:30 UTC) |

---

## Troubleshooting

### "secret_missing" (412) on /:id/discover-accounts
Operator skipped step 2 (POST `/:id/secrets`). Open the wizard
with `?wizard=<id>` and finish step 2.

### "wrong_state" (409) on PATCH /:id
The row isn't in `state=draft`. Either it was already submitted
(can't edit pending jobs) or reaped (>24h old draft). Check
`SELECT state, updated_at FROM migration_jobs WHERE id=?`.

### SSE stream returns 502 from nginx
nginx WS-proxy block must include `proxy_buffering off;`. Already
configured for `/api/v1/logs/stream/*`; the migrations stream
endpoint inherits the same location block. If you've forked the
vhost template, mirror the buffering setting.

### CREATE TABLE conflict on retry-resume
Update the panel — the M35.2 patch (commit `d4415d69`) sets
`reset_before_restore=true` on every db.restore call from the
migration importer. If you're still seeing this on jabali ≥ 0.2.10,
re-run the migration with **Retry from scratch** to force a fresh
DB.

### "...rejected; flip migration_allow_private_hosts to override"
Source server is on an internal network, a VPN such as Tailscale
(CGNAT, 100.64.0.0/10) or other non-public space. Either:
- Add a public DNS entry pointing at the source, or
- Flip the **Allow private IPs** toggle in the Migrations admin
  page (writes `server_settings.migration_allow_private_hosts=1`).
  Override is honored at request-time — no panel restart needed.

### "source SSH server rejected the supplied auth method (likely PasswordAuthentication=no)"
Source's `sshd_config` has `PasswordAuthentication no` (advertises
publickey only). Re-run the wizard, pick **SSH key** at Step 2,
paste the private key that's authorized on the source.

### "uapi: command not found" on analyze stage
Pre-M35.8 the analyze callback hard-wired the cpanel Discoverer
regardless of source kind. Fixed in commit `6664e565` — analyze
now uses `migrate.Get(job.SourceKind)`. Re-run `jabali update` if
you see this on a DA / Hestia / WHM job.

### "another migration job already owns this (source_host, source_user, source_kind)" with empty list view
A hidden draft or terminal row matched the (host, user, kind)
triple. Pre-M35.8 collision check matched any state. Fixed in
commit `fced6d43`: collision skips draft + done + failed +
cancelled. If still seen after deploy, manual cleanup:
`mysql -uroot jabali_panel -e "DELETE FROM migration_jobs WHERE state='draft'"`.

### DirectAdmin "Unrecognized arguments [info]" / `da admin user.show` errors
DA scaffold was written against guessed CLI verbs that don't
exist on real DA. Rewritten against real `/usr/local/directadmin/data/users/<u>/`
file layout in commit `c92e0ed8`. Probe is `da admin` (no args);
listing reads `domains.list` + `databases` come from
`mysql --defaults-file=/usr/local/directadmin/conf/my.cnf`.

### WP "Error establishing a database connection" after migration
appconfig rewrite step had a path-validation bug (`agent.files.*`
needs absolute paths). Fixed in `63028cfc`. After update,
manifest line `appconfigs: wordpress=N joomla=N drupal=N magento=N`
should report N>0 for any docroot with a matching app file.

### Kratos 409 conflict on `jabali user password` after migration
Orphan Kratos identity from a prior destroy/rerun cycle. Fixed in
`9f83e8d0`: `CreateIdentityWithPassword` returns `ErrIdentityExisted`
+ recovered id; userops.Create + rebuildOne now reuse it. Re-run
`jabali admin rebuild-kratos` after deploying the fix.
