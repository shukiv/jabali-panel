# REST API Reference

Jabali Security v0.1.0 -- 52 endpoints across 15 modules.

## Connection

The API listens on a Unix domain socket (not a TCP port):

    /run/jabali-security/jabali-security.sock

**curl example:**

```bash
curl --unix-socket /run/jabali-security/jabali-security.sock \
  -H "X-API-Key: YOUR_KEY" \
  http://localhost/api/v1/status
```

TCP fallback (disabled by default -- set `API_BIND="127.0.0.1"` in config to enable):

    http://127.0.0.1:9876/api/v1

## Authentication

All endpoints except `/health` require the `X-API-Key` header. The key is auto-generated on install and stored in `/etc/jabali-security/jabali-security.conf`. Uses constant-time comparison to prevent timing attacks. If no key is configured, requests are allowed without authentication.

## Response Format

All responses use this JSON envelope:

```json
{"success": true, "data": { ... }, "error": null}
```

On error:

```json
{"success": false, "data": null, "error": "Description of the error"}
```

| HTTP Code | Meaning |
|-----------|---------|
| 200 | Success |
| 400 | Bad request -- invalid parameters or missing required fields |
| 401 | Unauthorized -- missing or invalid API key |
| 404 | Not found -- resource doesn't exist or feature not enabled |
| 500 | Server error -- command failed, database error, etc. |
| 503 | Unavailable -- required subsystem not running |

---

## Health / Status

### `GET /health`

Returns minimal status without authentication. With a valid `X-API-Key`, returns detailed component status.

**Unauthenticated:**
```json
{"status": "healthy"}
```

**Authenticated:**
```json
{
  "status": "healthy",
  "components": {"database": "ok", "scanner": "ok", "watcher": "ok"},
  "uptime_seconds": 3661.2
}
```

### `GET /status`

Daemon status and resource metrics.

```json
{
  "running": true,
  "version": "0.1.0",
  "uptime_seconds": 3661.2,
  "incidents_24h": 12,
  "quarantined_count": 3,
  "watched_dirs": 48,
  "scan_queue_size": 0,
  "workers": 4,
  "memory_mb": 42.5
}
```

---

## Incidents

### `GET /incidents`

List security incidents.

**Query parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `limit` | int | 50 | Max results (1-1000) |
| `user` | string | -- | Filter by username |
| `severity` | string | -- | `low`, `medium`, `high`, `critical` |
| `since` | string | -- | ISO 8601 timestamp lower bound |

**Response:**

```json
[
  {
    "id": "a1b2c3d4e5f6g7h8",
    "path": "/home/user1/public_html/wp-content/uploads/shell.php",
    "username": "user1",
    "event_type": "create",
    "total_score": 85,
    "severity": "high",
    "action_taken": "quarantine",
    "findings": [
      {
        "scanner": "heuristic",
        "rule": "php_obfuscated_call",
        "score": 50,
        "description": "Obfuscated function call pattern detected"
      }
    ],
    "timestamp": "2026-03-27T10:30:00+00:00",
    "resolved": false,
    "notes": ""
  }
]
```

### `GET /incidents/{id}`

Get a single incident by ID. Returns 404 if not found.

### `POST /incidents/{id}/resolve`

Mark an incident as resolved.

**Body (optional):**

```json
{"notes": "False positive, whitelisted"}
```

**Response:**

```json
{"resolved": true, "id": "a1b2c3d4e5f6g7h8"}
```

---

## Scanning

### `POST /scan`

Scan a single file or directory. Symlinks are rejected. Directories automatically use the RapidScan engine for parallel scanning.

**Body:**

```json
{"path": "/home/user1/public_html/index.php"}
```

**Response (single file):**

```json
{
  "path": "/home/user1/public_html/index.php",
  "findings": [
    {
      "scanner": "yara",
      "rule": "webshell_php_generic",
      "score": 60,
      "description": "PHP webshell pattern"
    }
  ],
  "score": 60,
  "action": "quarantine",
  "severity": "high"
}
```

**Response (directory):** Same format as `POST /scan/rapid`.

### `POST /scan/full`

Trigger a full scheduled scan immediately. Returns 503 if scan scheduler not available.

**Response:**

```json
{"started": true, "status": { ... }}
```

### `GET /scan/scheduled`

Get scheduled scan status. Returns `{"enabled": false}` if scheduler not configured.

**Response:**

