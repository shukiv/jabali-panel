# GeoIP Blocking — WebShield Extension

## Objective

Add country-level IP blocking to jabali-security's WebShield module using MaxMind GeoLite2 databases. Admins can block/allow traffic by country code. The `.mmdb` database auto-downloads via MaxMind API and updates during `jabali-security update`.

## Architecture

GeoIP blocking integrates into WebShield as a new layer alongside bot filtering and rate limiting:

```
WebShield
├── Bot Filtering (UA patterns)     — on by default
├── Rate Limiting (req/s)           — off by default, attack mode enables
└── GeoIP Blocking (country codes)  — off by default, admin configures
```

**nginx integration**: Uses the `ngx_http_geoip2_module` (dynamic module for nginx). The config generator produces a `geoip2` directive in the `http {}` block and a `map` + `if` in the `server {}` block.

**Fallback**: If the nginx geoip2 module isn't available, GeoIP rules are applied at the daemon level — the CrowdSec/brute-force pipeline checks incoming IPs against the MaxMind database and blocks at the nftables level.

## Dependencies

- `maxminddb` Python package (pure Python reader, no C extension needed)
- MaxMind GeoLite2-Country database (`.mmdb` file, ~6MB)
- Optional: `libnginx-mod-http-geoip2` (nginx dynamic module for server-side blocking)

## Steps

### Step 1: Add maxminddb dependency + GeoIP database manager

**Files**: `pyproject.toml`, `lib/webshield/geoip.py` (new)

Add `maxminddb>=2.0` to `pyproject.toml` dependencies.

Create `lib/webshield/geoip.py` with:

```python
class GeoIPManager:
    def __init__(self, db_path: str, account_id: str = "", license_key: str = ""):
        ...

    def lookup(self, ip: str) -> str | None:
        """Return ISO country code for an IP, or None."""

    async def download_database(self) -> bool:
        """Download GeoLite2-Country.mmdb from MaxMind API."""

    def is_available(self) -> bool:
        """Check if the .mmdb file exists and is readable."""
```

Download URL: `https://download.maxmind.com/app/geoip_download?edition_id=GeoLite2-Country&license_key={key}&suffix=tar.gz`

Store at: `/var/lib/jabali-security/GeoLite2-Country.mmdb`

**Exit criteria**: `maxminddb` importable, `GeoIPManager.lookup("8.8.8.8")` returns `"US"` with a valid database.

---

### Step 2: Add config keys + GeoIP rules model

**Files**: `lib/config.py`, `lib/webshield/models.py`

New config keys in DEFAULTS:
```python
"GEOIP_ENABLED": "no",
"GEOIP_DB_PATH": "/var/lib/jabali-security/GeoLite2-Country.mmdb",
"GEOIP_MAXMIND_LICENSE_KEY": "",
"GEOIP_BLOCKED_COUNTRIES": "",       # comma-separated ISO codes, e.g. "CN,RU,KP"
"GEOIP_ALLOWED_COUNTRIES": "",       # if set, ONLY these countries allowed (whitelist mode)
"GEOIP_ACTION": "block",             # block, challenge, log
```

New model in `lib/webshield/models.py`:
```python
class GeoRule(BaseModel):
    country_code: str
    country_name: str
    action: str = "block"  # block, challenge, log
    enabled: bool = True
```

**Exit criteria**: `load_config()` parses all new keys. `GeoRule` model validates.

---

### Step 3: Integrate GeoIP into WebShield config generator

**Files**: `lib/webshield/config_generator.py`, `lib/webshield/manager.py`

Update `NginxConfigGenerator.__init__()` to accept `geoip_enabled`, `geoip_db_path`, `blocked_countries`, `geoip_action`.

In `generate_http_config()`, add (when geoip enabled):
```nginx
# GeoIP country detection
geoip2 /var/lib/jabali-security/GeoLite2-Country.mmdb {
    auto_reload 60m;
    $geoip2_country_code country iso_code;
}

# GeoIP action map
map $geoip2_country_code $jabali_geo_action {
    default 'pass';
    CN 'block';
    RU 'block';
}
```

In `generate_server_config()`, add:
```nginx
# GeoIP blocking
if ($jabali_geo_action = 'block') {
    return 403;
}
if ($jabali_geo_action = 'challenge') {
    return 503;
}
```

Update `WebShieldManager` constructor and `get_status()` to include geoip state.

