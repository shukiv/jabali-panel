# Configuration Reference

## File Location

| Context | Path |
|---|---|
| Root (production) | `/etc/jabali-security/jabali-security.conf` |
| User (development) | `~/.config/jabali-security/jabali-security.conf` |

## Format

```
# Comments start with #
KEY="value"
```

- One key per line
- Values are quoted (double or single quotes) or unquoted
- Blank lines and `#` comment lines are ignored
- Keys are uppercase with underscores

## Managing Configuration

```bash
# View current config
jabali-security config show

# Set a value (persists to file + pushes to running daemon)
jabali-security config set KEY value

# Validate config
jabali-security config test

# Edit directly
sudo nano /etc/jabali-security/jabali-security.conf
```

See also: `etc/jabali-security.conf.example` for a fully commented template.

---

## Daemon

| Key | Type | Default | Description |
|---|---|---|---|
| `LOG_LEVEL` | string | `info` | Log verbosity: `debug`, `info`, `warning`, `error`, `critical` |
| `LOG_DIR` | path | `/var/log/jabali-security` | Directory for log files (auto-created) |
| `DATA_DIR` | path | `/var/lib/jabali-security` | Persistent data directory (SQLite DB, state files) |
| `QUARANTINE_DIR` | path | `/var/security/quarantine` | Quarantine directory for malicious files |
| `WORKERS` | int | `4` | Number of async scan workers (1-32) |

## API

| Key | Type | Default | Description |
|---|---|---|---|
| `API_SOCKET` | path | `/run/jabali-security/jabali-security.sock` | Unix domain socket path for the REST API. Created automatically by systemd `RuntimeDirectory`. Permissions: `0660 root:www-data`. |
| `API_BIND` | string | *(empty)* | TCP bind address. Empty = disabled (Unix socket only). Set to `127.0.0.1` to re-enable TCP fallback for debugging. |
| `API_PORT` | int | `9876` | TCP port for REST API (only used when `API_BIND` is non-empty). |
| `API_KEY` | string | *(auto-generated)* | API authentication key. Auto-generated on install. |

## File Watcher

| Key | Type | Default | Description |
|---|---|---|---|
| `WATCH_DIRS` | csv | `/home/*/public_html,/home/*/domains/*/public_html,/home/*/tmp` | Comma-separated directories/globs to watch recursively |

> **Note:** `/var/www` was removed from defaults to avoid watching hosting panel application directories.

## Pre-Filter

| Key | Type | Default | Description |
|---|---|---|---|
| `SCAN_EXTENSIONS` | csv | `.php,.phtml,.js,.py,.sh,.cgi,.pl,.asp,.aspx,.jsp` | Comma-separated file extensions to scan |
| `MAX_FILE_SIZE` | int | `2097152` | Maximum file size in bytes (default 2 MB). Min: 1024. |
| `SKIP_DIRS` | csv | `.git,node_modules,vendor,__pycache__,.cache` | Comma-separated directory names to skip |

## Detection

Heuristic, entropy, and YARA-X scanners are always enabled (core detection engines).

| Key | Type | Default | Description |
|---|---|---|---|
| `ENTROPY_THRESHOLD` | float | `6.0` | Entropy threshold (0.0-8.0); content above this is flagged. Normal PHP source code scores 4.5-5.5; only obfuscated/encoded content exceeds 6.0. |
| `YARA_RULES_DIR` | path | `/usr/local/jabali-security/rules` | Directory containing YARA-X rule files (`.yar`) |

## ClamAV (Optional)

| Key | Type | Default | Description |
|---|---|---|---|
| `CLAMAV_ENABLED` | string | `auto` | `auto` (detect clamd), `yes` (require), `no` (disable) |
| `CLAMAV_SOCKET` | path | `/var/run/clamav/clamd.ctl` | Path to clamd Unix socket |
| `FRESHCLAM_ON_UPDATE` | bool | `yes` | Run freshclam when `jabali-security rules update` is called |

## Scoring