```json
{
  "enabled": true,
  "interval_hours": 24,
  "paths": ["/home/*/public_html"],
  "last_run": "2026-03-26T02:00:00+00:00",
  "next_run": "2026-03-27T02:00:00+00:00"
}
```

### `POST /scan/database`

Scan a MySQL CMS database for injected payloads. Validates database name, user, and host against allowlist patterns.

**Body:**

```json
{
  "database": "wp_user1",
  "user": "root",
  "host": "localhost",
  "cms_type": "wordpress",
  "table_prefix": "wp_"
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `database` | string | -- | Required. Alphanumeric + underscore only |
| `user` | string | `"root"` | DB user. Alphanumeric + underscore only |
| `host` | string | `"localhost"` | DB host. Alphanumeric, dots, hyphens only |
| `cms_type` | string | `"wordpress"` | `"wordpress"` or `"joomla"` |
| `table_prefix` | string | `"wp_"` | Table prefix. Alphanumeric + underscore only |

**Response:**

```json
{
  "database": "wp_user1",
  "findings_count": 2,
  "findings": [
    {
      "table": "wp_posts",
      "column": "post_content",
      "row_id": "142",
      "pattern": "js_inject",
      "description": "Injected JavaScript found"
    }
  ]
}
```

### `POST /scan/rapid`

Fast parallel directory scan with optional mtime caching. Path must be a directory. Returns 404 if directory not found.

**Body:**

```json
{"path": "/home/user1/public_html"}
```

**Response:**

```json
{
  "directory": "/home/user1/public_html",
  "files_scanned": 1240,
  "files_skipped": 890,
  "threats_found": 2,
  "results": [
    {"path": "/home/user1/public_html/shell.php", "score": 85, "action": "quarantine"}
  ]
}
```

---

## Quarantine

### `GET /quarantine`

List quarantined files.

| Parameter | Type | Description |
|-----------|------|-------------|
| `user` | string | Filter by username (optional) |

**Response:**

```json
[
  {
    "id": "f1e2d3c4b5a69788",
    "original_path": "/home/user1/public_html/shell.php",
    "quarantine_path": "/var/security/quarantine/user1/f1e2d3c4b5a69788",
    "username": "user1",
    "timestamp": "2026-03-27T10:30:00+00:00",
    "reason": "Score 85 exceeded quarantine threshold",
    "incident_id": "a1b2c3d4e5f6g7h8",
    "sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
    "restored": false,
    "deleted": false
  }
]
```

### `POST /quarantine/{id}/restore`

Restore a quarantined file to its original location.

**Response:**

```json
{"restored": true, "id": "f1e2d3c4b5a69788", "path": "/home/user1/public_html/shell.php"}
```

### `DELETE /quarantine/{id}`

Permanently delete a quarantined file.

**Response:**

```json
{"deleted": true, "id": "f1e2d3c4b5a69788"}
```

---

## Users

### `GET /users`

List users with aggregated incident statistics.

**Response:**

```json
[
  {"username": "user1", "incident_count": 5, "max_score": 85}
]
```

### `GET /users/{username}`

Get full risk profile for a user. Username must match `[a-zA-Z0-9._-]+`.

**Response:**

```json
{
  "username": "user1",
  "incidents": [ ... ],
  "quarantine": [ ... ],
  "incident_count": 5,
  "quarantine_count": 1
}
```

---

## IP Blocking

Manual IP blocking via the firewall (nftables/iptables). Separate from UFW rule management and brute-force auto-blocking.

### `POST /block`

Block an IP address.

**Body:**

```json
{
  "ip": "192.168.1.100",
  "reason": "manual block",
  "duration": 3600
}
```

| Field | Type | Description |
|-------|------|-------------|
| `ip` | string | Required. IPv4 or IPv6 address |
| `reason` | string | Block reason (default: `"manual block"`) |
| `duration` | int | Seconds until auto-unblock. Omit or `0` for permanent |

**Response:**

```json
{
  "blocked": true,
  "ip": "192.168.1.100",
  "reason": "manual block",
  "expires_at": "2026-03-27T11:30:00+00:00",
  "firewall": true
}
```

`firewall` indicates whether the IP was also blocked at the firewall level (false if no firewall backend available).

### `DELETE /block/{ip}`

Unblock an IP address. Returns 404 if not found.

**Response:**

```json
{"unblocked": true, "ip": "192.168.1.100"}
```

### `GET /blocklist`

List all blocked IPs.

**Response:**

```json
[
  {
    "ip": "192.168.1.100",
    "reason": "bruteforce",
    "blocked_at": "2026-03-27T10:30:00+00:00",
    "expires_at": "2026-03-27T11:30:00+00:00",
    "blocked_by": "auto"
  }
]
```

---

## Configuration

### `GET /config`

Get current configuration. All values are returned as strings. Booleans as `"yes"`/`"no"`, lists as comma-separated. `API_KEY` is redacted to `"set"` or `"unset"`.

**Response:**

```json
{
  "LOG_LEVEL": "info",
  "WORKERS": "4",
  "API_KEY": "set",
  "ENTROPY_THRESHOLD": "6.0",
  "WATCH_DIRS": "/home/*/public_html,/home/*/domains/*/public_html,/home/*/tmp"
}
```

### `PATCH /config`

Update configuration keys. Writes to the config file on disk. All values are converted to strings. Keys must exist in the defaults. Read-only keys (`API_KEY`, `API_BIND`, `API_PORT`) cannot be changed via the API. Path-type config values are validated against safe prefixes. `API_KEY` is redacted in the response.

**Body:**

```json
{"LOG_LEVEL": "debug", "WORKERS": "8"}
```

**Response:**

```json
{
  "updated": {"LOG_LEVEL": "debug", "WORKERS": "8"},
  "note": "Restart daemon to apply runtime changes"
}
```

---

## Rules

### `GET /rules`

List loaded detection rules and active scanners.

**Response:**

```json
{
  "yara_rules": [
    {"name": "webshells.yar", "size": 4096},
    {"name": "exploits.yar", "size": 2048}
  ],
  "yara_rules_dir": "/usr/local/jabali-security/rules",
  "yara_enabled": true,
  "clamav_enabled": true,
  "scanners": ["heuristic", "entropy", "yara", "clamav"]
}
```

### `POST /rules/reload`

Reload YARA rules from disk and optionally run freshclam to update ClamAV signatures.

**Response:**

```json
{
  "yara_reloaded": true,
  "freshclam_success": true,
  "freshclam_output": "ClamAV update process started..."
}
```

`freshclam_success` is `null` if `FRESHCLAM_ON_UPDATE` is disabled.

---

## Brute-Force Protection

All endpoints return 404 if `BRUTEFORCE_ENABLED="no"`.

### `GET /bruteforce/stats`

**Response:**

```json
{"tracked_ips": 42, "blocked_count": 3}
```

### `GET /bruteforce/blocked`

List IPs currently blocked by the firewall. Returns 404 if no firewall backend available.

**Response:**

```json
{"blocked_ips": ["192.168.1.100", "10.0.0.50"], "count": 2}
```

### `POST /bruteforce/whitelist`

Add IP to brute-force whitelist. Also unblocks the IP if currently blocked.

**Body:**

```json
{"ip": "192.168.1.1"}
```

**Response:**

```json
{"whitelisted": true, "ip": "192.168.1.1"}
```

### `DELETE /bruteforce/whitelist/{ip}`

Remove IP from whitelist. Returns 404 if not in whitelist.

**Response:**

```json
{"removed": true, "ip": "192.168.1.1"}
```

---

## WAF (ModSecurity)

Requires `WAF_ENABLED="yes"`. Rule management endpoints return 404 if WAF is not enabled.

### `GET /waf/events`

List WAF events from the ModSecurity audit log.

**Query parameters:**

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `limit` | int | 50 | Max results (1-1000) |
| `ip` | string | -- | Filter by client IP |
| `rule_id` | int | -- | Filter by ModSecurity rule ID |
| `since` | string | -- | ISO 8601 timestamp lower bound |

**Response:**

```json
[
  {
    "id": "abc123",
    "client_ip": "203.0.113.50",
    "uri": "/wp-login.php",
    "method": "POST",
    "rule_id": 942100,
    "rule_msg": "SQL Injection Attack Detected",
    "severity": "CRITICAL",
    "action": "deny",
    "hostname": "example.com",
    "username": "user1",
    "matched_data": "1=1",
    "created_at": "2026-03-27T10:30:00+00:00"
  }
]
```

### `GET /waf/rules`

List CRS rule files and disabled rules.

**Response:**

```json
{
  "rule_files": [
    {"file": "REQUEST-942-APPLICATION-ATTACK-SQLI.conf", "size": 45000}
  ],
  "disabled_rules": [942100, 942200],
  "web_server": "nginx"
}
```

### `POST /waf/rules/{rule_id}/disable`

Disable a ModSecurity rule and reload the web server. Rule ID must be 1-9999999.

**Response:**

```json
{"disabled": true, "rule_id": 942100, "web_server_reloaded": true}
```

### `POST /waf/rules/{rule_id}/enable`

Re-enable a previously disabled rule and reload the web server.

**Response:**

```json
{"enabled": true, "rule_id": 942100, "web_server_reloaded": true}
```

### `GET /waf/stats`

WAF statistics (aggregated from stored events).

**Response:**

```json
{
  "total_events_24h": 156,
  "blocked_24h": 42,
  "top_ips": [
    {"ip": "203.0.113.50", "count": 25}
  ],
  "top_rules": [
    {"rule_id": 942100, "count": 18, "rule_msg": "SQL Injection Attack Detected"}
  ]
}
```

### `POST /waf/crs/update`

Update OWASP Core Rule Set. Returns 500 on failure.

**Response:**

```json
{"success": true, "version": "4.0.0", "rules_count": 42}
```

---

## Proactive Defense

### `GET /proactive/status`

**Response:**

```json
{
  "process_kill_enabled": true,
  "process_kill_count": 5
}
```

### `GET /proactive/kills`

List recent process kills. Returns `[]` if process killer is not enabled.

**Response:**

```json
[
  {
    "id": "abc123",
    "pid": 12345,
    "ppid": 1000,
    "username": "user1",
    "score": 85,
    "success": true,
    "reason": "Suspicious process tree"
  }
]
```

---

## Cleanup

Requires `CLEANUP_ENABLED="yes"`.

### `GET /cleanup/records`

List recent cleanup operations (max 50).

**Response:**

```json
[
  {
    "id": "abc123",
    "path": "/home/user1/public_html/index.php",
    "strategy": "injection_removal",
    "success": true,
    "backup_path": "/var/lib/jabali-security/backups/abc123",
    "username": "user1",
    "created_at": "2026-03-27T10:30:00+00:00"
  }
]
```

### `POST /cleanup/file`

Clean a specific file. Creates a backup before cleaning. Rejects symlinks. Returns 404 if file not found, 503 if cleanup engine not available.

**Body:**

```json
{"path": "/home/user1/public_html/index.php"}
```

**Response:**

```json
{
  "success": true,
  "changes_made": ["Removed injected script tag at line 42"],
  "backup_path": "/var/lib/jabali-security/backups/abc123"
}
```

---

## Threat Intelligence

Requires `THREAT_INTEL_ENABLED="yes"`. All endpoints return 404 if not enabled.

### `GET /threat-intel/feeds`

List feed statuses.

**Response:**

```json
[
  {
    "name": "spamhaus_drop",
    "source_url": "https://www.spamhaus.org/drop/drop.txt",
    "last_update": "2026-03-27T06:00:00+00:00",
    "entry_count": 1200,
    "enabled": true,
    "feed_type": "ip"
  }
]
```

### `POST /threat-intel/update`

Trigger immediate update of all enabled feeds.

**Response:**

```json
{
  "updated": {
    "spamhaus_drop": true,
    "spamhaus_edrop": true,
    "blocklist_de_all": true,
    "malwarebazaar_recent": true
  },
  "success_count": 4,
  "total_count": 4
}
```

### `GET /threat-intel/check/ip/{ip}`

Check an IP address against all threat intelligence feeds. IP must be valid IPv4 or IPv6.

**Response:**

```json
{
  "ip": "203.0.113.50",
  "is_malicious": true,
  "score": 3,
  "feeds": ["spamhaus_drop", "blocklist_de_all", "tor_exit_nodes"]
}
```

### `GET /threat-intel/check/hash/{hash}`

Check a SHA-256 file hash against threat intelligence feeds. Hash must be exactly 64 hex characters.

| Parameter | Type | Description |
|-----------|------|-------------|
| `remote` | query | Set to `1`, `true`, or `yes` to also check remote APIs |

**Response:**

```json
{
  "hash": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
  "is_malicious": true,
  "score": 2,
  "feeds": ["malwarebazaar_recent"],
  "details": {
    "signature": "Backdoor.PHP.WebShell",
    "file_type": "php"
  }
}
```

---

## WebShield

Nginx-level bot filtering, rate limiting, and JS challenge pages. Requires `WEBSHIELD_ENABLED="yes"`. All endpoints return 404 if not enabled.

### `GET /webshield/status`

**Response:**

```json
{
  "installed": true,
  "nginx_available": true,
  "rate_limiting": true,
  "bot_filtering": true,
  "challenge_enabled": true,
  "blocked_ips_count": 12,
  "config_dir": "/etc/nginx/jabali-security"
}
```

### `POST /webshield/install`

Generate and write WebShield nginx config snippets. Returns 500 if install fails.

**Response:**

```json
{
  "success": true,
  "files_written": ["/etc/nginx/jabali-security/rate-limit.conf"],
  "nginx_config_valid": true,
  "note": "Include the config snippets in your nginx server blocks"
}
```

### `POST /webshield/uninstall`

Remove WebShield nginx config files.

**Response:**

```json
{"files_removed": ["/etc/nginx/jabali-security/rate-limit.conf"]}
```

### `GET /webshield/rules`

Get bot detection rules.

**Response:**

```json
[
  {
    "name": "bad_bot_ua",
    "action": "block",
    "category": "bot",
    "pattern": "MJ12bot|AhrefsBot",
    "enabled": true
  }
]
```

### `POST /webshield/update-blocklist`

Update the WebShield IP blocklist (nginx deny map). All IPs are validated.

**Body:**

```json
{"ips": ["192.168.1.100", "10.0.0.50"]}
```

**Response:**

```json
{"updated": true, "count": 2}
```

---

## UFW Firewall Management

Manage system-level UFW firewall rules via the REST API. Requires `UFW_ENABLED="yes"`. All endpoints return 404 if not enabled. Separate from the nftables-based IP blocking used by brute-force protection.

### `GET /firewall/ufw/status`

Get UFW status and default policies.

**Response:**

```json
{
  "available": true,
  "active": true,
  "default_incoming": "deny",
  "default_outgoing": "allow",
  "default_routed": "disabled",
  "rules": [ ... ],
  "rules_count": 5
}
```

`available` is `false` if the `ufw` binary is not installed on the system.

### `GET /firewall/ufw/rules`

List all UFW rules with their numbers.

**Response:**

```json
[
  {
    "number": 1,
    "to": "22/tcp",
    "action": "ALLOW",
    "from_ip": "Anywhere",
    "direction": "IN",
    "v6": false,
    "raw": "[ 1] 22/tcp                     ALLOW IN    Anywhere"
  }
]
```

### `POST /firewall/ufw/rules`

Add a UFW rule. At least one of `port`, `from_ip`, or `to_ip` is required.

**Body:**

```json
{
  "action": "allow",
  "port": "443",
  "protocol": "tcp",
  "from_ip": "192.168.1.0/24",
  "to_ip": "10.0.0.1",
  "direction": "in",
  "comment": "HTTPS from LAN"
}
```

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `action` | string | yes | `allow`, `deny`, `reject`, or `limit` |
| `port` | string | no | Port (1-65535), range (`8000:8080`), or service name |
| `protocol` | string | no | `tcp`, `udp`, or `any` |
| `from_ip` | string | no | Source IP or CIDR |
| `to_ip` | string | no | Destination IP or CIDR |
| `direction` | string | no | `in` or `out` |
| `comment` | string | no | Printable ASCII, max 256 chars |

**Response:**

```json
{"added": true}
```

### `DELETE /firewall/ufw/rules/{number}`

Delete a UFW rule by its number (1-9999).

**Response:**

```json
{"deleted": true, "rule_number": 1}
```

### `POST /firewall/ufw/enable`

Enable the UFW firewall.

**Response:**

```json
{"enabled": true}
```

### `POST /firewall/ufw/disable`

Disable the UFW firewall.

**Response:**

```json
{"disabled": true}
```

### `POST /firewall/ufw/reload`

Reload UFW configuration.

**Response:**

```json
{"reloaded": true}
```

### `GET /firewall/ufw/apps`

List available UFW application profiles.

**Response:**

```json
["Apache", "Nginx Full", "OpenSSH"]
```

### `GET /firewall/ufw/apps/{name}`

Get details for a specific application profile. URL-encode names with spaces (e.g., `Nginx%20Full`). Returns 404 if not found.

**Response:**

```json
{
  "name": "Nginx Full",
  "title": "Web Server (Nginx, HTTP + HTTPS)",
  "description": "Small, but very powerful and efficient web server",
  "ports": "80,443/tcp"
}
```

### `POST /firewall/ufw/apps/{name}/allow`

Allow traffic for an application profile.

**Response:**

```json
{"allowed": true, "app": "Nginx Full"}
```

### `POST /firewall/ufw/apps/{name}/deny`

Deny traffic for an application profile.

**Response:**

```json
{"denied": true, "app": "Nginx Full"}
```

---

## GeoIP Blocking

GeoIP operates independently from WebShield. It writes its own nginx configs and uses MaxMind GeoLite2-Country database.

### `GET /webshield/geo-status`

GeoIP database status and metadata.

**Response:**

```json
{"enabled": true, "available": true, "path": "/var/lib/jabali-security/GeoLite2-Country.mmdb", "build_epoch": 1775211257, "database_type": "GeoLite2-Country"}
```

### `GET /webshield/geo-rules`

List blocked/allowed countries with database status.

**Response:**

```json
{"rules": [{"country_code": "CN", "country_name": "China", "action": "block", "enabled": true}], "mode": "blocklist", "action": "block", "enabled": true, "db": {"available": true}}
```

### `POST /webshield/geo-rules`

Set blocked or allowed countries. Regenerates nginx config and reloads nginx.

**Body:**

```json
{"countries": ["CN", "RU", "KP"], "action": "block", "mode": "blocklist"}
```

- `action`: `block` (403), `challenge` (PoW page), or `log`
- `mode`: `blocklist` (block listed, allow rest) or `whitelist` (allow listed, block rest)

### `DELETE /webshield/geo-rules/{country_code}`

Remove a single country from the block/allow list.

### `POST /webshield/geo-update-db`

Download/update MaxMind GeoIP database. Optionally accepts `account_id` and `license_key` to write `/etc/GeoIP.conf` for the `geoipupdate` CLI tool.

**Body (optional):**

```json
{"account_id": "123456", "license_key": "xxxx_xxxx"}
```

---

## Attack Mode

### `GET /attack-mode`

Check attack mode status.

**Response:**

```json
{"active": false}
```

### `POST /attack-mode/enable`

Enable "Under Attack" mode. Saves current settings, applies aggressive config (WAF on, WebShield, IP blocking, lowered thresholds).

### `POST /attack-mode/disable`

Disable attack mode and restore previous settings.

---

## CrowdSec

Community threat intelligence powered by CrowdSec LAPI. Requires `CROWDSEC_ENABLED` and a bouncer API key.

### `GET /crowdsec/status`

**Response:**

```json
{
  "enabled": true,
  "connected": true,
  "lapi_url": "http://127.0.0.1:8080",
  "active_decisions": 142,
  "blocked_ips": 89,
  "last_poll": "2026-03-31T10:00:00+00:00",
  "error": ""
}
```

### `GET /crowdsec/decisions`

List all active CrowdSec decisions.

**Response:**

```json
{
  "decisions": [
    {"id": 1023, "origin": "CAPI", "type": "ban", "scope": "Ip", "value": "1.2.3.4", "duration": "4h0m0s", "scenario": "crowdsecurity/ssh-bf"}
  ],
  "count": 1
}
```

### `GET /crowdsec/check/{ip}`

Check a specific IP against CrowdSec decisions (cached + live LAPI query).

**Response:**

```json
{
  "ip": "1.2.3.4",
  "cached_decisions": [...],
  "live_decisions": [...],
  "score": 60,
  "is_blocked": true
}
```

---

## Daemon Control

### `POST /daemon/restart`

Restart the jabali-security daemon via systemctl. Executes after a 1-second delay.

---

## Endpoint Summary

| Module | Endpoints | Config Gate |
|--------|-----------|-------------|
| Health / Status | 3 | -- |
| Incidents | 3 | -- |
| Scanning | 5 | -- |
| Quarantine | 3 | -- |
| Users | 2 | -- |
| IP Blocking | 3 | -- |
| Configuration | 2 | -- |
| Rules | 2 | -- |
| Brute-Force | 4 | `BRUTEFORCE_ENABLED` |
| WAF | 6 | `WAF_ENABLED` |
| Process Killer | 2 | `PROCESS_KILL_ENABLED` |
| Cleanup | 2 | `CLEANUP_ENABLED` |
| Threat Intel | 4 | `THREAT_INTEL_ENABLED` |
| CrowdSec | 3 | `CROWDSEC_ENABLED` |
| Attack Mode | 3 | -- |
| WebShield | 5 | `WEBSHIELD_ENABLED` |
| GeoIP | 5 | `GEOIP_ENABLED` |
| UFW Firewall | 11 | `UFW_ENABLED` |
| **Total** | **68** | |
