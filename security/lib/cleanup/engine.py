"""Cleanup engine — orchestrate malware cleanup strategies."""
from __future__ import annotations

import logging

from lib.cleanup.cms_cleaner import CMSCleaner
from lib.cleanup.models import CleanupResult
from lib.models import Finding

logger = logging.getLogger(__name__)


class CleanupEngine:
    """Orchestrate cleanup: try CMS-specific first, then generic, then quarantine fallback."""

    def __init__(self, enabled: bool = False, auto: bool = False, use_checksums: bool = True) -> None:
        self._enabled = enabled
        self._auto = auto
        self._cms_cleaner = CMSCleaner(use_checksums=use_checksums)
        self._results: list[CleanupResult] = []
        self._max_results = 500

    async def clean_file(self, path: str, findings: list[Finding] | None = None) -> CleanupResult:
        """Attempt to clean a file. Returns CleanupResult."""
        result = await self._cms_cleaner.clean_file(path)
        self._results.append(result)
        if len(self._results) > self._max_results:
            self._results = self._results[-self._max_results:]
        return result

    @property
    def enabled(self) -> bool:
        return self._enabled

    @property
    def auto(self) -> bool:
        return self._auto

