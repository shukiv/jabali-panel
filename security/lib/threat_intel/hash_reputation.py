"""Hash reputation database -- known-bad file hash lookups."""
from __future__ import annotations

import asyncio
import json
import logging
from pathlib import Path
from urllib.error import URLError
from urllib.request import Request, urlopen

logger = logging.getLogger(__name__)

_MALWAREBAZAAR_URL = "https://mb-api.abuse.ch/api/v1/"


class HashReputationDB:
    """Known-bad file hash database with local cache and optional remote lookups."""

    def __init__(self) -> None:
        self._known_bad: set[str] = set()  # SHA-256 hashes

    def load_feed(self, path: Path) -> int:
        """Load a hash list file (one SHA-256 per line). Returns count loaded."""
        if not path.is_file():
            return 0

        count = 0
        for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            # Extract first field (hash lists may have CSV format)
            h = line.split(",")[0].strip().strip('"').lower()
            if len(h) == 64 and all(c in "0123456789abcdef" for c in h):
                self._known_bad.add(h)
                count += 1

        logger.info("Loaded %d known-bad hashes from %s", count, path)
        return count

    def check_local(self, sha256: str) -> bool:
        """Check if hash is in the local known-bad set. O(1)."""
        return sha256.lower() in self._known_bad

    async def check_remote(self, sha256: str, api_key: str = "") -> dict | None:
        """Check hash against MalwareBazaar API. Returns details or None."""
        return await asyncio.to_thread(self._query_malwarebazaar, sha256)

    @staticmethod
    def _query_malwarebazaar(sha256: str) -> dict | None:
        """Query MalwareBazaar for a hash. Synchronous -- run in executor."""
        try:
            data = ("query=get_info&hash=%s" % sha256).encode("utf-8")
            req = Request(_MALWAREBAZAAR_URL, data=data, method="POST")  # noqa: S310
            req.add_header("Content-Type", "application/x-www-form-urlencoded")
            with urlopen(req, timeout=10) as resp:  # noqa: S310
                result = json.loads(resp.read().decode())
            if result.get("query_status") == "ok" and result.get("data"):
                entry = result["data"][0]
                return {
                    "sha256": entry.get("sha256_hash", ""),
                    "file_type": entry.get("file_type", ""),
                    "signature": entry.get("signature", ""),
                    "tags": entry.get("tags", []),
                    "first_seen": entry.get("first_seen", ""),
                }
        except (URLError, OSError, json.JSONDecodeError, KeyError, IndexError):
            pass
        return None

    @property
    def size(self) -> int:
        return len(self._known_bad)
