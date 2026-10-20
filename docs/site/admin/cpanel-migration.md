# cPanel Migration

The cPanel ingest path. Status: production-supported.

## Source archive

The standard `cpmove-<user>.tar.gz` produced by cPanel's `Backup Wizard` or `pkgacct`. Either format works.

For server-wide migration, produce a separate cpmove per account or feed a WHM-level dump through the [WHM pipeline](./whm-migration.md).

## What gets migrated

| Asset | Behavior |
|---|---|
| Linux user account | Recreated under the destination panel; UID may differ. |
| Home directory contents | Copied to `/home/<user>/`. |
| Hosted domains | Created as panel Domain rows; vhosts rendered by the reconciler. |
| Subdomains | Created as Domain rows (or aliases, per heuristic on the cPanel `addon_domains` map). |
| DNS zones | Translated to PowerDNS schema rows. |
| MySQL databases | Restored via `mysql` against panel-managed MariaDB. |
| MySQL users | Each restored database gets its own database user, `<user>_<database>`, with a new password, and WordPress, Joomla, Drupal, Magento and ITFlow configs are pointed at it. With **Carry over source passwords** on (`--preserve-source-state` on the CLI), the source's MySQL users are also recreated with their original password hash and their grants on the restored databases, so app configs keep their credentials. The import never takes over a MariaDB account that already exists on this server, other than one it made for this account: such a database gets no user of its own, such a source user is not recreated, and the migration report lists each one. |
| Email accounts | Created in Stalwart. Cleartext passwords are not preserved (cPanel uses Dovecot/CRAM-MD5 which Stalwart's Argon2id store rejects); generated passwords are printed in the migration report for out-of-band delivery. |
| DKIM keys | Imported as-is so mail flow is uninterrupted. |
| Mailing lists | Not migrated — Mailman is not currently in the panel; the affected accounts surface in the migration report. |
| FTP accounts | Mapped to SFTP via `Match Group` + the user's SSH key vault. cPanel FTP-only passwords do not transfer (FTP is plaintext; the panel does not host FTP). |
| Cron jobs | Translated into systemd-user timers if the command passes the [Cron allowlist](../cron.md). Disallowed commands surface in the report; the operator may add them to the allowlist and re-run. |
| SSL certificates | Re-issued by the reconciler via Let's Encrypt; the cPanel cert is not migrated (paths and renewal hooks differ). |

## What is not migrated

- WHMCS / Reseller-only configuration (Jabali has no reseller construct).
- cPanel "Backup Manager" snapshots (use the new panel's [Backups](./backups.md) going forward).
- Spamassassin per-mailbox rules (Stalwart handles spam scoring server-side).
- Apache `.htaccess` files referencing modules nginx does not have (mod_rewrite is supported via the nginx vhost template; mod_php directives are dropped).

## Operator workflow

1. Produce the `cpmove-<user>.tar.gz` on the cPanel host.
2. Upload to `/jabali-admin/migrations` (web) or SCP to `/var/lib/jabali/migrations/incoming/`.
3. Click **Analyze** on the row. Review the report.
4. Click **Restore**.
5. After completion, communicate generated mail passwords to mailbox owners (or set a force-first-login password-reset policy under [Server Settings](./server-settings.md) so they self-serve).
6. Update DNS at the registrar to point to the new panel.
7. Issue SSL via the per-domain SSL toggle.

## Audit

Each phase emits an audit row; the per-domain creation also writes one `domain.create` row per domain.
