"""Protocol definition for all scanners in jabali-security."""

from __future__ import annotations

from typing import Protocol, runtime_checkable

from lib.models import Finding


@runtime_checkable
class ScannerBase(Protocol):
    name: str

    async def scan(self, path: str, content: bytes) -> list[Finding]:
        """Scan file content and return findings."""
        ...

    @property
    def enabled(self) -> bool:
        """Whether this scanner is active."""
        ...
