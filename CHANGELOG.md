# Changelog

All notable changes to Jabali Panel will be documented in this file.

## [0.9-rc116] - 2026-03-13

### Fixed

- **Mail Server Hostname incorrect after install** — The installer wasn't setting `mail_hostname` in DnsSetting, so Server Settings > Email showed `mail.<system-hostname>` (e.g., `mail.web03.REDACTEDDOMAIN.com`) instead of `mail.<root-domain>`. Now explicitly sets `mail_hostname` to `mail.{root_domain}` during install. (Closes #26)

## [0.9-rc115] - 2026-03-13

### Fixed

- **DNS zone not active after install** — The `dns.sync_zone` call after DKIM generation was missing the `records` parameter, causing the zone file to be overwritten with only SOA/NS records (no A, MX, TXT, etc.). Now passes full records from the database, matching the "Rebuild Zone" behavior. (Closes #25)

## [0.9-rc114] - 2026-03-12

### Fixed

- **SMTP connection drops on ports 25, 587, 465** — Set `inet_interfaces=all` in Postfix (Debian 13 defaults to `loopback-only`, blocking external connections). Set `myhostname` to the configured FQDN so the SMTP banner shows the correct domain instead of the system hostname. Fixed debconf pre-seed to use `$SERVER_HOSTNAME` instead of `hostname -f`. (Closes #23)
- **SMTPS port 465 not configured** — Added `smtps` service to Postfix `master.cf` for legacy mail clients using implicit TLS.

## [0.9-rc113] - 2026-03-12

### Changed

- **Debian 13 only** — Dropped support for Debian 11 (Bullseye) and Debian 12 (Bookworm). Jabali now requires Debian 13 (Trixie) with Dovecot 2.4.

### Removed

- Dovecot 2.3 configuration paths in the installer and `jabali:configure-dovecot-acl` command
- Version detection logic for Dovecot 2.3 vs 2.4
- Legacy `dovecot-sql.conf.ext` and `dovecot-dict-sql.conf.ext` external config file support

## [0.9-rc103] - 2026-03-12

### Added

- **Shared Folders** - New "Shared Folders" tab on the Email page for managing IMAP folder sharing between mailboxes. Users can share individual folders with other mailboxes on the same domain using four permission levels: Read, Read & Write, Full Access, and Admin. Recipients automatically discover shared folders via IMAP. Backed by Dovecot ACL plugin with vfile backend and SQL-based shared mailbox discovery (`user_shares` table).
- **Dovecot ACL plugin configuration** - Dovecot is now configured with the ACL plugin and shared namespaces out of the box. New installations get this automatically; existing installations can run `php artisan jabali:configure-dovecot-acl` to enable the feature.
- **Shared folders translations** - All shared folder UI strings are translated in 7 languages (en, es, ar, fr, ru, pt, he).

### Deployment Notes

1. For existing installations, run `php artisan jabali:configure-dovecot-acl` to configure Dovecot ACL support and create the `user_shares` table
2. Run `php artisan migrate` to create the shared folder permissions table
3. Dovecot will be automatically restarted after ACL configuration

## [0.9-rc101] - 2026-03-12

### Added

- **IMAP Sync** - New Migration tab for migrating mail from external IMAP servers. Supports single mailbox sync and bulk migration (multiple mailboxes at once). Uses `imapsync` as the backend engine with PHP `imap_open()` for connection testing. Includes sync history table with status tracking, retry, and cancel actions. (`app/Filament/Jabali/Pages/ImapSync.php`, `app/Jobs/RunImapSync.php`, `app/Models/ImapSyncTask.php`)
- **Mail subdomain redirect** - Visiting `mail.domain.ext` in a browser now redirects to webmail (Roundcube) instead of showing the panel login. Autoconfig/autodiscover paths are excluded so mail client auto-discovery still works. (`app/Http/Middleware/MailSubdomainRedirect.php`)
- **Installer --debug flag** - Verbose output is now suppressed by default with an animated spinner. Pass `--debug` to see full command output for troubleshooting.
- **Server hostname DNS record** - When a domain matching the server's base domain is created (e.g., `example.com` on server `web02.example.com`), an A record for the hostname subdomain is automatically added. (`app/Observers/DomainObserver.php`)

### Fixed

- **Domain setup during install** - The installer now properly calls the agent's `domainCreate()` and sets the correct user-scoped `document_root` path instead of `/var/www/html`. This fixes the issue where the base domain had no vhost or web directory after install, and users couldn't re-add it through the panel. (Closes #16)
- **Debian 13 detection** - Debian 13 (trixie) is now correctly identified as a stable release instead of "testing/unstable". The OS detection logic now checks `VERSION_ID` first. (Closes #21)
- **Dovecot MySQL authentication** - Dovecot was configured for SQLite but the app uses MySQL. Fixed to use Dovecot 2.4 MySQL block format. Also fixed empty password issue by reading credentials from `/root/.jabali_db_credentials` instead of `.env` (which doesn't exist yet at configure_mail time).
- **IMAP test connection hanging** - Replaced `imapsync --dry` with PHP `imap_open()` for connection testing, since imapsync validates both source and destination hosts even in dry-run mode.
- **IMAP folder checkboxes not appearing** - Replaced conditional schema building with `->visible()` closures for dynamic field visibility in Livewire/Filament forms.
- **Installer .env path** - Fixed `$INSTALL_DIR` undefined variable references, replaced with `$JABALI_DIR`.
- **Installer uninstall hanging** - Wrapped `apt-get autoremove` in error-tolerant block to prevent `set -e` from killing the script.
- **Refresh button redirect** - Fixed Refresh button redirecting to Livewire update endpoint.

### Changed

- **Installer output** - All verbose command output (apt, npm, composer) is now suppressed with an animated spinner. Failures show the last 20 lines of output for debugging.
- **DNS Records table** - Default pagination changed to 25 rows per page.

## [0.9-rc86] - 2026-03-11

### Security

Pre-1.0 security audit remediation. This release addresses vulnerabilities across authentication, authorization, input validation, and configuration.

#### Critical

- **Fix command injection in disk quota check** - `CheckDiskQuotas` now escapes usernames with `escapeshellarg()` before passing to shell commands (`app/Console/Commands/CheckDiskQuotas.php`)
- **Fix command injection in file integrity check** - `CheckFileIntegrity` now escapes all `$basePath` interpolations with `escapeshellarg()` (`app/Console/Commands/CheckFileIntegrity.php`)
- **Fix CSRF on impersonation stop** - Changed `/impersonate/stop` from GET to POST with CSRF token. The impersonation banner now uses a form instead of a plain link (`routes/web.php`, `ImpersonationController.php`, `JabaliPanelProvider.php`)
- **Encrypt DKIM private keys at rest** - Added `'dkim_private_key' => 'encrypted'` cast to `EmailDomain` model. Existing plaintext values require a one-time migration (`app/Models/EmailDomain.php`)
- **Enable TLS verification for migration APIs** - WHM and cPanel API calls now verify SSL certificates by default. Set `JABALI_IMPORT_INSECURE_TLS=true` in `.env` to opt out for self-signed certificates (`app/Services/Migration/WhmApiService.php`, `app/Services/Migration/CpanelApiService.php`, `config/app.php`)

#### High

- **Move webmail SSO tokens out of /tmp** - SSO token files now stored in `/var/lib/jabali/sso-tokens/` with `0600` permissions and `LOCK_EX` atomic writes instead of world-readable `/tmp` (`routes/web.php`, `install.sh`)
- **Upgrade page-cache secret to SHA-256** - WordPress plugin API secret verification upgraded from `md5()` to `hash('sha256', ...)`. Both server and bundled WordPress plugin updated simultaneously (`routes/api.php`, `resources/wordpress/jabali-cache/jabali-cache.php`)
- **Restrict admin backup download paths** - `adminDownload()` now validates that backup paths are under `/home/`, `/var/backups/`, or `storage/app/backups/` before serving files (`app/Http/Controllers/BackupDownloadController.php`)
- **Sanitize terms and policy HTML** - Raw HTML output in terms/policy views now filtered through `sanitizeHtml()` (`resources/views/terms.blade.php`, `resources/views/policy.blade.php`)

#### Medium

- **Verify impersonation session state** - `ImpersonationController::stop()` now checks that `session('impersonated_by')` exists before clearing session data (`app/Http/Controllers/ImpersonationController.php`)
- **Prevent user enumeration via timing** - Admin login performs a constant-time dummy `Hash::check()` when the user is not found, preventing timing-based email enumeration (`app/Filament/Admin/Pages/Auth/Login.php`)
- **Hide internal error details** - Page-cache API endpoints now return generic error messages to callers and log detailed errors server-side (`routes/api.php`)

#### Other

- **Fix XSS in impersonation banner** - User name and username are now HTML-escaped with `e()` in the impersonation notice (`app/Providers/Filament/JabaliPanelProvider.php`)
- **Fix XSS in OpenGraph meta tags** - Site name, title, and description are now HTML-escaped in meta tag attributes (`app/Providers/Filament/JabaliPanelProvider.php`)
- **Use config() for TLS flag** - Migration services read `JABALI_IMPORT_INSECURE_TLS` via `config('app.import_insecure_tls')` instead of `env()` directly, ensuring compatibility with config caching

### Breaking Changes

- **Impersonation stop route** is now POST instead of GET. Any external links or bookmarks to `/impersonate/stop` will no longer work.
- **WordPress page-cache plugin** must be updated to match the new SHA-256 secret verification. The bundled plugin in `resources/wordpress/jabali-cache/` is already updated. Deployed sites should update the plugin simultaneously with the panel.
- **DKIM private keys** will need a one-time encryption migration for existing data. New keys are encrypted automatically.
- **SSO token directory** changed from `/tmp/` to `/var/lib/jabali/sso-tokens/`. The installer creates this directory automatically.

### Deployment Notes

1. Run `php artisan config:clear` after updating `.env` with any new variables
2. Create SSO token directory: `mkdir -p /var/lib/jabali/sso-tokens && chown www-data:www-data /var/lib/jabali/sso-tokens && chmod 700 /var/lib/jabali/sso-tokens`
3. If using DKIM, run a migration to encrypt existing private keys
4. Update the jabali-cache WordPress plugin on all managed sites
5. Run `php artisan config:cache` to cache the new configuration
