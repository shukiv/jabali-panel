# Security Policy

## Supported Versions

| Version | Supported |
|---------|-----------|
| 0.9.x (RC) | Yes |

## Reporting a Vulnerability

If you discover a security vulnerability in Jabali Panel, please report it responsibly:

1. **Do not** open a public GitHub issue for security vulnerabilities
2. Email security details to the maintainers
3. Include steps to reproduce, affected versions, and potential impact
4. You will receive an acknowledgment within 48 hours

## Security Architecture

Jabali Panel handles privileged operations (DNS, SSL, mail, user isolation, backups, migrations) and follows defense-in-depth principles:

### Authentication & Session Security

- Admin and user panels use separate authentication guards (`admin`, `web`)
- Two-factor authentication (TOTP) supported for all accounts
- Admin impersonation uses one-time tokens bound to IP address, with POST + CSRF for session termination
- Login timing is constant regardless of whether the user exists (prevents user enumeration)
- Sessions are encrypted when `SESSION_ENCRYPT=true` is set in production
- Secure cookies enforced when `SESSION_SECURE_COOKIE=true` is set in production

### Input Validation & Injection Prevention

- All shell command arguments are escaped with `escapeshellarg()` to prevent OS command injection
- Blade templates use `{{ }}` (escaped) by default; any `{!! !!}` raw output is sanitized through Laravel's HTML sanitizer
- Database queries use Eloquent ORM and parameterized bindings throughout
- File paths are validated with `realpath()` and directory prefix checks to prevent path traversal

### TLS & Network Security

- Migration API calls (WHM/cPanel) enforce TLS certificate verification by default
- To connect to servers with self-signed certificates during migration, set `JABALI_IMPORT_INSECURE_TLS=true` in `.env`
- HSTS headers are sent in production over HTTPS
- Content Security Policy (CSP) is enforced on all panel responses

### API Security

- Internal API endpoints validate requests originate from localhost or carry a valid `JABALI_INTERNAL_API_TOKEN`
- WordPress page-cache API uses SHA-256 HMAC verification of `AUTH_KEY`
- PhpMyAdmin SSO tokens are single-use and cached with automatic expiry
- Error responses in production return generic messages; details are logged server-side

### Data Protection

- DKIM private keys are encrypted at rest using Laravel's `encrypted` cast (requires `APP_KEY`)
- Webmail SSO tokens are stored in a restricted directory (`/var/lib/jabali/sso-tokens/`) with `0600` permissions and 5-minute expiry
- Backup downloads are restricted to allowed directory prefixes (`/home/`, `/var/backups/`, `storage/app/backups/`)

### Security Headers

All panel responses include:

- `Content-Security-Policy` - restricts resource loading to same-origin
- `X-Content-Type-Options: nosniff`
- `X-Frame-Options: SAMEORIGIN`
- `Referrer-Policy: strict-origin-when-cross-origin`
- `Permissions-Policy` - disables camera, microphone, geolocation
- `Strict-Transport-Security` - enabled in production over HTTPS

### Environment Variables

| Variable | Purpose | Default |
|----------|---------|---------|
| `JABALI_INTERNAL_API_TOKEN` | Shared token for internal API calls from non-localhost | (unset) |
| `JABALI_IMPORT_INSECURE_TLS` | Disable TLS verification for migration API calls | `false` |
| `SESSION_ENCRYPT` | Encrypt session data at rest | `false` |
| `SESSION_SECURE_COOKIE` | Send session cookies only over HTTPS | `false` |

## Security Audit History

### v0.9-rc86 (2026-03-11)

Pre-1.0 security audit covering authentication, authorization, input validation, and configuration. See CHANGELOG.md for the full list of fixes.
