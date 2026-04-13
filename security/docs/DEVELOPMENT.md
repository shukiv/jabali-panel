# Development Guide

## Setup

### Prerequisites

- Python 3.12+
- [uv](https://docs.astral.sh/uv/) (recommended) or pip
- Linux (inotify required for file watching)

### Install dependencies

```bash
cd jabali-security
uv sync
```

This installs all production and dev dependencies (pytest, pytest-asyncio, ruff).

### Run locally

```bash
# Start daemon in foreground (logs to stderr)
uv run jabali-security start --foreground

# With custom config
uv run jabali-security start --foreground --config etc/jabali-security.conf.example

```

When running as non-root, config/data/logs use `~/.config/jabali-security/` instead of system paths.

### Run a scan

```bash
uv run jabali-security scan /path/to/file.php
uv run jabali-security scan /path/to/directory -r --json
```

---

## Project Structure

```
jabali-security/
+-- daemon/                 # CLI entry point + async daemon
|   +-- __main__.py         # Click CLI (30+ commands)
|   +-- server.py           # SecurityDaemon supervisor
+-- lib/                    # Core library modules
|   +-- config.py           # KEY=VALUE parser, JabaliConfig dataclass
|   +-- constants.py        # Version, paths, app name
|   +-- models.py           # Pydantic v2 data models
|   +-- filter.py           # PreFilter (extension, size, skip dirs)
|   +-- scoring.py          # ScoringEngine (aggregate findings -> action)
|   +-- response.py         # ResponseEngine (execute quarantine/suspend)
|   +-- incidents.py        # IncidentStore (SQLite, schema, CRUD)
|   +-- quarantine.py       # QuarantineManager (move/restore/delete)
|   +-- queue.py            # ScanQueue (async queue)
|   +-- registry.py         # ComponentRegistry (build + lifecycle)
|   +-- privilege.py        # Sudo helpers for privilege separation
|   +-- behavior_tracker.py # File lifecycle tracking
|   +-- process_monitor.py  # /proc polling for suspicious processes
|   +-- tenant.py           # File path -> hosting account mapper
|   +-- notify.py           # Email + webhook notifications
|   +-- hash_cache.py       # Persistent scan result cache
|   +-- rapidscan.py        # Parallel directory scanner
|   +-- log_tailer.py       # AsyncLogTailer (reusable async log file tailer)
|   +-- system_tools.py     # System utilities (freshclam, etc.)
|   +-- scanner/            # Detection engines
|   |   +-- base.py         # ScannerBase abstract class
|   |   +-- heuristic.py    # Regex pattern matching
|   |   +-- entropy.py      # Shannon entropy analysis
|   |   +-- yara_engine.py  # YARA-X signature scanning
|   |   +-- clamav.py       # ClamAV clamd integration (optional, auto-detected)
|   |   +-- database.py     # MySQL database scanner
|   |   +-- __init__.py     # ScanOrchestrator
|   +-- bruteforce/         # Brute-force protection
|   |   +-- detector.py     # Sliding window detector
|   |   +-- firewall.py     # nftables/iptables manager
|   |   +-- log_parser.py   # Auth log tail + regex parsing
|   |   +-- models.py       # Auth event models
|   +-- waf/                # WAF integration
|   |   +-- audit_log_parser.py  # ModSecurity audit log parser
|   |   +-- rule_manager.py      # CRS rule management
|   +-- proactive/          # Proactive defense
|   |   +-- process_killer.py    # Suspicious process killer
|   +-- cleanup/            # Malware cleanup
|   |   +-- engine.py       # Cleanup orchestrator
|   |   +-- cms_cleaner.py  # CMS integrity checker
|   |   +-- injection_patterns.py  # Injection regex patterns
|   |   +-- scheduler.py    # Scheduled scan runner
|   |   +-- models.py       # Cleanup record models
|   +-- threat_intel/       # Threat intelligence
|   |   +-- feed_manager.py # Feed download, cache, lookup
|   |   +-- ip_reputation.py  # Sorted int ranges + bisect IP lookup
|   |   +-- hash_reputation.py # Known-bad hash set
|   +-- crowdsec/           # CrowdSec integration
|   |   +-- client.py       # Async LAPI bouncer client
|   |   +-- models.py       # CrowdSecDecision, CrowdSecStatus
|   +-- webshield/          # WebShield + GeoIP
|   |   +-- manager.py      # Nginx config generation (bot filtering, rate limiting)
|   |   +-- geoip.py        # MaxMind GeoIP manager (lookup, download, nginx config)
|   |   +-- config_generator.py # WebShield nginx config generation
|   +-- ufw/                # UFW firewall management
|   |   +-- manager.py      # UFW CLI wrapper (async subprocess)
|   |   +-- validators.py   # Input validation for rules
|   |   +-- models.py       # Pydantic models for rules/status
|   +-- watcher/            # File watching
|       +-- inotify.py      # inotify wrapper
+-- api/                    # REST API
|   +-- app.py              # aiohttp application factory
|   +-- middleware.py       # API key auth, request logging
|   +-- routes/             # Route handlers split by domain
|       +-- __init__.py     # Route registration (imports all sub-routers)
|       +-- core.py         # Health, status
|       +-- incidents.py    # Incident CRUD
|       +-- scanning.py     # On-demand, full, database, rapid scan
|       +-- quarantine.py   # Quarantine list, restore, delete
|       +-- users.py        # User risk scores
|       +-- blocking.py     # IP block/unblock
|       +-- config.py       # Config get/patch
|       +-- rules.py        # Rule management
|       +-- bruteforce.py   # Brute-force endpoints
|       +-- waf.py          # WAF endpoints
|       +-- proactive.py    # Proactive defense endpoints
|       +-- cleanup.py      # Cleanup endpoints
|       +-- threat_intel.py # Threat intel endpoints
|       +-- webshield.py    # WebShield endpoints
|       +-- ufw.py          # UFW firewall management endpoints
|       +-- crowdsec.py     # CrowdSec status, decisions, IP check
|       +-- attack_mode.py  # Attack mode enable/disable
|       +-- helpers.py      # Shared response helpers
+-- panel/                  # Jabali Panel plugin (Filament v5)
|   +-- JabaliSecurityPlugin.php   # Plugin registration
|   +-- JabaliSecurityClient.php   # Unix socket API client
|   +-- Pages/Security.php         # Main security page (5 tabs)
|   +-- Widgets/                   # Filament table widgets
|   +-- views/security.blade.php   # Blade view template
+-- rules/                  # YARA-X rule files (.yar)
+-- etc/                    # Config + systemd service files
|   +-- jabali-security.conf.example
|   +-- jabali-security.service
+-- tests/                  # pytest test suite (366 tests)
+-- scripts/                # Build scripts
|   +-- build-deb.sh        # .deb package builder
+-- debian/                 # .deb packaging files
+-- pyproject.toml          # Project metadata, dependencies, tool config
```

---

## How to Add a New Scanner

All scanners implement the `ScannerBase` abstract class in `lib/scanner/base.py`.

### 1. Create the scanner module

Create `lib/scanner/my_scanner.py`:

```python
from __future__ import annotations

from lib.models import Finding
from lib.scanner.base import ScannerBase


class MyScanner(ScannerBase):
    name = "my_scanner"

    def __init__(self, enabled: bool = True) -> None:
        self._enabled = enabled

    @property
    def enabled(self) -> bool:
        return self._enabled

    async def scan(self, path: str, content: bytes) -> list[Finding]:
        findings: list[Finding] = []
        # Your detection logic here
        # Append Finding objects with scanner name, rule, score, description
        return findings
```

### 2. Register in the orchestrator

Edit `lib/scanner/__init__.py` -- add the import and instantiation in `ScanOrchestrator.__init__()`:

```python
from lib.scanner.my_scanner import MyScanner

# In __init__:
if config.my_scanner_enabled:
    self._scanners.append(MyScanner())
```

### 3. Add config key

Add to `lib/config.py`:
- Add `"MY_SCANNER_ENABLED": "no"` to the `DEFAULTS` dict
- Add `my_scanner_enabled: bool = False` to the `JabaliConfig` dataclass
- Add `my_scanner_enabled=_bool(merged["MY_SCANNER_ENABLED"])` in `load_config()`

### 4. Add to config example

Add the key with a comment to `etc/jabali-security.conf.example`.

### 5. Write tests

Add `tests/test_my_scanner.py` with tests for both clean and malicious content.

---

## How to Add a New Feature Module

For larger features (like brute-force or WAF), the pattern is:

### 1. Create a module directory

```
lib/my_feature/
    __init__.py
    my_component.py
    models.py       # (if needed)
```

### 2. Add config keys

Follow the same pattern as adding a scanner (DEFAULTS, JabaliConfig, load_config, conf.example).

### 3. Register in ComponentRegistry

Edit `lib/registry.py`:
- Import the new component
- Add it as an optional field on `ComponentRegistry`
- Add a `_build_my_feature()` private function
- Call it in `build()` gated on the config flag
- Expose it via `populate_app()` if the API needs it

### 4. Add API endpoints

Create a new route module `api/routes/my_feature.py` with a `setup_routes(app)` function. Add the import to `api/routes/__init__.py` in the `_MODULES` tuple.

### 5. Add CLI commands

Add a click group or commands in `daemon/__main__.py`.

### 6. Write tests

Add tests covering the core logic, edge cases, and error handling.

---

## Testing

### Running tests

```bash
# All tests
uv run pytest tests/ -v

# Single test file
uv run pytest tests/test_scoring.py -v

# Single test
uv run pytest tests/test_scoring.py::test_score_aggregation -v

# With coverage (if installed)
uv run pytest tests/ --cov=lib --cov-report=term-missing
```

### Test configuration

From `pyproject.toml`:
- Test directory: `tests/`
- Python path includes project root (`.`)
- Async mode: `auto` (pytest-asyncio)
- Security assertions (`S101`) allowed in tests

### What to test

- **Unit tests** for all detection logic (scanners, scoring, filtering)
- **Edge cases**: empty files, binary files, huge files, symlinks
- **Config parsing**: defaults, overrides, invalid values, boundary values
- **Database operations**: CRUD, concurrent access, schema migrations
- **Input validation**: malformed IPs, SQL injection attempts, path traversal

### Fixtures

Shared fixtures in `tests/conftest.py`:

| Fixture | Description |
|---|---|
| `tmp_config` | Temp config file with test overrides |
| `sample_config` | `JabaliConfig` with defaults |
| `sample_php_webshell` | Malicious PHP content for scanner tests |
| `sample_clean_php` | Clean PHP content for false-positive tests |

---

## Code Style

### Linting

```bash
uv run ruff check .
uv run ruff check . --fix   # auto-fix
```

Configuration in `pyproject.toml`:
- Target: Python 3.12
- Line length: 120
- Rules: E (pycodestyle), F (pyflakes), W (warnings), I (isort), S (bandit security)
- `E501` (line length) is ignored
- `S101` (assert) is allowed in tests

### Conventions

- No `shell=True` in subprocess calls -- always use list args
- Use `import yara_x` not `import yara`
- YARA-X API: `yara_x.Compiler()`, `yara_x.Scanner(rules)`, `results.matching_rules`
- Config format: `KEY="value"` (gniza4linux pattern)
- Async throughout the daemon (asyncio, aiohttp, aiosqlite)
- Pydantic v2 for data models
- Type hints on all function signatures
- Use `trash` instead of `rm`/`rmdir` for file deletion

---

## Release Process

### Build .deb package

```bash
./scripts/build-deb.sh
```

This creates a `.deb` package in the `build/` directory containing:
- Application files in `/usr/local/jabali-security/`
- systemd service file
- CLI wrapper script
- YARA rules
- Config example

### Version bumping

Update the version in `pyproject.toml` (`version = "X.Y.Z"`) and `lib/constants.py` (`VERSION = "X.Y.Z"`).

### Deployment

```bash
# Via install script (from Git)
curl -fsSL https://git.linux-hosting.co.il/shukivaknin/jabali-security/raw/branch/master/install.sh | sudo bash

# Via .deb package
sudo dpkg -i jabali-security_X.Y.Z_amd64.deb
```
