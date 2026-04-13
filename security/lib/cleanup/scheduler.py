"""Scan scheduler — trigger periodic full directory scans."""
from __future__ import annotations

import asyncio
import logging
import os
from datetime import datetime, timezone
from pathlib import Path

from lib.config import JabaliConfig
from lib.filter import PreFilter
from lib.models import FileEvent
from lib.scanner import ScanOrchestrator
from lib.scoring import ScoringEngine
from lib.tenant import resolve_user

logger = logging.getLogger(__name__)


class ScanScheduler:
    """Run periodic full directory scans."""

    def __init__(
        self,
        config: JabaliConfig,
        scanner: ScanOrchestrator,
        scoring: ScoringEngine,
        enabled: bool = False,
        interval_hours: int = 24,
        paths: list[str] | None = None,
    ) -> None:
        self._config = config
        self._scanner = scanner
        self._scoring = scoring
        self._enabled = enabled
        self._interval = interval_hours * 3600
        self._paths = paths or []
        self._pre_filter = PreFilter(config)
        self._last_run: datetime | None = None
        self._files_scanned = 0
        self._threats_found = 0

    async def run(self, on_threat=None) -> None:
        """Run scan loop. Calls on_threat(event, score) for each detection."""
        if not self._enabled or not self._paths:
            logger.info("Scheduled scans disabled")
            return

        logger.info("Scan scheduler started: interval=%dh, paths=%s",
                     self._interval // 3600, ", ".join(self._paths))

        while True:
            await asyncio.sleep(self._interval)
            await self._run_scan(on_threat)

    async def run_now(self, on_threat=None) -> dict:
        """Trigger an immediate full scan. Returns summary."""
        return await self._run_scan(on_threat)

    async def _run_scan(self, on_threat=None) -> dict:
        """Execute a full scan of configured paths."""
        self._last_run = datetime.now(timezone.utc)
        self._files_scanned = 0
        self._threats_found = 0

        logger.info("Starting scheduled full scan...")

        import glob as glob_mod
        for pattern in self._paths:
            for dir_path in glob_mod.glob(pattern):
                if os.path.isdir(dir_path):
                    await self._scan_directory(dir_path, on_threat)

        logger.info("Scheduled scan complete: %d files scanned, %d threats found",
                     self._files_scanned, self._threats_found)

        return {
            "files_scanned": self._files_scanned,
            "threats_found": self._threats_found,
            "completed_at": self._last_run.isoformat() if self._last_run else None,
        }

    async def _scan_directory(self, dir_path: str, on_threat=None) -> None:
        """Recursively scan a directory."""
        for root, _dirs, files in os.walk(dir_path):
            for fname in files:
                full_path = os.path.join(root, fname)
                if not self._pre_filter.should_scan(full_path):
                    continue

                try:
                    content = Path(full_path).read_bytes()
                except (OSError, PermissionError):
                    continue

                if len(content) > self._config.max_file_size:
                    continue

                self._files_scanned += 1
                findings = await self._scanner.scan(full_path, content)

                if findings:
                    event = FileEvent(
                        event_type="scheduled_scan",
                        path=full_path,
                        username=resolve_user(full_path),
                        size=len(content),
                    )
                    score = self._scoring.evaluate(event, findings)
                    if score.action != "ignore":
                        self._threats_found += 1
                        if on_threat:
                            await on_threat(event, score)

                # Yield control periodically
                if self._files_scanned % 100 == 0:
                    await asyncio.sleep(0.01)

    @property
    def status(self) -> dict:
        return {
            "enabled": self._enabled,
            "interval_hours": self._interval // 3600,
            "paths": self._paths,
            "last_run": self._last_run.isoformat() if self._last_run else None,
            "files_scanned": self._files_scanned,
            "threats_found": self._threats_found,
        }
