# Changelog

All notable changes to Jabali Panel will be documented in this file.

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
