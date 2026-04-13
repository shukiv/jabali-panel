"""CrowdSec LAPI bouncer client — polls decisions, caches in memory."""

from __future__ import annotations

import asyncio
import logging
from datetime import datetime, timezone

import aiohttp

from lib.crowdsec.models import CrowdSecDecision

logger = logging.getLogger(__name__)

# Map CrowdSec scenario patterns to scoring weights
SCENARIO_WEIGHTS: dict[str, int] = {
    "ssh-bf": 60,
    "ssh-slow-bf": 50,
    "ssh-bf_user-enum": 40,
    "http-sqli": 70,
    "http-xss": 70,
    "http-backdoors": 80,
    "http-probing": 30,
    "http-sensitive-files": 50,
    "http-path-traversal": 60,
    "http-bf-wordpress": 60,
    "http-wordpress-scan": 50,
    "modsecurity": 50,
}


def scenario_weight(scenario: str) -> int:
    """Map a CrowdSec scenario name to a scoring weight."""
    for pattern, weight in SCENARIO_WEIGHTS.items():
        if pattern in scenario:
            return weight
    return 40  # Default weight for unknown scenarios


class CrowdSecClient:
    """Async LAPI bouncer client — polls decisions, caches in memory."""

    def __init__(self, lapi_url: str, api_key: str) -> None:
        self._url = lapi_url.rstrip("/")
        self._headers = {"X-Api-Key": api_key}
        self._session: aiohttp.ClientSession | None = None
        self._decisions: dict[str, list[CrowdSecDecision]] = {}
        self._last_poll = ""
        self._connected = False
        self._error = ""

    async def start(self) -> None:
        """Initialize session and do full state dump."""
        self._session = aiohttp.ClientSession()
        try:
            data = await self._poll_stream(startup=True)
            self._apply_stream(data)
            self._connected = True
            self._error = ""
            logger.info(
                "CrowdSec LAPI connected: %d active decisions",
                self.active_decisions_count,
            )
        except Exception as exc:
            self._connected = False
            self._error = str(exc)
            logger.warning("CrowdSec LAPI initial connect failed: %s", exc)

    async def stop(self) -> None:
        if self._session:
            await self._session.close()
            self._session = None

    async def run_sync_loop(self, interval: int = 10) -> None:
        """Background task: poll LAPI stream every N seconds."""
        await self.start()
        while True:
            await asyncio.sleep(interval)
            try:
                data = await self._poll_stream(startup=False)
                self._apply_stream(data)
                self._connected = True
                self._error = ""
            except Exception as exc:
                self._connected = False
                self._error = str(exc)
                logger.debug("CrowdSec LAPI poll failed: %s", exc)

    async def _poll_stream(self, startup: bool) -> dict:
        """GET /v1/decisions/stream."""
        if not self._session:
            raise RuntimeError("CrowdSec client not started")
        params = {"startup": str(startup).lower()}
        async with self._session.get(
            "%s/v1/decisions/stream" % self._url,
            headers=self._headers,
            params=params,
            timeout=aiohttp.ClientTimeout(total=5),
        ) as resp:
            if resp.status == 403:
                raise ValueError("Invalid CrowdSec bouncer API key")
            resp.raise_for_status()
            return await resp.json()

    def _apply_stream(self, data: dict) -> None:
        """Apply new/deleted decisions to in-memory cache."""
        now = datetime.now(timezone.utc).isoformat()
        for raw in data.get("new") or []:
            ip = raw.get("value", "").split("/")[0]  # Strip /32 suffix
            if not ip:
                continue
            decision = CrowdSecDecision(
                id=raw.get("id", 0),
                origin=raw.get("origin", ""),
                type=raw.get("type", "ban"),
                scope=raw.get("scope", "Ip"),
                value=raw.get("value", ""),
                duration=raw.get("duration", ""),
                scenario=raw.get("scenario", ""),
            )
            self._decisions.setdefault(ip, []).append(decision)
        for raw in data.get("deleted") or []:
            ip = raw.get("value", "").split("/")[0]
            self._decisions.pop(ip, None)
        self._last_poll = now

    def check_ip(self, ip: str) -> list[CrowdSecDecision]:
        """Check if IP has active CrowdSec decisions (O(1) lookup)."""
        return self._decisions.get(ip, [])

    def check_ip_score(self, ip: str) -> int:
        """Return a combined score for an IP based on active decisions."""
        decisions = self.check_ip(ip)
        if not decisions:
            return 0
        score = 0
        for d in decisions:
            w = scenario_weight(d.scenario)
            # Community signals (CAPI) get a confidence bonus
            if d.origin == "CAPI":
                w += 20
            score = max(score, w)
        return min(score, 100)

    async def query_ip(self, ip: str) -> list[dict] | None:
        """Query LAPI directly for a specific IP (live, not cached)."""
        if not self._session:
            return None
        try:
            async with self._session.get(
                "%s/v1/decisions" % self._url,
                headers=self._headers,
                params={"ip": ip},
                timeout=aiohttp.ClientTimeout(total=5),
            ) as resp:
                if resp.status == 200:
                    return await resp.json()
                return None
        except Exception:
            return None

    async def block_ip(self, ip: str, duration: int, reason: str) -> bool:
        """Block an IP via cscli decisions add (list args, no shell).

        Duration in seconds, 0 = permanent (mapped to 1 year).
        IP is validated by caller before reaching this method.
        """
        import shutil
        cscli = shutil.which("cscli")
        if not cscli:
            return False
        dur_str = "%ds" % duration if duration > 0 else "8760h"
        from lib.privilege import sudo_prefix
        cmd = [*sudo_prefix(), cscli, "decisions", "add", "--ip", ip, "--duration", dur_str,
               "--reason", reason, "--type", "ban"]
        try:
            proc = await asyncio.create_subprocess_exec(
                *cmd,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
            )
            _, stderr = await asyncio.wait_for(proc.communicate(), timeout=10)
            if proc.returncode == 0:
                logger.info("CrowdSec: blocked %s for %s (%s)", ip, dur_str, reason)
                return True
            logger.warning("CrowdSec block failed for %s: %s", ip, stderr.decode().strip())
            return False
        except Exception as exc:
            logger.warning("CrowdSec block_ip error: %s", exc)
            return False

    async def unblock_ip(self, ip: str) -> bool:
        """Unblock an IP via cscli decisions delete (list args, no shell)."""
        import shutil
        cscli = shutil.which("cscli")
        if not cscli:
            return False
        from lib.privilege import sudo_prefix
        try:
            proc = await asyncio.create_subprocess_exec(
                *sudo_prefix(), cscli, "decisions", "delete", "--ip", ip,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
            )
            await asyncio.wait_for(proc.communicate(), timeout=10)
            if proc.returncode == 0:
                self._decisions.pop(ip, None)
                return True
            return False
        except Exception:
            return False

    @property
    def active_decisions_count(self) -> int:
        return sum(len(v) for v in self._decisions.values())

    @property
    def blocked_ips(self) -> list[str]:
        return list(self._decisions.keys())

    @property
    def connected(self) -> bool:
        return self._connected

    @property
    def last_poll(self) -> str:
        return self._last_poll

    @property
    def error(self) -> str:
        return self._error

    @property
    def local_decisions_count(self) -> int:
        """Count decisions from local detection only (not CAPI community)."""
        return sum(
            1 for decisions in self._decisions.values()
            for d in decisions if d.origin != "CAPI"
        )

    def get_all_decisions(self) -> list[dict]:
        """Return all active decisions as dicts (for API responses)."""
        result = []
        for ip, decisions in self._decisions.items():
            for d in decisions:
                result.append(d.model_dump())
        return result
