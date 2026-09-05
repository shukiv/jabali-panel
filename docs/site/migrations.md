# Migrating From Another Panel

`/jabali-admin/migrations`. M15 (in part) + ongoing pipeline work.

## Supported sources

A source can be ingested from an **uploaded archive** or, for the control-panel
sources, pulled **live over SSH** (the panel reads the source with the vendor's
own read-only CLI, then rebuilds destination-side).

| Source | Format | Status |
|---|---|---|
| **cPanel** | `cpmove-<user>.tar.gz` or live SSH | ✅ — preserves MySQL users + bcrypt password hashes (so migrated apps keep working); see "preserve cpanel MySQL users + password hashes" commit. |
| **DirectAdmin** | DA backup tarball or live SSH | ✅ — see `docs/user/directadmin-migration.astro` (legacy) for source-side prep notes. |
| **Hestia** | Hestia `v-backup-user` tarball (`<user>.<ts>.tar[.gz]`) or live SSH | ✅ — files, DBs, DNS (incl. SRV + CAA), and apps (e.g. Nextcloud) restore end-to-end; the account Contact Name (FNAME/LNAME) carries onto the user. Mail is the Exim→Stalwart subset (see below). |
| **WHM** | WHM-level dump (multiple `cpmove`s in one) | 🟡 — same caveats as cPanel per-user. |
| **Plesk** | live SSH (subscriptions) | ✅ (GH #429) — reads databases, DNS, and reseller customers + service plans via `plesk bin` / wp-toolkit read-only CLI, then rebuilds destination-side. |
| **CloudPanel** | live SSH (site users) | ✅ |
| **CyberPanel** | live SSH (websites) | ✅ |
| **Jabali → Jabali** | live SSH (accounts) | ✅ — move accounts between Jabali servers. |

## Workflow

1. Either upload an archive to `/jabali-admin/migrations` (or `scp` to `/var/lib/jabali/migrations/incoming/`), **or** point the wizard at a **live SSH source** (host, port, credentials) for the control-panel importers.
2. The pipeline runs four phases:
   - **Analyze** — inspect the archive, list users / domains / DBs / mailboxes / DNS zones / cron jobs.
   - **Fix-perms** — apply chown / chmod normalisations expected by Jabali's per-user pool layout.
   - **Validate** — DB password hashes parseable, DNS zone files valid, mail accounts consistent.
   - **Restore** — create the panel user, ingest each domain / DB / mailbox / cron, hand off to the reconciler.
3. Watch progress at `/jabali-admin/migrations/<id>`.

## Per-source notes

### cPanel

- MySQL passwords are bcrypt in cPanel ≥ 11.96; Jabali stores the bcrypt hash directly in MariaDB so user apps keep authenticating without password reset.
- Email accounts: cPanel uses Dovecot+Exim, Jabali uses Stalwart. Passwords reset to a generated value (printed in the migration report) — operator must communicate the new passwords to mailbox owners, or set "force first-login password reset" so users self-serve.
- DKIM keys: imported.
- DNSSEC: not migrated automatically (key formats differ); re-enable per-domain in Jabali.

### DirectAdmin

- See `docs/user/directadmin-migration.astro` for the source-side prep (run `da backup-all` etc.) before upload.

### Hestia

- BIND zones translated to PowerDNS schema rows — A/AAAA/CNAME/MX/TXT/**SRV** (priority in the pdns prio column) / **CAA** all import; the source SOA is dropped (pdns generates its own).
- Hestia's `web/<domain>/public_html` docroot layout is handled for both the file split and the app-config scan (differs from the native `domains/<domain>/public_html`).
- The account **Contact Name** (`user.conf` FNAME/LNAME) carries onto the created jabali user's name.
- Exim → Stalwart routing rules: forwards + autoresponders ported; complex Exim ACL rules need manual re-implementation.

**Offline restore (CLI):**

```
jabali migrate restore --hestiacp --file /path/<user>.<ts>.tar --source-user <user> \
  --target-email <email> --target-password <pw>
```

- A low-disk host **aborts before** staging or creating the user (disk preflight).
- Re-run the same command to **resume**; add `--retry-from-scratch` (alias `--fresh`)
  to wipe the job and re-run the whole pipeline (recreates a deleted target user).
- A restore that leaves a site returning **5xx** (a crashing app) ends `degraded`,
  not `done`; a transient `0` during nginx/FPM convergence does not degrade it.

### WHM

- Splits into per-`cpmove` jobs internally; each runs through the cPanel pipeline.

## Limitations

- Each pipeline is **"stop-the-world" for the destination user** while it runs.
- **Plesk / CloudPanel / CyberPanel / Jabali** are live-SSH-only — the panel
  reads the source over SSH; there is no offline backup-archive import for them.
- **No CSF/LFS rule translation**. CrowdSec is the IP-trust source on Jabali; carry over allowlists manually.
