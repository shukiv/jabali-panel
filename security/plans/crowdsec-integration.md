# CrowdSec LAPI Integration Plan

## Architecture

jabali-security is the **central brain**. CrowdSec is a **signal source + community intelligence**.

```
CrowdSec Agent (reads logs, detects, shares with 200k+ installations)
    |
    v
LAPI (localhost:8080)
    |
    v
jabali-security CrowdSecClient (polls /v1/decisions/stream every 10s)
    |
    v
In-memory decision cache (dict[ip] -> list[Decision])
    |
    v
Scoring engine (CrowdSec weight + local detections + threat feeds)
    |
    v
Decision authority (block / rate-limit / quarantine / ignore)
```

## Phase 1: LAPI Client + Decision Sync

### New file: `lib/crowdsec/client.py`

```python
class CrowdSecClient:
    """Async LAPI bouncer client — polls decisions, caches in memory."""

    def __init__(self, lapi_url: str, api_key: str):
        self._url = lapi_url.rstrip("/")
        self._headers = {"X-Api-Key": api_key}
        self._session: aiohttp.ClientSession | None = None
        self._decisions: dict[str, list[dict]] = {}  # ip -> decisions

    async def start(self):
        self._session = aiohttp.ClientSession()
        # Full state dump on startup
        data = await self._poll_stream(startup=True)
        self._apply_stream(data)

    async def stop(self):
        if self._session:
            await self._session.close()

    async def run_sync_loop(self, interval: int = 10):
        """Background task: poll stream every N seconds."""
        await self.start()
        while True:
            await asyncio.sleep(interval)
            try:
                data = await self._poll_stream(startup=False)
                self._apply_stream(data)
            except Exception:
                logger.warning("CrowdSec LAPI poll failed")

    async def _poll_stream(self, startup: bool) -> dict:
        """GET /v1/decisions/stream"""
        params = {"startup": str(startup).lower()}
        async with self._session.get(
            f"{self._url}/v1/decisions/stream",
            headers=self._headers,
            params=params,
            timeout=aiohttp.ClientTimeout(total=5),
        ) as resp:
            if resp.status == 403:
                raise ValueError("Invalid CrowdSec bouncer API key")
            resp.raise_for_status()
            return await resp.json()

    def _apply_stream(self, data: dict):
        """Apply new/deleted decisions to in-memory cache."""
        for d in (data.get("new") or []):
            ip = d.get("value", "")
            self._decisions.setdefault(ip, []).append(d)
        for d in (data.get("deleted") or []):
            ip = d.get("value", "")
            self._decisions.pop(ip, None)

    def check_ip(self, ip: str) -> list[dict]:
        """Check if IP has active CrowdSec decisions (O(1) lookup)."""
        return self._decisions.get(ip, [])

    async def query_ip(self, ip: str) -> list[dict] | None:
        """Query LAPI directly for a specific IP."""
        async with self._session.get(
            f"{self._url}/v1/decisions",
            headers=self._headers,
            params={"ip": ip},
            timeout=aiohttp.ClientTimeout(total=5),
        ) as resp:
            if resp.status == 200:
                return await resp.json()
            return None

    @property
    def active_decisions_count(self) -> int:
        return sum(len(v) for v in self._decisions.values())

    @property
    def blocked_ips(self) -> list[str]:
        return list(self._decisions.keys())
```

### LAPI Protocol Details

**Authentication**: `X-Api-Key` header with bouncer key from `cscli bouncers add jabali-security -o raw`

**Endpoints** (bouncer has read-only access):
- `GET /v1/decisions/stream?startup=true` — full state dump (initial)
- `GET /v1/decisions/stream` — delta since last poll (LAPI tracks LastPull server-side)
- `GET /v1/decisions?ip=1.2.3.4` — query specific IP

**Decision object**:
```json
{
  "id": 1023,
  "origin": "crowdsec",      // "crowdsec" | "cscli" | "CAPI" | "lists"
  "type": "ban",             // "ban" | "captcha" | custom
  "scope": "Ip",             // "Ip" | "Range"
  "value": "1.2.3.4",        // the IP or CIDR
  "duration": "4h0m0s",      // Go duration format
  "scenario": "crowdsecurity/ssh-bf"
}
```

**Stream response**: `{"new": [...], "deleted": [...]}` — null arrays when no changes.

**Poll interval**: 10 seconds (same as official bouncers).

### Config keys to add (`lib/config.py` DEFAULTS + dataclass)

```python
"CROWDSEC_ENABLED": "auto",        # "auto" (detect LAPI), "yes", "no"
"CROWDSEC_LAPI_URL": "http://127.0.0.1:8080",
"CROWDSEC_BOUNCER_KEY": "",
"CROWDSEC_SYNC_INTERVAL": "10",    # seconds between polls
```

### Registry integration (`lib/registry.py`)

- Add `crowdsec: CrowdSecClient | None` to ComponentRegistry
- Builder: `_build_crowdsec(config)` — returns None if disabled or no key
- `auto` mode: try connecting to LAPI, enable if responsive
- Background task: `crowdsec.run_sync_loop(interval=config.crowdsec_sync_interval)`
- Populate app: `app["crowdsec"] = self.crowdsec`

### API route: `api/routes/crowdsec.py`

