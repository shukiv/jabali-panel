"""Shannon entropy scanner for detecting obfuscated/encrypted content."""

from __future__ import annotations

import math
from collections import Counter

from lib.models import Finding


class EntropyScanner:
    name = "entropy"

    def __init__(self, threshold: float = 6.0, enabled: bool = True) -> None:
        self._threshold = threshold
        self._enabled = enabled

    async def scan(self, path: str, content: bytes) -> list[Finding]:
        if len(content) < 64:
            return []

        # Skip binary files (null bytes in header indicate non-text content)
        if b"\x00" in content[:512]:
            return []

        overall = self._shannon_entropy(content)

        findings: list[Finding] = []
        if overall > self._threshold:
            findings.append(Finding(
                scanner="entropy",
                rule="high_entropy_file",
                score=self._score_for_entropy(overall),
                description="High Shannon entropy: %.2f (threshold: %.2f)" % (overall, self._threshold),
                metadata={"entropy": round(overall, 4), "size": len(content)},
            ))

        high_blocks = self._find_high_entropy_blocks(content)
        if high_blocks and not findings:
            if len(high_blocks) >= 3:
                avg = sum(e for _, e in high_blocks) / len(high_blocks)
                findings.append(Finding(
                    scanner="entropy",
                    rule="high_entropy_blocks",
                    score=15,
                    description="%d blocks with high entropy (avg: %.2f)" % (len(high_blocks), avg),
                    metadata={"block_count": len(high_blocks), "avg_entropy": round(avg, 4)},
                ))

        return findings

    @staticmethod
    def _shannon_entropy(data: bytes) -> float:
        """Calculate Shannon entropy of byte data. Range: 0.0 (uniform) to 8.0 (random)."""
        if not data:
            return 0.0
        counts = Counter(data)
        length = len(data)
        entropy = 0.0
        for count in counts.values():
            if count == 0:
                continue
            p = count / length
            entropy -= p * math.log2(p)
        return entropy

    def _find_high_entropy_blocks(self, data: bytes, block_size: int = 256) -> list[tuple[int, float]]:
        """Find blocks with entropy above threshold. Returns list of (offset, entropy)."""
        high: list[tuple[int, float]] = []
        for i in range(0, len(data) - block_size + 1, block_size):
            block = data[i:i + block_size]
            e = self._shannon_entropy(block)
            if e > self._threshold + 0.5:
                high.append((i, e))
        return high

    def _score_for_entropy(self, entropy: float) -> int:
        """Map entropy value to a threat score."""
        if entropy > 7.0:
            return 30
        if entropy > 6.0:
            return 25
        if entropy > 5.0:
            return 20
        return 15

    @property
    def enabled(self) -> bool:
        return self._enabled