| Key | Type | Default | Description |
|---|---|---|---|
| `SCORE_LOG` | int | `40` | Minimum score to create a log entry. Min: 0. |
| `SCORE_QUARANTINE` | int | `70` | Minimum score to quarantine the file. Min: 0. |
| `SCORE_SUSPEND` | int | `100` | Minimum score to suspend the hosting account. Min: 0. |

Scoring determines the action for each detected threat:

| Score Range | Action |
|---|---|
| Below `SCORE_LOG` | Ignored |
| `SCORE_LOG` to `SCORE_QUARANTINE` | Logged as incident |
| `SCORE_QUARANTINE` to `SCORE_SUSPEND` | File quarantined |
| `SCORE_SUSPEND` and above | Account suspended (if enabled) |

## Process Monitor

Process monitoring is always enabled (core detection engine).

| Key | Type | Default | Description |
|---|---|---|---|
| `PROCESS_POLL_INTERVAL` | int | `2` | Poll interval in seconds for /proc scanning (1-300) |

## Behavior Tracking

Behavior tracking is always enabled (core detection engine).

| Key | Type | Default | Description |
|---|---|---|---|
| `BEHAVIOR_TTL` | int | `300` | Time-to-live in seconds for behavior records. Min: 10. |

## Response

| Key | Type | Default | Description |
|---|---|---|---|
| `AUTO_QUARANTINE` | bool | `yes` | Automatically quarantine files exceeding `SCORE_QUARANTINE` |
| `AUTO_SUSPEND` | bool | `no` | Automatically suspend accounts exceeding `SCORE_SUSPEND` |
| `AUTO_BLOCK_IP` | bool | `no` | Automatically block attacker IP addresses |

## Notifications

| Key | Type | Default | Description |
|---|---|---|---|
| `NOTIFY_EMAIL` | string | *(empty)* | Email address for alerts (empty = disabled) |
| `NOTIFY_WEBHOOK` | string | *(empty)* | Webhook URL for alerts (empty = disabled) |
| `NOTIFY_MIN_SEVERITY` | string | `high` | Minimum severity to trigger notifications: `low`, `medium`, `high`, `critical` |

## IP Protection

SSH and HTTP brute-force detection is handled by CrowdSec. Stalwart mail server has built-in IP blocking. The whitelist below applies to all sources.

| Key | Type | Default | Description |
|---|---|---|---|
| `FIREWALL_BACKEND` | string | `auto` | `auto` (detect nftables/iptables), `nftables`, `iptables`, `none` |
| `BRUTEFORCE_WHITELIST_IPS` | csv | *(empty)* | Comma-separated IPs that are never blocked |

## WAF (ModSecurity)

| Key | Type | Default | Description |
|---|---|---|---|
| `WAF_ENABLED` | bool | `no` | Enable ModSecurity WAF audit log parsing and rule management |
| `WAF_AUDIT_LOG` | path | `/var/log/modsec_audit.log` | ModSecurity audit log file path. Path varies by installation: nginx default is `/var/log/nginx/modsec_audit.log`, Apache default is `/var/log/modsec_audit.log`. |
| `WAF_AUDIT_LOG_TYPE` | string | `serial` | Audit log type: `serial` (single file) or `concurrent` (directory-based) |
| `WAF_RULES_DIR` | path | `/usr/local/share/owasp-crs/rules` | OWASP Core Rule Set directory. Installed from GitHub (`coreruleset/coreruleset`), updated via `git pull` on `jabali-security update`. |
| `WAF_OVERRIDES_FILE` | path | `/etc/modsecurity/jabali-overrides.conf` | Jabali-managed overrides file for disabling rules |
| `WAF_CRS_AUTO_UPDATE` | bool | `no` | Auto-update OWASP CRS on rules update |
| `WAF_WEB_SERVER` | string | `auto` | Web server to reload: `auto`, `nginx`, `apache` |

## Process Killer

