# CLI Reference

```
jabali-security [COMMAND] [OPTIONS]
```

All commands support `--help` for usage details. Commands that output data support `--json` for machine-readable output.

When the daemon is running, most commands communicate via the REST API. Some commands (e.g., `scan`, `scan-db`, `scan-rapid`) can also run standalone without the daemon.

---

## Daemon

### start

Start the security daemon.

```bash
jabali-security start [--foreground] [--config PATH]
```

| Option | Description |
|---|---|
| `--foreground` | Run in foreground with console logging (instead of daemonizing) |
| `--config PATH` | Path to config file (default: `/etc/jabali-security/jabali-security.conf`) |

```bash
# Start as systemd service (normal)
sudo systemctl start jabali-security

# Start in foreground for debugging
jabali-security start --foreground

# Start with custom config
jabali-security start --config /path/to/jabali-security.conf
```

### stop

Stop the running daemon by sending SIGTERM.

```bash
jabali-security stop
```

### status

Show daemon status including uptime, memory, queue size, and incident counts.

```bash
jabali-security status [--json]
```

```bash
jabali-security status
# Jabali Security v0.1.0
#   Status:     running (PID 12345)
#   Uptime:     2h 15m 30s
#   Workers:    4
#   Queue:      0 pending
#   Watched:    48 dirs
#   Incidents:  3 (24h)
#   Quarantine: 1 files
#   Memory:     42.5 MB
```

### update

Update jabali-security to the latest version. Pulls the latest code from the Git repository and restarts the daemon.

```bash
jabali-security update
```

---

## Scanning

### scan

Scan a file or directory for threats.

```bash
jabali-security scan <PATH> [--recursive|-r] [--json]
```

| Option | Description |
|---|---|
| `-r`, `--recursive` | Scan directory recursively (required for directories) |
| `--json` | Output as JSON |

```bash
# Scan a single file
jabali-security scan /home/user1/public_html/index.php

# Scan a directory recursively
jabali-security scan /home/user1/public_html -r

# JSON output for scripting
jabali-security scan /home/user1/public_html -r --json
```

Works with or without the daemon running. When the daemon is running, uses the API; otherwise runs a standalone scan.

### scan-full

Trigger a full scheduled scan immediately.

```bash
jabali-security scan-full
```

Requires the daemon to be running with `SCHEDULED_SCAN_ENABLED=yes`.

### scan-db

Scan a MySQL database for malware (injected payloads in CMS tables).

```bash
jabali-security scan-db <DATABASE> [--user USER] [--host HOST] [--cms CMS] [--prefix PREFIX] [--json]
```

| Option | Default | Description |
|---|---|---|
| `--user` | root | MySQL user |
| `--host` | localhost | MySQL host |
| `--cms` | wordpress | CMS type (`wordpress` or `joomla`) |
| `--prefix` | wp_ | Table prefix |
| `--json` | -- | Output as JSON |

```bash
jabali-security scan-db wp_user1 --user root --cms wordpress --prefix wp_
```

### scan-rapid

Fast parallel directory scan with mtime cache (skips unchanged files).

```bash
jabali-security scan-rapid <PATH> [--workers|-w N] [--json]
```

| Option | Default | Description |
|---|---|---|
| `-w`, `--workers` | 4 | Number of parallel workers |
| `--json` | -- | Output as JSON |

```bash
jabali-security scan-rapid /home --workers 8
```

---

## Incidents

### incidents list

List security incidents with optional filters.

```bash
jabali-security incidents list [--limit|-n N] [--user USERNAME] [--severity LEVEL] [--json]
```

| Option | Default | Description |
|---|---|---|
| `-n`, `--limit` | 20 | Max results |
| `--user` | -- | Filter by username |
| `--severity` | -- | Filter: `low`, `medium`, `high`, `critical` |
| `--json` | -- | Output as JSON |

```bash
# List recent high-severity incidents
jabali-security incidents list --severity high

# List incidents for a specific user
jabali-security incidents list --user user1 --limit 50
```

---

## Quarantine

### quarantine list

List quarantined files.

```bash
jabali-security quarantine list [--user USERNAME] [--json]
```

### quarantine restore

Restore a quarantined file to its original location.

```bash
jabali-security quarantine restore <RECORD_ID>
```

### quarantine delete

Permanently delete a quarantined file.

```bash
jabali-security quarantine delete <RECORD_ID>
```

```bash
# List quarantined files
jabali-security quarantine list

# Restore a file
jabali-security quarantine restore a1b2c3d4e5f6g7h8

# Delete permanently
jabali-security quarantine delete a1b2c3d4e5f6g7h8
```

---

## Configuration

### config show

Show current configuration (from daemon if running, otherwise from file).

```bash
jabali-security config show
```

### config set

Set a configuration value. Persists to file and pushes to running daemon if available.

```bash
jabali-security config set <KEY> <VALUE>
```

```bash
jabali-security config set LOG_LEVEL debug
jabali-security config set WORKERS 4
jabali-security config set BRUTEFORCE_ENABLED yes
```

