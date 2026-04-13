# Jabali Security

**Event-driven security suite for Linux shared hosting.**
A lightweight, panel-agnostic alternative to Imunify360.

---

## Features

| Category | Capabilities |
|---|---|
| **Real-time monitoring** | inotify file watcher, process tree monitor, behavior lifecycle tracking |
| **Multi-engine scanning** | Heuristic regex, Shannon entropy, YARA-X signatures, ClamAV CLI (optional) |
| **Threat scoring** | Aggregated score per event with configurable log/quarantine/suspend thresholds |
| **Automated response** | Quarantine, process kill, IP blocking via nftables/iptables |
| **CrowdSec integration** | LAPI bouncer client, decision streaming, community threat signals |
| **UFW management** | Full UFW firewall rule CRUD, enable/disable/reload, app profiles via REST API |
| **Brute-force protection** | SSH + mail (Dovecot/Postfix/Exim/Stalwart) with progressive blocking |
| **WAF integration** | ModSecurity audit log parsing, OWASP CRS management, rule toggling |
| **Proactive defense** | Suspicious process killer (reverse shells, crypto miners, malicious scripts) |
| **Attack mode** | Elevated defense posture with tighter thresholds on demand |
| **Malware cleanup** | Injection removal, CMS integrity checks (WordPress/Joomla), backup-before-clean |
| **Threat intelligence** | Spamhaus, blocklist.de, MalwareBazaar, Tor exit nodes; IP + hash lookups |
| **WebShield** | Bot UA filtering (on by default), rate limiting (opt-in, auto-enabled in attack mode), shared PoW challenge pages |
| **GeoIP blocking** | Country-level block/challenge/log with MaxMind GeoLite2, independent nginx config, auto-database updates |
| **Database scanning** | MySQL table scanning for injected payloads (WordPress, Joomla) |
| **RapidScan** | Parallel directory scanner with mtime cache for unchanged-file skipping |
| **Scheduled scans** | Configurable periodic full-path scanning |
| **Multi-tenant** | Automatic file-to-user mapping for shared hosting accounts |
| **REST API** | aiohttp on Unix socket with API key auth, 50+ endpoints |
| **CLI** | 30+ click commands for full management |
| **Panel plugin** | Filament-based admin page for Jabali Panel integration |

## Quick Install

```bash
curl -fsSL https://git.linux-hosting.co.il/shukivaknin/jabali-security/raw/branch/master/install.sh | sudo bash
```

The installer will: clone the repo, create a Python venv, install dependencies, generate an API key, configure CrowdSec + firewall bouncer, install OWASP CRS from GitHub, set up UFW rules, enable WebShield bot filtering (on Jabali Panel servers), and start the daemon.

```bash
jabali-security status              # check daemon status
jabali-security config test         # verify configuration
jabali-security scan /home -r       # on-demand recursive scan
journalctl -u jabali-security -f    # watch live logs
```

## Architecture

```
                          ┌──────────────────────┐
                          │   Security Daemon     │
                          │   (asyncio supervisor)│
                          └──────────┬───────────┘
                                     │
          ┌──────────────────────────┼──────────────────────────┐
          │                          │                          │
  ┌───────▼────────┐      ┌─────────▼─────────┐     ┌─────────▼─────────┐
  │ Event Sources   │      │ Detection Pipeline │     │ Response Engine   │
  │                 │      │                    │     │                   │
  │ inotify watcher │─────>│ Scan Queue (50K)   │────>│ Quarantine        │
  │ /proc monitor   │      │     │              │     │ Process kill      │
  │ behavior tracker│      │ Scan Workers (×4)  │     │ IP block (nft)    │
  │ auth log parser │      │     │              │     │ User suspend      │
  │ WAF audit log   │      │ Scanners:          │     │ Notifications     │
  └─────────────────┘      │  - Heuristic       │     └───────────────────┘
                           │  - Entropy         │
                           │  - YARA-X          │     ┌───────────────────┐
                           │  - ClamAV (opt)    │────>│ Incident Store    │
                           │     │              │     │ (SQLite)          │
                           │ Scoring Engine     │     └───────────────────┘
                           └────────────────────┘

  ┌───────────────────┐    ┌────────────────────┐    ┌───────────────────┐
  │ CrowdSec Client   │    │ Threat Intel Feeds │    │ REST API          │
  │ LAPI bouncer      │    │ IP reputation (bisect)│ │ (Unix socket)     │
  │ decision stream   │    │ hash reputation     │    │ 50+ endpoints     │
  └───────────────────┘    │ periodic updates    │    └───────────────────┘
                           └────────────────────┘
  ┌───────────────────┐    ┌────────────────────┐    ┌───────────────────┐
  │ WebShield Manager │    │ WAF Rule Manager   │    │ UFW Manager       │
  │ nginx rate limit  │    │ ModSecurity + CRS  │    │ firewall CRUD     │
  └───────────────────┘    └────────────────────┘    └───────────────────┘
```

### Key design decisions

- **YARA-X primary scanner** — Rust-based, fast, low memory. ClamAV CLI available for manual scans but the daemon (`clamd`) is not installed (it uses ~950MB RSS).
- **IP reputation via sorted int ranges + bisect** — O(log n) lookups instead of O(n) linear scan. Feeds like blocklist.de (500K+ entries) use ~30MB instead of ~150MB.
- **Bounded scan queue** — 50,000 max entries with backpressure to prevent unbounded memory growth during inotify storms.
- **Unix socket API** — not network-exposed by default. Panel communicates via `JabaliSecurityClient.php`.
- **CrowdSec as signal source** — community threat intelligence decisions streamed into the unified blocklist, not a standalone firewall.
- **PHP-FPM isolation is NOT managed here** — that's `jabali-isolator` (systemd-nspawn containers). This daemon only monitors.