| Key | Type | Default | Description |
|---|---|---|---|
| `PROCESS_KILL_ENABLED` | bool | `no` | Enable proactive killing of suspicious processes |
| `PROCESS_KILL_THRESHOLD` | int | `70` | Minimum threat score to kill a process (1-100) |
| `PROCESS_KILL_MIN_UID` | int | `1000` | Minimum UID for killable processes (protects system processes) |
| `PROCESS_KILL_WHITELIST` | csv | `wp-cron.php,artisan,composer` | Command substrings to whitelist from killing |

## Malware Cleanup

| Key | Type | Default | Description |
|---|---|---|---|
| `CLEANUP_ENABLED` | bool | `no` | Enable the cleanup engine (master switch) |
| `CLEANUP_AUTO` | bool | `no` | Automatically attempt cleanup before quarantine |
| `CLEANUP_BACKUP_DIR` | path | `/var/lib/jabali-security/backups` | Directory for file backups before cleanup |
| `CLEANUP_CMS_CHECKSUMS` | bool | `yes` | Use CMS checksums for integrity verification |

## Scheduled Scan

| Key | Type | Default | Description |
|---|---|---|---|
| `SCHEDULED_SCAN_ENABLED` | bool | `no` | Enable periodic full-path scanning |
| `SCHEDULED_SCAN_INTERVAL` | int | `24` | Interval between scans in hours (1-8760) |
| `SCHEDULED_SCAN_PATHS` | csv | `/home/*/public_html` | Paths/globs to scan on schedule |

## Threat Intelligence

| Key | Type | Default | Description |
|---|---|---|---|
| `THREAT_INTEL_ENABLED` | bool | `no` | Enable threat intelligence feeds |
| `THREAT_INTEL_UPDATE_INTERVAL` | int | `6` | Feed update interval in hours (1-168) |
| `THREAT_INTEL_FEEDS` | csv | `spamhaus_drop,spamhaus_edrop,blocklist_de_all,tor_exit_nodes,malwarebazaar_recent` | Enabled feeds |
| `THREAT_INTEL_AUTO_BLOCK` | bool | `no` | Auto-block IPs found in threat intel feeds |
| `THREAT_INTEL_AUTO_BLOCK_THRESHOLD` | int | `3` | Feed matches required before auto-blocking (1-10) |

Available feeds: `spamhaus_drop`, `spamhaus_edrop`, `blocklist_de_all`, `tor_exit_nodes`, `malwarebazaar_recent`

## WebShield

| Key | Type | Default | Description |
|---|---|---|---|
| `WEBSHIELD_ENABLED` | bool | `no` | Enable WebShield nginx integration. Auto-enabled on Jabali Panel servers. |
| `WEBSHIELD_RATE_LIMITING` | bool | `no` | Enable rate limiting. Off by default (can block legitimate traffic). Auto-enabled by Attack Mode. |
| `WEBSHIELD_RATE_LIMIT` | int | `10` | Requests per second per IP when rate limiting is on (1-10000) |
| `WEBSHIELD_RATE_BURST` | int | `20` | Burst size above rate before 429 (1-100000) |
| `WEBSHIELD_CHALLENGE_ENABLED` | bool | `yes` | Enable JS challenge page for suspicious bots |
| `WEBSHIELD_BOT_FILTERING` | bool | `yes` | Enable user-agent based bot filtering |
| `WEBSHIELD_NGINX_CONF_DIR` | path | `/etc/nginx/jabali-security` | Directory for generated nginx config snippets |
| `NGINX_ACCESS_LOG` | path | `/var/log/nginx/access.log` | Nginx access log path (for WebShield stats) |

## GeoIP Blocking

| Key | Type | Default | Description |
|---|---|---|---|
| `GEOIP_ENABLED` | bool | `no` | Enable GeoIP country blocking with MaxMind database |
| `GEOIP_DB_PATH` | path | `/var/lib/jabali-security/GeoLite2-Country.mmdb` | Path to MaxMind GeoLite2-Country database |
| `GEOIP_MAXMIND_LICENSE_KEY` | string | (empty) | MaxMind license key for auto-downloading the database (free at maxmind.com) |
| `GEOIP_BLOCKED_COUNTRIES` | csv | (empty) | Comma-separated ISO country codes to block (e.g. `CN,RU,KP`) |
| `GEOIP_ALLOWED_COUNTRIES` | csv | (empty) | Whitelist mode: only these countries allowed (overrides blocked list) |
| `GEOIP_ACTION` | string | `block` | Default action for blocked countries: `block` (403), `challenge` (PoW page), or `log` |

