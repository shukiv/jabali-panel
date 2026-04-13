"""Threat intelligence feed manager -- download and update feeds."""
from __future__ import annotations

import asyncio
import json
import logging
from datetime import datetime, timezone
from pathlib import Path
from urllib.request import Request, urlopen

from lib.threat_intel.hash_reputation import HashReputationDB
from lib.threat_intel.ip_reputation import IPReputationDB
from lib.threat_intel.models import FeedStatus, ReputationResult

logger = logging.getLogger(__name__)

# Built-in feed sources (all free, no API key required)
_IP_FEEDS: dict[str, str] = {
    "spamhaus_drop": "https://www.spamhaus.org/drop/drop.txt",
    "blocklist_de_all": "https://lists.blocklist.de/lists/all.txt",
    "tor_exit_nodes": "https://check.torproject.org/torbulkexitlist",
}

_HASH_FEEDS: dict[str, str] = {
    # MalwareBazaar recent SHA-256 hashes (last 1 hour)
    "malwarebazaar_recent": "https://mb-api.abuse.ch/api/v1/",
}


class FeedManager:
    """Download, cache, and query threat intelligence feeds."""

    def __init__(self, data_dir: str, enabled_feeds: list[str] | None = None) -> None:
        self._data_dir = Path(data_dir) / "threat_intel"
        self._data_dir.mkdir(parents=True, exist_ok=True)
        self._enabled_feeds = set(enabled_feeds) if enabled_feeds else set(_IP_FEEDS.keys())
        self._ip_db = IPReputationDB()
        self._hash_db = HashReputationDB()
        self._feed_status: dict[str, FeedStatus] = {}
        self._load_cached_feeds()

    def _load_cached_feeds(self) -> None:
        """Load previously downloaded feeds from cache directory."""
        for name in _IP_FEEDS:
            if name not in self._enabled_feeds:
                continue
            cache_file = self._data_dir / ("%s.txt" % name)
            if cache_file.is_file():
                count = self._ip_db.load_feed(name, cache_file)
                self._feed_status[name] = FeedStatus(
                    name=name, source_url=_IP_FEEDS[name],
                    entry_count=count, feed_type="ip",
                    last_update=datetime.fromtimestamp(cache_file.stat().st_mtime, tz=timezone.utc),
                )

        # Load hash feeds
        hash_cache = self._data_dir / "malwarebazaar_hashes.txt"
        if hash_cache.is_file():
            count = self._hash_db.load_feed(hash_cache)
            self._feed_status["malwarebazaar_recent"] = FeedStatus(
                name="malwarebazaar_recent", source_url=_HASH_FEEDS.get("malwarebazaar_recent", ""),
                entry_count=count, feed_type="hash",
                last_update=datetime.fromtimestamp(hash_cache.stat().st_mtime, tz=timezone.utc),
            )

    async def update_all(self) -> dict[str, bool]:
        """Download all enabled feeds. Returns {feed_name: success}."""
        results: dict[str, bool] = {}
        for name, url in _IP_FEEDS.items():
            if name not in self._enabled_feeds:
                continue
            success = await self._download_feed(name, url, "ip")
            results[name] = success

        if "malwarebazaar_recent" in self._enabled_feeds:
            success = await self._download_malwarebazaar_hashes()
            results["malwarebazaar_recent"] = success

        return results

    async def _download_feed(self, name: str, url: str, feed_type: str) -> bool:
        """Download a feed file. Returns success."""
        cache_file = self._data_dir / ("%s.txt" % name)
        try:
            content = await asyncio.to_thread(self._http_get, url)
            if not content:
                return False
            cache_file.write_text(content, encoding="utf-8")
            count = self._ip_db.load_feed(name, cache_file)
            self._feed_status[name] = FeedStatus(
                name=name, source_url=url, entry_count=count,
                feed_type=feed_type, last_update=datetime.now(timezone.utc),
            )
            logger.info("Updated feed %s: %d entries", name, count)
            return True
        except Exception:
            logger.exception("Failed to update feed %s", name)
            return False

    async def _download_malwarebazaar_hashes(self) -> bool:
        """Download recent malware hashes from MalwareBazaar."""
        cache_file = self._data_dir / "malwarebazaar_hashes.txt"
        try:
            data = "query=get_recent&selector=time".encode()

            def _fetch():
                req = Request(_HASH_FEEDS["malwarebazaar_recent"], data=data, method="POST")  # noqa: S310
                req.add_header("Content-Type", "application/x-www-form-urlencoded")
                with urlopen(req, timeout=30) as resp:  # noqa: S310
                    raw = resp.read(50 * 1024 * 1024 + 1)
                    if len(raw) > 50 * 1024 * 1024:
                        raise ValueError("MalwareBazaar response too large")
                    return json.loads(raw.decode())

            result = await asyncio.to_thread(_fetch)
            if result.get("query_status") != "ok":
                return False
            hashes = []
            for entry in result.get("data", []):
                h = entry.get("sha256_hash", "")
                if h:
                    hashes.append(h)
            if hashes:
                cache_file.write_text("\n".join(hashes) + "\n", encoding="utf-8")
                count = self._hash_db.load_feed(cache_file)
                self._feed_status["malwarebazaar_recent"] = FeedStatus(
                    name="malwarebazaar_recent",
                    source_url=_HASH_FEEDS["malwarebazaar_recent"],
                    entry_count=count, feed_type="hash",
                    last_update=datetime.now(timezone.utc),
                )
                logger.info("Updated MalwareBazaar feed: %d hashes", count)
            return True
        except Exception:
            logger.exception("Failed to update MalwareBazaar feed")
            return False

    _MAX_FEED_SIZE = 50 * 1024 * 1024  # 50MB

    @staticmethod
    def _http_get(url: str, max_size: int = 50 * 1024 * 1024) -> str:
        """Download URL content with size limit. Synchronous -- run in executor."""
        req = Request(url, headers={"User-Agent": "jabali-security"})  # noqa: S310
        with urlopen(req, timeout=30) as resp:  # noqa: S310
            data = resp.read(max_size + 1)
            if len(data) > max_size:
                raise ValueError("Feed response exceeds %d bytes" % max_size)
            return data.decode("utf-8", errors="replace")

    def check_ip(self, ip: str) -> ReputationResult:
        """Check an IP against all loaded feeds."""
        matches = self._ip_db.check(ip)
        return ReputationResult(
            entity=ip, entity_type="ip",
            is_malicious=len(matches) > 0,
            score=min(100, len(matches) * 30),
            feeds=matches,
        )

    def check_hash(self, sha256: str) -> ReputationResult:
        """Check a hash against the local known-bad database."""
        is_bad = self._hash_db.check_local(sha256)
        return ReputationResult(
            entity=sha256, entity_type="hash",
            is_malicious=is_bad,
            score=80 if is_bad else 0,
            feeds=["malwarebazaar_recent"] if is_bad else [],
        )

    async def check_hash_remote(self, sha256: str) -> ReputationResult:
        """Check a hash against remote APIs (slower, use sparingly)."""
        local = self.check_hash(sha256)
        if local.is_malicious:
            return local
        remote = await self._hash_db.check_remote(sha256)
        if remote:
            return ReputationResult(
                entity=sha256, entity_type="hash",
                is_malicious=True, score=90,
                feeds=["malwarebazaar_api"],
                details=remote,
            )
        return local

    async def run_periodic_updates(self, interval_hours: int = 6) -> None:
        """Background task: update feeds on startup, then every N hours."""
        logger.info("Threat intel feed updater started (interval=%dh)", interval_hours)
        # Initial update on startup if no cached data
        if not any(fs.entry_count > 0 for fs in self._feed_status.values()):
            logger.info("No cached feeds found, pulling initial data...")
            results = await self.update_all()
            success = sum(1 for v in results.values() if v)
            logger.info("Initial feed update: %d/%d succeeded", success, len(results))
        while True:
            await asyncio.sleep(interval_hours * 3600)
            logger.info("Updating threat intelligence feeds...")
            results = await self.update_all()
            success = sum(1 for v in results.values() if v)
            logger.info("Feed update complete: %d/%d succeeded", success, len(results))

    @property
    def feed_statuses(self) -> list[FeedStatus]:
        return list(self._feed_status.values())

