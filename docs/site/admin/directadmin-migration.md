# DirectAdmin Migration

The DirectAdmin ingest path. Status: production-supported.

## Source archive

DirectAdmin's standard backup tarball (`backup-Jan-01-2026-12_00.tar.gz`). Produce on the source host via:

```bash
da backup-all
# or per-user:
da backup-user <username>
```

The resulting tarballs land under `/home/admin/admin_backups/`.

## What gets migrated

| Asset | Behavior |
|---|---|
| User account | Recreated under the destination panel. |
| Home directory | Copied to `/home/<user>/`. |
| Domains and subdomains | Created as Domain rows. DirectAdmin's `subdomain` directories are translated to subdomain Domain rows, not aliases. |
| DNS zones | Translated from DirectAdmin's BIND-style files to PowerDNS rows. |
| MySQL databases | Restored. Each database gets its own database user, `<user>_<database>`, with a new password, and WordPress, Joomla, Drupal, Magento and ITFlow configs are pointed at it. DirectAdmin's own database users and their passwords are not carried over. |
| Email accounts | Created in Stalwart; passwords reset (DirectAdmin uses Exim+Dovecot password hash formats Stalwart cannot import). |
| Forwarders, autoresponders, catch-all | Translated to Stalwart equivalents. |
| Cron jobs | Translated to systemd-user timers if the command is on the [Cron allowlist](../cron.md). |
| FTP accounts | Mapped to SFTP via `Match Group`; passwords do not transfer. |

## Source-side prep

For best results:

1. On the source host, ensure the user is not actively writing during the backup window (file consistency).
2. Capture the bind zones (`/var/named/<domain>.db`) — DirectAdmin's BIND format is what the panel parses.
3. Note the per-domain SSL certificates being used; SSL is not migrated and will be reissued on the destination.

## Operator workflow

Identical to the [cPanel pipeline](./cpanel-migration.md): upload, analyze, restore, communicate generated passwords, repoint DNS, issue SSL.

## Limitations

- **Modsecurity rules** — DirectAdmin's per-user Modsec rules are not migrated (Modsec is removed; see [Removed Features](../removed-features.md)). Equivalent protection is provided by [AppSec](./appsec.md) at the server level.
- **CSF allowlists** — not migrated; carry over manually into [CrowdSec Allowlists](./crowdsec-allowlists.md).
- **DirectAdmin Reseller** — Jabali has no reseller construct; reseller-owned accounts migrate as individual users.

## Per-user migration vs full-server

For one-off per-user moves, use the per-user backup. For server-cutover migrations, produce a backup per user with `da backup-user`, batch-upload to the destination, and run the pipeline against each.

## Audit

Per-phase audit rows are emitted; per-domain creation produces one `domain.create` row per domain.