> GeoIP operates independently from WebShield. It writes its own nginx configs to `/etc/nginx/jabali/cache-zones/geoip.conf` (http-level) and `/etc/nginx/jabali/includes/geo.conf` (server-level).

## Challenge System

| Key | Type | Default | Description |
|---|---|---|---|
| `CHALLENGE_DIFFICULTY` | int | `18` | SHA-256 proof-of-work difficulty in leading zero bits. 18 ≈ 0.5s solve time on modern hardware. |
| `CHALLENGE_TTL` | int | `86400` | Challenge cookie lifetime in seconds (default 24h). After solving, visitor bypasses challenges for this duration. |

> The challenge page is shared by GeoIP and WebShield. When a visitor is challenged, they solve a SHA-256 proof-of-work puzzle in their browser. On success, a `jabali_passed` cookie bypasses future challenges.

## UFW Firewall Management

| Key | Type | Default | Description |
|---|---|---|---|
| `UFW_ENABLED` | bool | `no` | Enable UFW firewall management via REST API. Requires `ufw` to be installed. Separate from the nftables/iptables-based brute-force IP blocking. |

## CrowdSec

| Key | Type | Default | Description |
|---|---|---|---|
| `CROWDSEC_ENABLED` | string | `auto` | `auto` (detect LAPI), `yes`, or `no` |
| `CROWDSEC_LAPI_URL` | url | `http://127.0.0.1:8080` | CrowdSec Local API URL |
| `CROWDSEC_BOUNCER_KEY` | string | (empty) | Bouncer API key. Generated by installer via `cscli bouncers add jabali-security -o raw` |
| `CROWDSEC_SYNC_INTERVAL` | int | `10` | Polling interval in seconds for decision stream (5-300) |

> `auto` mode: enables CrowdSec integration if bouncer key is configured and LAPI responds. No-op if CrowdSec is not installed.

## RapidScan

| Key | Type | Default | Description |
|---|---|---|---|
| `RAPIDSCAN_WORKERS` | int | `4` | Number of parallel workers (1-32) |
| `RAPIDSCAN_MTIME_CACHE` | bool | `yes` | Cache file modification times to skip unchanged files |

---

## Common Scenarios

### Minimal setup (file scanning only)

```
LOG_LEVEL="info"
WATCH_DIRS="/home/*/public_html"
AUTO_QUARANTINE="yes"
```

### Full protection (all features)

```
BRUTEFORCE_ENABLED="yes"
WAF_ENABLED="yes"
PROCESS_KILL_ENABLED="yes"
CLEANUP_ENABLED="yes"
CLEANUP_AUTO="yes"
THREAT_INTEL_ENABLED="yes"
WEBSHIELD_ENABLED="yes"
UFW_ENABLED="yes"
SCHEDULED_SCAN_ENABLED="yes"
```

### High-traffic server (tuned thresholds)

```
WORKERS="8"
WEBSHIELD_RATE_LIMIT="50"
WEBSHIELD_RATE_BURST="100"
BRUTEFORCE_SSH_THRESHOLD="10"
BRUTEFORCE_MAIL_THRESHOLD="20"
RAPIDSCAN_WORKERS="8"
```

### Sensitive server (aggressive response)

```
SCORE_LOG="20"
SCORE_QUARANTINE="50"
SCORE_SUSPEND="80"
AUTO_QUARANTINE="yes"
AUTO_BLOCK_IP="yes"
PROCESS_KILL_THRESHOLD="50"
NOTIFY_EMAIL="admin@example.com"
NOTIFY_MIN_SEVERITY="medium"
```
