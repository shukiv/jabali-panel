# Migrating From Another Panel

`/jabali-admin/migrations`.

## Supported sources

A source can be ingested from an **uploaded archive** or, for the control-panel
sources, pulled **live over SSH** (the panel reads the source with the vendor's
own read-only CLI, then rebuilds destination-side).

| Source | Format | Status |
|---|---|---|
| **cPanel** | `cpmove-<user>.tar.gz` or live SSH | ✅ — each database gets its own database user with a new password, and common app configs are pointed at it. With **Carry over source passwords**, the source's MySQL users and password hashes come across too. See [cPanel migration](admin/cpanel-migration.md). |
| **DirectAdmin** | DA user backup (`user.<reseller>.<user>.tar.zst`, `.tar.gz` or `.tar`) restored with `jabali migrate restore --directadmin`, or live SSH | ✅ — see [DirectAdmin](#directadmin) below. |
| **Hestia** | Hestia `v-backup-user` tarball (`<user>.<ts>.tar[.gz]`) or live SSH | ✅ — files, DBs, DNS (incl. SRV + CAA), and apps (e.g. Nextcloud) restore end-to-end; the account Contact Name (FNAME/LNAME) carries onto the user. Mail is the Exim→Stalwart subset (see below). |
| **WHM** | WHM-level dump (multiple `cpmove`s in one) | 🟡 — same caveats as cPanel per-user. |
| **Plesk** | live SSH (subscriptions) | ✅ — reads databases, DNS, and reseller customers + service plans via `plesk bin` / wp-toolkit read-only CLI, then rebuilds destination-side. |
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

- MySQL users: each restored database gets its own user, `<user>_<database>`, with a new password, and WordPress, Joomla, Drupal, Magento and ITFlow configs are pointed at it. With **Carry over source passwords** (`--preserve-source-state`), the source's MySQL users are recreated with their original password hash, so app configs keep their credentials.
- Email accounts: cPanel uses Dovecot+Exim, Jabali uses Stalwart. Passwords reset to a generated value (printed in the migration report) — operator must communicate the new passwords to mailbox owners, or set "force first-login password reset" so users self-serve.
- DKIM keys: imported.
- DNSSEC: not migrated automatically (key formats differ); re-enable per-domain in Jabali.

### DirectAdmin

- See `docs/user/directadmin-migration.astro` for the source-side prep of a live SSH pull.

**Offline restore (CLI):** a DirectAdmin user backup, as DirectAdmin's own
backup pages and `da admin-backup` write it (`user.<reseller>.<user>.tar.zst`
by default; `.tar.gz` and plain `.tar` work too), restores without SSH access
to the source:

```
jabali migrate restore --directadmin --file /path/user.<reseller>.<user>.tar.zst \
  --target-email <email> --target-password <pw>
```

- The source account is read from the backup itself (`backup/user.conf`), so
  the file can have any name. `--source-user` is optional; one that names
  another account is refused.
- Resume, `--retry-from-scratch`, the disk preflight and the `degraded` result
  work as for Hestia below.
- **Carried:**
  - the home directory;
  - every domain's `public_html` and `public_ftp`;
  - each database with a new database user, and WordPress, Joomla, Drupal,
    Magento and ITFlow configs pointed at it, including a second WordPress in a
    subdirectory of a docroot (a shop at `public_html/shop/`);
  - each domain's DNS zone and SSL certificate;
  - mail accounts with their mailboxes, and the account's own mailbox;
  - forwarders to full addresses;
  - cron jobs;
  - SSH keys.
- With **Carry over source passwords** (`--preserve-source-state`), the
  source's MySQL users come across with their `mysql_native_password` hashes,
  and the domain certificates are installed.
- **Subdomains:** a DirectAdmin subdomain is served from a folder of its
  parent's site. Here it gets its own folder,
  `domains/<sub>.<domain>/public_html`, and is created as a **Web Domain
  only**. Its DNS records stay in the parent domain's zone.
- **Mail stays off for a domain** with no mail account, forwarder or mailbox on
  the source. The account's own mailbox is then not carried either, since it
  only holds DirectAdmin's notices.
- **Not carried, and listed in the migration report:**
  - FTP accounts other than the main one;
  - autoresponders and vacation messages;
  - forwarders to a local name, and pipe or `:fail:` rules;
  - DirectAdmin's access logs and statistics;
  - the Redis dump and WP-CLI's download cache.
  Recreate the ones you need in the panel.
- Cron jobs keep their command lines. The source's `PATH` and jailed `SHELL`
  are dropped, so a job that ran a source-specific PHP binary
  (`/usr/local/php81/bin/php`) needs its path updated.

### Hestia

- BIND zones translated to PowerDNS schema rows — A/AAAA/CNAME/MX/TXT/**SRV** (priority in the pdns prio column) / **CAA** all import; the source SOA is dropped (pdns generates its own).
- Hestia's `web/<domain>/public_html` docroot layout is handled for both the file split and the app-config scan (differs from the native `domains/<domain>/public_html`).
- Resource types are preserved per source facet: Hestia lists web dirs and DNS zones independently, so a web dir with no source zone imports as a **Web Domain only** (`dns_disabled`, no managed PowerDNS zone), a DNS zone with no web dir imports as a **DNS Zone only** (`web_disabled`, no nginx vhost/PHP), and a domain in both imports full-facet. A restored DirectAdmin backup does the same for subdomains (see below). Other importers (cPanel, a live DirectAdmin pull, Plesk, CloudPanel, CyberPanel) create every domain full-facet unchanged.
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
