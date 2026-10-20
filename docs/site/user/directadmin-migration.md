# Migrating from DirectAdmin (User)

If you are moving from DirectAdmin to Jabali, the operator handles the per-user backup ingest. Operator-side details: [DirectAdmin Migration](../admin/directadmin-migration.md).

## What gets moved

- Linux home directory.
- Web domains and subdomains. DirectAdmin's per-subdomain document roots are translated to subdomain Domain rows (separate vhosts).
- DNS zones (translated from DirectAdmin's BIND-style format).
- MySQL databases, each with its own database user and a new password. WordPress, Joomla, Drupal and Magento configurations are pointed at the new database and user. Your old database users and their passwords do not carry over.
- Email accounts in Stalwart — **passwords reset** (DirectAdmin uses Exim+Dovecot password hash formats Stalwart cannot import).
- Forwarders, autoresponders, catch-all settings.
- Cron jobs (those passing the [Cron allowlist](./cron-jobs.md)).

## What does not get moved

- TLS certificates — reissued by Let's Encrypt after the per-domain SSL toggle.
- ModSecurity per-domain rules — Modsec is removed in Jabali (see [Removed Features](../removed-features.md)); equivalent protection is provided by the server-side AppSec WAF.
- CSF allowlists — carry over manually to the administrator (CrowdSec uses a different model).

## Source-side prep

Before the operator pulls your backup:

1. Use the DirectAdmin user-level "Create / Restore Backups" to generate a per-user tarball.
2. Note the per-domain TLS certificates you currently use and the SSL providers (you will reissue via Let's Encrypt on Jabali, but you may want a record of the prior expirations).
3. Note any custom Apache `.htaccess` rules you depend on so you can verify they translate to nginx rules after the migration.

## After your account is restored

1. **Log in** — set a new panel password under Profile.
2. **Add SSH keys** under [SSH Keys](./ssh-keys.md) for SFTP access.
3. **Collect email passwords** from the administrator and pass them on to mailbox owners.
4. **Repoint DNS** at the registrar when the migration is fully verified.
5. **Issue SSL** per domain.

## Gotchas

- **Apache directives that depend on `mod_php`** — these do not translate. PHP runs via FPM under nginx; configure PHP behavior under [PHP Settings](./php-settings.md) instead.
- **DirectAdmin's per-user CGI** — CGI scripts (Perl, etc.) are not exposed to nginx vhosts by default. If you depend on CGI, contact the administrator before cutting over.
- **Per-user MySQL `root`** — DirectAdmin lets the reseller see a `root`-like database manager. Jabali does not; you have your own DB users with privileges only on your own databases. Use the [Databases](./databases.md) SSO into phpMyAdmin for the equivalent UI.