### config test

Validate the configuration file and show key settings with warnings.

```bash
jabali-security config test
```

```bash
jabali-security config test
# Configuration file: /etc/jabali-security/jabali-security.conf
#   Log level:     info
#   API bind:      127.0.0.1:9876
#   Workers:       4
#   Watch dirs:    /home/*/public_html, /home/*/tmp
#   Scan ext:      .php, .phtml, .js, .py, .sh, ...
#   Max file size: 2097152 bytes
#   YARA enabled:  True
#   ClamAV:        auto
#
# Configuration OK.
```

---

## Rules

### rules list

List loaded detection rules (YARA files, ClamAV status, active scanners).

```bash
jabali-security rules list
```

### rules update

Reload YARA rules from disk and update ClamAV signatures (runs freshclam).

```bash
jabali-security rules update
```

---

## Users

### user list

List hosting users with their risk scores.

```bash
jabali-security user list [--min-score N] [--json]
```

| Option | Default | Description |
|---|---|---|
| `--min-score` | 0 | Only show users with risk score >= N |
| `--json` | -- | Output as JSON |

### user risk

Show risk profile for a specific user.

```bash
jabali-security user risk <USERNAME> [--json]
```

```bash
jabali-security user risk user1
# User:       user1
# Risk score: 85
# Status:     active
# Incidents:  5
#
# Recent incidents:
#   [high] a1b2c3d4e5f6g7h8 - quarantine (2026-03-27)
```

---

## IP Blocking

### block

Block an IP address in the firewall and database.

```bash
jabali-security block <IP> [--reason TEXT] [--duration SECONDS]
```

| Option | Default | Description |
|---|---|---|
| `--reason` | manual | Reason for blocking |
| `--duration` | 0 | Duration in seconds (0 = permanent) |

```bash
jabali-security block 192.168.1.100 --reason "brute force" --duration 3600
```

### unblock

Unblock an IP address.

```bash
jabali-security unblock <IP>
```

### blocklist

List all blocked IP addresses.

```bash
jabali-security blocklist [--json]
```

---

## Brute-Force Protection

### bruteforce stats

Show brute-force protection statistics (tracked IPs, block count).

```bash
jabali-security bruteforce stats [--json]
```

### bruteforce blocked

List IPs currently blocked by brute-force protection.

```bash
jabali-security bruteforce blocked [--json]
```

### bruteforce whitelist-add

Add an IP to the brute-force whitelist (never block this IP).

```bash
jabali-security bruteforce whitelist-add <IP>
```

### bruteforce whitelist-remove

Remove an IP from the brute-force whitelist.

```bash
jabali-security bruteforce whitelist-remove <IP>
```

---

## WAF (ModSecurity)

### waf events

List recent WAF events.

```bash
jabali-security waf events [--limit|-n N] [--ip IP] [--rule-id ID] [--json]
```

| Option | Default | Description |
|---|---|---|
| `-n`, `--limit` | 20 | Max results |
| `--ip` | -- | Filter by client IP |
| `--rule-id` | -- | Filter by ModSecurity rule ID |
| `--json` | -- | Output as JSON |

### waf rules

List CRS rule files and disabled rules.

```bash
jabali-security waf rules [--json]
```

### waf disable

Disable a ModSecurity rule by ID. Reloads the web server.

```bash
jabali-security waf disable <RULE_ID>
```

```bash
jabali-security waf disable 942100
# Rule 942100 disabled. Web server reloaded.
```

### waf enable

Re-enable a previously disabled ModSecurity rule.

```bash
jabali-security waf enable <RULE_ID>
```

### waf stats

Show WAF statistics for the last 24 hours (event counts, top IPs, top rules).

```bash
jabali-security waf stats [--json]
```

### waf update

Update OWASP Core Rule Set.

```bash
jabali-security waf update
```

---

## Proactive Defense

### proactive status

Show proactive defense status (process killer).

```bash
jabali-security proactive status [--json]
```

### proactive kills

List recent process kills.

```bash
jabali-security proactive kills [--json]
```

---

## Cleanup

### cleanup records

List recent cleanup operations.

```bash
jabali-security cleanup records [--json]
```

### cleanup file

Manually clean a specific file (remove injected code).

```bash
jabali-security cleanup file <PATH> [--json]
```

```bash
jabali-security cleanup file /home/user1/public_html/index.php
# Cleanup succeeded: /home/user1/public_html/index.php
#   Changes: 2
```

### cleanup cms

Check CMS integrity and clean infections for a site directory.

```bash
jabali-security cleanup cms <PATH> [--json]
```

```bash
jabali-security cleanup cms /home/user1/public_html
```

---

## Threat Intelligence

### threat-intel feeds

List threat intelligence feed statuses.

```bash
jabali-security threat-intel feeds [--json]
```

### threat-intel update

Trigger an immediate update of all enabled feeds.

