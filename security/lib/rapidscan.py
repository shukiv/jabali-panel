"""RapidScan — parallel file scanning with mtime cache for performance."""
from __future__ import annotations

import asyncio
import json
import logging
import os
from pathlib import Path

from lib.config import JabaliConfig
from lib.filter import PreFilter

logger = logging.getLogger(__name__)

# Cap mtime cache at 500K paths (~150MB worst-case for long paths, typically ~50MB)
_MTIME_CACHE_MAX = 500_000


class RapidScanEngine:
    """Parallel directory scanning with mtime-based skip optimization."""

    def __init__(self, config: JabaliConfig, workers: int = 4) -> None:
        self._config = config
        self._workers = workers
        self._pre_filter = PreFilter(config)
        self._mtime_cache: dict[str, float] = {}
        self._cache_path: Path | None = None

    def set_cache_path(self, path: Path) -> None:
        """Set and load the mtime cache file."""
        self._cache_path = path
        self._load_cache()

    async def scan_directory(self, dir_path: str, scanner, scoring) -> dict:
        """Parallel recursive scan with mtime optimization. Returns summary."""
        # Collect files to scan
        files_to_scan: list[str] = []
        files_skipped = 0

        for root, _dirs, files in os.walk(dir_path):
            for fname in files:
                full = os.path.join(root, fname)
                if not self._pre_filter.should_scan(full):
                    continue
                try:
                    mtime = os.path.getmtime(full)
                except OSError:
                    continue
                # Skip if mtime unchanged since last scan
                cached_mtime = self._mtime_cache.get(full)
                if cached_mtime is not None and mtime <= cached_mtime:
                    files_skipped += 1
                    continue
                files_to_scan.append(full)

        logger.info(
            "RapidScan: %d files to scan, %d skipped (unchanged) in %s",
            len(files_to_scan), files_skipped, dir_path,
        )

        # Parallel scanning with semaphore
        sem = asyncio.Semaphore(self._workers)
        results: list[dict] = []
        threats_found = 0

        async def _scan_one(path: str) -> None:
            nonlocal threats_found
            async with sem:
                try:
                    content = await asyncio.to_thread(Path(path).read_bytes)
                    if len(content) > self._config.max_file_size:
                        return
                    findings = await scanner.scan(path, content)
                    # Update mtime cache
                    try:
                        self._mtime_cache[path] = os.path.getmtime(path)
                    except OSError:
                        pass
                    if findings:
                        from lib.models import FileEvent
                        from lib.tenant import resolve_user
                        event = FileEvent(
                            event_type="rapidscan", path=path,
                            username=resolve_user(path), size=len(content),
                        )
                        score = scoring.evaluate(event, findings)
                        if score.action != "ignore":
                            threats_found += 1
                            results.append({
                                "path": path,
                                "score": score.total,
                                "action": score.action,
                                "findings": [{"scanner": f.scanner, "rule": f.rule, "score": f.score, "description": f.description} for f in findings],
                                "findings_count": len(findings),
                            })
                except (OSError, PermissionError):
                    pass

        # Run all scans
        tasks = [_scan_one(f) for f in files_to_scan]
        await asyncio.gather(*tasks)

        # Save cache and trim if over limit
        self._trim_mtime_cache()
        self._save_cache()

        return {
            "directory": dir_path,
            "files_scanned": len(files_to_scan),
            "files_skipped": files_skipped,
            "threats_found": threats_found,
            "results": results,
        }

    def _trim_mtime_cache(self) -> None:
        """Evict stale/excess entries from the mtime cache."""
        if len(self._mtime_cache) <= _MTIME_CACHE_MAX:
            return
        # Remove paths that no longer exist on disk first
        dead = [p for p in self._mtime_cache if not os.path.exists(p)]
        for p in dead:
            del self._mtime_cache[p]
        # If still over limit, drop arbitrary oldest-inserted entries
        if len(self._mtime_cache) > _MTIME_CACHE_MAX:
            overflow = len(self._mtime_cache) - _MTIME_CACHE_MAX
            for p in list(self._mtime_cache.keys())[:overflow]:
                del self._mtime_cache[p]

    def _load_cache(self) -> None:
        if not self._cache_path or not self._cache_path.is_file():
            return
        try:
            raw: dict[str, float] = json.loads(self._cache_path.read_text(encoding="utf-8"))
            # Enforce cap immediately on load
            if len(raw) > _MTIME_CACHE_MAX:
                keys = list(raw.keys())[-_MTIME_CACHE_MAX:]
                raw = {k: raw[k] for k in keys}
            self._mtime_cache = raw
        except (json.JSONDecodeError, OSError):
            self._mtime_cache = {}

    def _save_cache(self) -> None:
        if not self._cache_path:
            return
        try:
            self._cache_path.parent.mkdir(parents=True, exist_ok=True)
            self._cache_path.write_text(json.dumps(self._mtime_cache), encoding="utf-8")
        except OSError:
            pass