```
GET  /api/v1/crowdsec/status         — connected, decision count, last poll
GET  /api/v1/crowdsec/decisions      — all active decisions
GET  /api/v1/crowdsec/check/{ip}     — check specific IP
POST /api/v1/crowdsec/sync           — trigger immediate poll
```

## Phase 2: Scoring Integration

### CrowdSec scenario weights

Map CrowdSec scenarios to scoring weights in the unified scoring engine:

| Scenario pattern | Weight | Rationale |
|---|---|---|
| `*/ssh-bf*` | 60 | Confirmed brute force |
| `*/http-sqli*`, `*/http-xss*` | 70 | Active exploitation |
| `*/http-backdoors*` | 80 | Webshell/backdoor access |
| `*/http-probing` | 30 | Reconnaissance (lower confidence) |
| `*/http-sensitive-files` | 50 | Config file access attempts |
| `*/http-bf-wordpress*` | 60 | CMS brute force |
| `*/postfix-spam`, `*/dovecot-spam` | 60 | Mail abuse |
| Origin: `CAPI` (community) | +20 bonus | Confirmed by multiple installations |

### Integration with existing brute-force detector

In `_on_auth_event` callback:
```python
decision = detector.record(event)
# Enrich with CrowdSec context
if crowdsec:
    cs_decisions = crowdsec.check_ip(event.ip)
    if cs_decisions:
        # Lower threshold for known attackers
        # Or: skip progressive blocking, go straight to longer ban
```

### Integration with threat intel check_ip

In `FeedManager.check_ip()` or a new unified `check_ip_all()`:
```python
# Existing feeds
result = self._ip_db.check(ip)
# CrowdSec enrichment
if crowdsec:
    cs = crowdsec.check_ip(ip)
    if cs:
        result.feeds.append("crowdsec")
        result.score += scenario_weight(cs[0]["scenario"])
```

## Phase 3: Installer

### CrowdSec install block (after ClamAV in install.sh)

```bash
# -- CrowdSec (community threat intelligence) --
if ! command -v cscli &>/dev/null; then
    run_with_spinner "Installing CrowdSec" bash -c '
        curl -fsSL https://install.crowdsec.net | bash
        DEBIAN_FRONTEND=noninteractive apt-get install -y -qq \
            crowdsec crowdsec-firewall-bouncer-nftables 2>/dev/null
    '
fi

# Install hosting-relevant collections
if command -v cscli &>/dev/null; then
    cscli collections install crowdsecurity/linux 2>/dev/null
    cscli collections install crowdsecurity/sshd 2>/dev/null
    cscli collections install crowdsecurity/nginx 2>/dev/null
    cscli collections install crowdsecurity/base-http-scenarios 2>/dev/null
    cscli collections install crowdsecurity/postfix 2>/dev/null
    cscli collections install crowdsecurity/dovecot 2>/dev/null
fi
```

### Bouncer key generation

```bash
if command -v cscli &>/dev/null; then
    bouncer_key=$(cscli bouncers add jabali-security -o raw 2>/dev/null || echo "")
    if [ -n "$bouncer_key" ]; then
        # Write to config (same pattern as API_KEY generation)
    fi
fi
```

### systemd weak dependency

```ini
After=network.target crowdsec.service
Wants=crowdsec.service
```

## Hosting-Relevant CrowdSec Scenarios

| Category | Scenarios |
|---|---|
| **SSH** | ssh-bf, ssh-slow-bf, ssh-bf_user-enum, ssh-cve-2024-6387 |
| **HTTP** | http-probing, http-sensitive-files, http-sqli-probing, http-xss-probing, http-backdoors-attempts, http-path-traversal-probing |
| **WordPress** | http-bf-wordpress_bf, http-bf-wordpress_bf_xmlrpc, http-wordpress-scan, http-wordpress_user-enum |
| **Mail** | postfix-spam, postfix-relay-denied, dovecot-spam |
| **FTP** | proftpd-bf, vsftpd-bf |
| **ModSec** | modsecurity (integrates with our WAF logs) |
| **Nginx** | nginx-req-limit-exceeded |

## Current Architecture Gaps Found

1. **THREAT_INTEL_AUTO_BLOCK is defined but never implemented** — config keys exist but no code path actually auto-blocks IPs from feeds
2. **No correlation between subsystems** — brute-force, file scanning, WAF, and threat intel operate independently
3. **IP scoring is binary** (blocked or not) — no progressive scoring like file scanning has

## Files to Create/Modify

| File | Action |
|---|---|
| `lib/crowdsec/__init__.py` | Create — package |
| `lib/crowdsec/client.py` | Create — LAPI bouncer client |
| `lib/crowdsec/models.py` | Create — Decision, CrowdSecStatus models |
| `lib/config.py` | Modify — add CROWDSEC_* keys |
| `lib/registry.py` | Modify — add crowdsec component |
| `api/routes/crowdsec.py` | Create — REST endpoints |
| `api/routes/__init__.py` | Modify — register crowdsec routes |
| `install.sh` | Modify — optional CrowdSec install |
| `etc/jabali-security.conf.example` | Modify — add CrowdSec config section |
| `etc/jabali-security.service` | Modify — add Wants=crowdsec.service |
| `daemon/__main__.py` | Modify — add crowdsec CLI commands |
| `panel/Pages/Security.php` | Modify — add CrowdSec status to overview |