```bash
jabali-security threat-intel update
# Updating threat intelligence feeds...
# Feed update complete: 5/5 succeeded.
#   spamhaus_drop: OK
#   spamhaus_edrop: OK
#   blocklist_de_all: OK
#   tor_exit_nodes: OK
#   malwarebazaar_recent: OK
```

### threat-intel check-ip

Check an IP address against threat intelligence feeds.

```bash
jabali-security threat-intel check-ip <IP> [--json]
```

```bash
jabali-security threat-intel check-ip 203.0.113.50
# MALICIOUS: 203.0.113.50 (score: 3)
#   Matched feeds: spamhaus_drop, blocklist_de_all, tor_exit_nodes
```

### threat-intel check-hash

Check a SHA-256 hash against threat intelligence feeds.

```bash
jabali-security threat-intel check-hash <SHA256> [--remote] [--json]
```

| Option | Description |
|---|---|
| `--remote` | Also check remote APIs (slower) |
| `--json` | Output as JSON |

---

## WebShield

### webshield status

Show WebShield installation status.

```bash
jabali-security webshield status [--json]
```

### webshield install

Install WebShield nginx configuration files (rate limiting, bot filtering, challenge pages).

```bash
jabali-security webshield install
```

### webshield uninstall

Remove WebShield nginx configuration files.

```bash
jabali-security webshield uninstall
```

### webshield rules

List bot detection rules.

```bash
jabali-security webshield rules [--json]
```

---

## CrowdSec

Community threat intelligence integration. Requires CrowdSec to be installed and a bouncer API key configured.

### crowdsec status

Show CrowdSec LAPI connection status.

```bash
jabali-security crowdsec status [--json]
```

```bash
jabali-security crowdsec status
# CrowdSec integration:
#   Enabled:     yes
#   Connected:   yes
#   LAPI URL:    http://127.0.0.1:8080
#   Decisions:   142
#   Blocked IPs: 89
#   Last poll:   2026-03-31T10:00:00+00:00
```

### crowdsec decisions

List active CrowdSec decisions (banned IPs with scenario details).

```bash
jabali-security crowdsec decisions [--json]
```

### crowdsec check

Check a specific IP against CrowdSec decisions.

```bash
jabali-security crowdsec check <IP> [--json]
```

```bash
jabali-security crowdsec check 203.0.113.50
# IP: 203.0.113.50
# Score: 60
# Blocked: yes
# Cached decisions:
#   ban — crowdsecurity/ssh-bf (4h0m0s)
```

### crowdsec unban

Remove a CrowdSec decision for an IP.

```bash
jabali-security crowdsec unban <IP>
```

```bash
jabali-security crowdsec unban 198.51.100.1
# Decision removed for 198.51.100.1.
```

---

## Attack Mode

Panic button for active attacks. Enables aggressive defenses: process killer, auto-block IPs, WAF blocking, WebShield rate limiting, tighter brute-force thresholds, progressive IP bans.

### attack-mode status

Show current attack mode status.

```bash
jabali-security attack-mode status
```

### attack-mode enable

Activate attack mode — all aggressive defenses enabled immediately.

```bash
jabali-security attack-mode enable
```

```bash
jabali-security attack-mode enable
# Attack mode ENABLED.
#   WebShield rate limiting installed (10 req/s)
#   Brute-force thresholds lowered (SSH: 3/120s, Mail: 3/120s)
#   Process killer threshold lowered
#   All tracked brute-force IPs blocked
```

### attack-mode disable

Deactivate attack mode — restore previous settings.

```bash
jabali-security attack-mode disable
```

---

## Firewall (UFW)

Manage UFW firewall rules, enable/disable, and reload.

### firewall status

Show UFW status and current rules.

```bash
jabali-security firewall status [--json]
```

```bash
jabali-security firewall status
# UFW: active
# Default: incoming=deny outgoing=allow
#   22/tcp ALLOW Anywhere
#   443/tcp ALLOW Anywhere
#   8443/tcp ALLOW Anywhere
```

### firewall enable

Enable UFW firewall.

```bash
jabali-security firewall enable
```

### firewall disable

Disable UFW firewall.

```bash
jabali-security firewall disable
```

### firewall reload

Reload UFW rules.

```bash
jabali-security firewall reload
```

### firewall allow

Allow a port through the firewall.

```bash
jabali-security firewall allow <PORT> [--proto tcp|udp|any] [--from IP] [--comment TEXT]
```

```bash
jabali-security firewall allow 3306 --proto tcp --from 10.0.0.0/8 --comment "MySQL from LAN"
```

### firewall deny

Deny a port through the firewall.

```bash
jabali-security firewall deny <PORT> [--proto tcp|udp|any] [--from IP]
```

### firewall delete-rule

Delete a firewall rule by number (as shown in `firewall status`).

```bash
jabali-security firewall delete-rule <RULE_NUMBER>
```

---

## Daemon Control

### restart

Restart the jabali-security daemon.

```bash
jabali-security restart
```

---