## Requirements

- Linux with kernel 2.6.13+ (inotify support)
- Python 3.12+
- systemd (for service management)
- Optional: ClamAV (CLI scanner + freshclam for definitions)
- Optional: CrowdSec (installed automatically)
- Optional: nftables/iptables, UFW, nginx, ModSecurity + OWASP CRS

## CLI Reference

Full command reference: [docs/CLI.md](docs/CLI.md)

| Group | Commands |
|---|---|
| **Daemon** | `start`, `stop`, `restart`, `status`, `update` |
| **Scanning** | `scan`, `scan-full`, `scan-db`, `scan-rapid` |
| **Incidents** | `incidents list`, `incidents resolve` |
| **Quarantine** | `quarantine list`, `quarantine restore`, `quarantine delete` |
| **Config** | `config show`, `config set`, `config test` |
| **Rules** | `rules list`, `rules update` |
| **Users** | `user list`, `user risk` |
| **IP Blocking** | `block`, `unblock`, `blocklist` |
| **Brute-Force** | `bruteforce stats`, `bruteforce blocked`, `bruteforce whitelist-add`, `bruteforce whitelist-remove` |
| **WAF** | `waf events`, `waf rules`, `waf disable`, `waf enable`, `waf stats`, `waf update` |
| **Proactive** | `proactive status`, `proactive kills` |
| **Cleanup** | `cleanup records`, `cleanup file`, `cleanup cms` |
| **Threat Intel** | `threat-intel feeds`, `threat-intel update`, `threat-intel check-ip`, `threat-intel check-hash` |
| **WebShield** | `webshield status`, `webshield install`, `webshield uninstall`, `webshield rules` |
| **GeoIP** | `webshield geo-status`, `geo-list`, `geo-block`, `geo-unblock`, `geo-update-db` |
| **CrowdSec** | `crowdsec status`, `crowdsec decisions`, `crowdsec check`, `crowdsec unban` |
| **Attack Mode** | `attack-mode status`, `attack-mode enable`, `attack-mode disable` |
| **Firewall** | `firewall status`, `firewall enable`, `firewall disable`, `firewall reload`, `firewall allow`, `firewall deny`, `firewall delete-rule` |

## REST API

Socket: `/run/jabali-security/jabali-security.sock`
Authentication: `X-API-Key` header.
Full reference: [docs/API.md](docs/API.md)

| Endpoint | Method | Description |
|---|---|---|
| `/health` | GET | Health check |
| `/status` | GET | Daemon status and stats |
| `/incidents` | GET | List incidents |
| `/incidents/{id}` | GET | Get incident detail |
| `/scan` | POST | On-demand file or directory scan |
| `/scan/rapid` | POST | Fast parallel scan |
| `/scan/database` | POST | MySQL database scan |
| `/quarantine` | GET | List quarantined files |
| `/users` | GET | User risk scores |
| `/blocklist` | GET | Blocked IPs |
| `/config` | GET/PATCH | Configuration |
| `/bruteforce/*` | GET/POST/DELETE | Brute-force management |
| `/waf/*` | GET/POST | WAF events and rules |
| `/proactive/*` | GET | Process killer status and kills |
| `/cleanup/*` | GET/POST | Cleanup operations |
| `/threat-intel/*` | GET/POST | Threat intelligence |
| `/webshield/*` | GET/POST | WebShield management |
| `/firewall/ufw/*` | GET/POST/PUT/DELETE | UFW firewall rule management |
| `/crowdsec/*` | GET/DELETE | CrowdSec decisions and status |
| `/attack-mode` | GET/POST | Attack mode toggle |
| `/webshield/geo-*` | GET/POST/DELETE | GeoIP country blocking rules and database |

## Configuration

Config file: `/etc/jabali-security/jabali-security.conf`
Format: `KEY="value"` (one per line, `#` comments).

See [`etc/jabali-security.conf.example`](etc/jabali-security.conf.example) for all options with descriptions.
Full reference: [docs/CONFIGURATION.md](docs/CONFIGURATION.md)

## Development

```bash
uv sync                                    # install dependencies
uv run jabali-security start --foreground  # run daemon locally
uv run pytest tests/ -v                    # run 335 tests (~0.3s)
uv run ruff check .                        # lint
```

Full developer guide: [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md)

## Testing

```bash
uv run pytest tests/ -v
```

335 unit tests covering: config parsing, heuristic/entropy/YARA scanners, scoring engine, incident store, quarantine, response engine, behavior tracker, brute-force detection, IP reputation, log parsing, WebShield config, CMS detection, injection cleaning, UFW management, and CrowdSec integration.

### External Security Testing

```bash
./tests/test_security.sh jabali.site           # full test (with nmap)
./tests/test_security.sh jabali.site --quick   # skip port scan
```

Tests WebShield bot filtering, rate limiting, WAF blocking (SQLi/XSS/path traversal/command injection), WordPress hardening (user enum, XML-RPC, sensitive files), proactive defense (process killer), and dashboard/API exposure.

## Uninstall

```bash
sudo bash install.sh --uninstall
```

Completely removes the application, config, data, logs, and quarantine.

## Documentation

| Document | Description |
|---|---|
| [Architecture](docs/ARCHITECTURE.md) | System design, components, data flow, database schema |
| [API Reference](docs/API.md) | Full REST API documentation |
| [CLI Reference](docs/CLI.md) | All CLI commands with usage and examples |
| [Configuration](docs/CONFIGURATION.md) | Every config key with type, default, and description |
| [Development](docs/DEVELOPMENT.md) | Developer setup, project structure, how to extend |

## License

Proprietary
