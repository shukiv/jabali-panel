# Security

Jabali combines built-in hardening with an external security daemon (jabali-security) for real-time threat detection.

## Built-in Hardening

- **Shell argument escaping** — all agent commands use `escapeshellarg()`
- **Path sanitization** — `PathSanitizer::clean()` on all user-supplied file paths
- **Cron command validation** — `CronCommandValidator::validate()` against an allowlist
- **Input validation** — Livewire public properties validated before forwarding to agent
- **CSRF protection** — enforced on all state-changing requests
- **Impersonation tokens** — one-time, IP-bound, 5-minute expiry, SHA-256 hashed
- **DKIM keys** — encrypted at rest via Laravel's `encrypted` cast
- **Webhook secrets** — encrypted at rest, hidden from JSON serialization
- **CSP, HSTS, X-Frame-Options** — on all panel responses

## SSH Shell Isolation

Shell users run inside nspawn containers managed by jabali-isolator:

- Containers created on-demand when users SSH in
- Auto-stop after 5 minutes of idle time
- 256 MB memory limit per container
- Home directory ownership: `user:user 755` for shell users, `root:user 750` for SFTP-only
- Login shell set to jabali-shell via `chsh`, which uses `nsenter` to enter the container

## jabali-security Daemon

Separate Python daemon providing:

- **Brute-force protection** — monitors auth logs, auto-blocks repeat offenders
- **WAF** — ModSecurity with OWASP Core Rule Set (installed from GitHub, not apt)
- **WebShield** — bot filtering (UA-based, on by default) and rate limiting (opt-in)
- **CrowdSec** — community threat intelligence with local decisions
- **Malware scanning** — ClamAV + YARA rules, on-demand and scheduled
- **File integrity monitoring** — detects unauthorized changes
- **Quarantine** — isolates infected files for review
- **Attack mode** — panic button that enables aggressive rate limiting and thresholds

Communication: Unix socket at `/run/jabali-security/jabali-security.sock`, authenticated via `X-API-Key`.

## Per-User Isolation

- Separate Linux accounts per user
- Per-user PHP-FPM pools with dedicated sockets
- Per-user Redis ACL (isolated keyspaces for WordPress caching)
- Per-user nginx page cache at `/home/{user}/cache/nginx/`
- Per-user log directories
