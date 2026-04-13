"""Scan orchestrator — runs all enabled scanners and aggregates findings."""

from __future__ import annotations

import asyncio
import logging

from lib.config import JabaliConfig
from lib.models import Finding
from lib.scanner.base import ScannerBase
from lib.scanner.clamav import ClamavScanner
from lib.scanner.entropy import EntropyScanner
from lib.scanner.heuristic import HeuristicScanner
from lib.scanner.yara_engine import YaraEngine

logger = logging.getLogger(__name__)


class ScanOrchestrator:
    """Run all enabled scanners on file content and aggregate findings."""

    def __init__(self, config: JabaliConfig) -> None:
        self._scanners: list[ScannerBase] = []

        self._scanners.append(HeuristicScanner())
        self._scanners.append(EntropyScanner(threshold=config.entropy_threshold))
        self._scanners.append(YaraEngine(rules_dir=config.yara_rules_dir))

        # ClamAV: auto-detect or explicit
        clamav = ClamavScanner(socket_path=config.clamav_socket, mode=config.clamav_enabled)
        if clamav.enabled:
            self._scanners.append(clamav)

        enabled_names = [s.name for s in self._scanners if s.enabled]
        logger.info("Scan engines enabled: %s", ", ".join(enabled_names) or "none")

    async def scan(self, path: str, content: bytes) -> list[Finding]:
        """Run all enabled scanners concurrently and return aggregated findings."""
        if not self._scanners:
            return []

        tasks = [scanner.scan(path, content) for scanner in self._scanners if scanner.enabled]
        results = await asyncio.gather(*tasks, return_exceptions=True)

        findings: list[Finding] = []
        for result in results:
            if isinstance(result, Exception):
                logger.error("Scanner error: %s", result)
                continue
            findings.extend(result)

        return findings

    def reload_rules(self) -> None:
        """Reload YARA rules (and any other reloadable scanners)."""
        for scanner in self._scanners:
            if isinstance(scanner, YaraEngine):
                scanner.reload_rules()

    @property
    def scanner_names(self) -> list[str]:
        return [s.name for s in self._scanners if s.enabled]
