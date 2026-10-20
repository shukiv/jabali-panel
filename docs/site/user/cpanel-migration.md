# Migrating from cPanel (User)

If you are moving your account from a cPanel host to Jabali, this is the typical path. Operator-side details are in [cPanel Migration](../admin/cpanel-migration.md).

## What gets moved

The administrator handles the import. Once your account is restored on the new panel, the following are in place:

- Your Linux home directory, with files at their original paths.
- Hosted domains.
- DNS zones (translated to PowerDNS records).
- MySQL databases, each with its own database user. WordPress, Joomla, Drupal and Magento configurations are pointed at the new database and user. If the administrator carries over source passwords, your original MySQL users come along too, with their passwords.
- Email accounts — recreated in Stalwart. **Email passwords are reset** because cPanel's Dovecot password hash format cannot be imported into Stalwart. You receive new generated passwords; communicate them to mailbox owners.
- DKIM keys.
- Cron jobs (those whose commands pass the [Cron allowlist](./cron-jobs.md)).
- SFTP — old cPanel FTP credentials do **not** transfer. Add your SSH public key under [SSH Keys](./ssh-keys.md).

## What does not get moved

- Spamassassin per-mailbox rules (Stalwart's spam filter is server-side; per-mailbox tuning is currently operator-controlled).
- Mailing-list configurations (Mailman is not part of the panel).
- Apache `.htaccess` directives that depend on `mod_php` or other Apache-only modules. `mod_rewrite` directives translate; review your `.htaccess` files after the migration to confirm rewrite rules still work as expected.
- TLS certificates — the panel reissues via Let's Encrypt automatically.

## After your account is restored

1. **Log in** — your panel password is set by the administrator and communicated separately. Set a new one under Profile → Security → Change Password.
2. **SFTP** — add at least one SSH public key under [SSH Keys](./ssh-keys.md) before you try to upload anything.
3. **Email** — collect the generated mail passwords from the administrator and pass them on, or have mailbox owners use the recovery flow if the administrator enabled it for your domain.
4. **DNS** — when the administrator confirms the migration is complete, update your domain's nameservers (or A records) at the registrar to point to the new panel. Wait for propagation before letting users hit the new endpoint.
5. **Verify** — visit each migrated site, log in to each mailbox, run a test query against each database. Open a support ticket immediately if anything is missing.

## Gotchas

- **WordPress site URL** — if the source cPanel served `https://oldhost.example.com/path/` and you cut over to `https://example.com/`, run `wp search-replace` on the migrated database to update the site URL. The migration pipeline does not do this; only you know your final URL.
- **Email signatures with absolute URLs** — links pointing at the old domain may need updating in mail client signatures.
- **Cron jobs that referenced `php` at an absolute path** — `/opt/cpanel/ea-php80/root/usr/bin/php` does not exist on Jabali. Re-edit the cron job to use the unqualified `php` (the per-user FPM pool resolves it via the per-user `$PATH`).
