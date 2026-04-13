# Jabali Security

Standalone, event-driven security suite for Linux shared hosting.

> **Cross-repo context**: Read `~/projects/jabali-shared/CONTEXT.md` before making changes that touch the panel integration. Update the change log there when modifying API endpoints or the Filament plugin.

## Stack

- Python 3.12+, asyncio-based daemon
- YARA-X for signature scanning (not legacy yara-python)
- ClamAV optional backend (clamd socket, auto-detected)
- aiohttp for REST API
- click for CLI
- aiosqlite for incident storage
- Pydantic for data models

## Project Layout

- `daemon/` — CLI entry point + async daemon server
- `lib/` — Core library modules (scanners, watcher, scoring, etc.)
- `lib/ufw/` — UFW firewall management (rule CRUD, enable/disable, app profiles)
- `lib/webshield/` — WebShield bot filtering + GeoIP blocking (independent nginx configs)
- `api/` — REST API (aiohttp), routes split into `api/routes/` by domain
- `etc/webshield/` — Challenge page HTML + njs validator script
- `rules/` — YARA-X rule files (.yar)
- `etc/` — Config example + systemd service
- `tests/` — pytest test suite + external security test script (`test_security.sh`)
- `debian/` — .deb packaging

## Conventions

- Config format: KEY="value" (gniza4linux pattern)
- Config path: /etc/jabali-security/jabali-security.conf
- Data path: /var/lib/jabali-security/
- Log path: /var/log/jabali-security/
- Quarantine: /var/security/quarantine/{user}/
- API: 127.0.0.1:9876 with X-API-Key auth
- No shell=True in subprocess calls — always use list args
- Use `import yara_x` not `import yara`
- YARA-X API: `yara_x.Compiler()`, `yara_x.Scanner(rules)`, `results.matching_rules`
- Use `trash` instead of `rm`/`rmdir`
- PHP-FPM isolation is managed by jabali-isolator (systemd-nspawn containers), not by jabali-security
- jabali-security monitors and protects: file scanning, process monitoring, brute-force detection, WAF events, GeoIP blocking, WebShield bot filtering
- SSH management is handled by the panel, NOT jabali-security
- GeoIP writes to `/etc/nginx/jabali/cache-zones/geoip.conf` (http-level) and `/etc/nginx/jabali/includes/geo.conf` (server-level)
- Use `maxminddb` library for GeoIP database lookups
- WebShield requires manual nginx include + reload after install
- Default watch paths: `/home/*/public_html`, `/home/*/domains/*/public_html`, `/home/*/tmp`

## Running

```
uv run jabali-security start --foreground
uv run pytest tests/
```