**Exit criteria**: Generated nginx configs include geoip2 directives when enabled. `nginx -t` passes with the generated config (requires `libnginx-mod-http-geoip2`).

---

### Step 4: Add API endpoints for GeoIP management

**Files**: `api/routes/webshield.py`

New endpoints:
- `GET /api/v1/webshield/geo-status` — GeoIP database info (available, path, last updated, country count)
- `GET /api/v1/webshield/geo-rules` — List blocked/allowed countries with names
- `POST /api/v1/webshield/geo-rules` — Set blocked countries: `{"countries": ["CN","RU"], "action": "block"}`
- `DELETE /api/v1/webshield/geo-rules/{country_code}` — Remove a country rule
- `POST /api/v1/webshield/geo-update-db` — Download/update the MaxMind database

All endpoints update the config file and regenerate nginx configs.

**Exit criteria**: All endpoints respond correctly. Setting countries regenerates nginx config. Database download works with valid license key.

---

### Step 5: Add CLI commands

**Files**: `daemon/__main__.py`

New subcommands under `webshield`:
- `webshield geo-status` — Show database info
- `webshield geo-block <COUNTRY_CODE> [--action block|challenge|log]` — Block a country
- `webshield geo-unblock <COUNTRY_CODE>` — Remove country block
- `webshield geo-list` — List blocked countries
- `webshield geo-update-db --license-key KEY` — Download/update database

**Exit criteria**: All commands work via the daemon API.

---

### Step 6: Auto-download during install and update

**Files**: `install.sh`, `daemon/__main__.py` (update command)

In `install.sh` (after WebShield setup):
- If `GEOIP_MAXMIND_LICENSE_KEY` is set in config, download the database
- Install `libnginx-mod-http-geoip2` package (optional, `|| true`)

In the `update` command:
- If GeoIP is enabled and license key is configured, update the database (max once per 24h)

**Exit criteria**: Fresh install downloads the database if license key is provided. `jabali-security update` refreshes it.

---

### Step 7: Panel widget

**Files**: `panel/Widgets/GeoBlockTable.php` (new), `panel/Pages/Security.php`

Create a Filament table widget showing:
- Country code, country name, action (badge), enabled (icon)
- Header action: "Add Country" with country code input + action select
- Row actions: toggle enable/disable, delete
- Separate tab or section within the WebShield defense tab

**Exit criteria**: Panel shows GeoIP rules table within the Defense > WebShield section.

---

### Step 8: Tests + documentation

**Files**: `tests/test_geoip.py` (new), `docs/CLI.md`, `docs/API.md`, `docs/CONFIGURATION.md`, `README.md`

Tests:
- GeoIPManager lookup with mock database
- Config generation with geoip enabled/disabled
- API endpoint responses
- Country code validation (ISO 3166-1 alpha-2)

Documentation:
- Add GeoIP config keys to CONFIGURATION.md
- Add CLI commands to CLI.md
- Add API endpoints to API.md
- Update README features table

**Exit criteria**: All tests pass. 80%+ coverage on new code. Docs complete.

---

## Config Example

```ini
# -- GeoIP Blocking --
GEOIP_ENABLED="yes"
GEOIP_DB_PATH="/var/lib/jabali-security/GeoLite2-Country.mmdb"
GEOIP_MAXMIND_LICENSE_KEY="your_license_key_here"
GEOIP_BLOCKED_COUNTRIES="CN,RU,KP,IR"
GEOIP_ACTION="block"
```

## Dependency Order

```
Step 1 (maxminddb + manager)
  → Step 2 (config + model)
    → Step 3 (nginx config gen)  ─┐
    → Step 4 (API endpoints)     ─┤── parallel
    → Step 5 (CLI commands)      ─┘
      → Step 6 (install/update)
      → Step 7 (panel widget)
      → Step 8 (tests + docs)
```

Steps 3, 4, 5 can run in parallel after step 2.

## Risks

| Risk | Mitigation |
|------|-----------|
| `libnginx-mod-http-geoip2` not available on Debian Trixie | Fallback: daemon-level blocking via nftables (check IP on connection, not at nginx level) |
| MaxMind license key not configured | GeoIP disabled gracefully, clear message in panel |
| Large `.mmdb` file slows startup | Load lazily on first lookup, not at daemon init |
| Country code typos | Validate against ISO 3166-1 alpha-2 list |
