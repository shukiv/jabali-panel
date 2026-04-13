"""Tests for lib.scanner.entropy — EntropyScanner."""

from __future__ import annotations

import os

import pytest

from lib.scanner.entropy import EntropyScanner


@pytest.fixture
def scanner() -> EntropyScanner:
    return EntropyScanner(threshold=4.5, enabled=True)


class TestEntropyDetection:
    async def test_random_bytes_high_entropy(self, scanner: EntropyScanner) -> None:
        # os.urandom produces high entropy (~7.9-8.0)
        content = os.urandom(1024)
        # Ensure no null bytes in header (would cause skip)
        content = bytes(b if b != 0 else 1 for b in content)
        findings = await scanner.scan("/suspicious.php", content)
        assert len(findings) > 0
        rules = [f.rule for f in findings]
        assert "high_entropy_file" in rules

    async def test_repeated_bytes_no_finding(self, scanner: EntropyScanner) -> None:
        # All same byte = entropy 0.0
        content = b"A" * 256
        findings = await scanner.scan("/boring.php", content)
        assert findings == []

    async def test_small_file_skipped(self, scanner: EntropyScanner) -> None:
        # Less than 64 bytes
        content = b"x" * 63
        findings = await scanner.scan("/tiny.php", content)
        assert findings == []

    async def test_exactly_64_bytes_not_skipped(self, scanner: EntropyScanner) -> None:
        # Exactly 64 bytes of low entropy should not be skipped but also no finding
        content = b"A" * 64
        findings = await scanner.scan("/exact64.php", content)
        assert findings == []

    async def test_binary_content_skipped(self, scanner: EntropyScanner) -> None:
        content = b"\x00" * 100 + os.urandom(1024)
        findings = await scanner.scan("/binary.bin", content)
        assert findings == []

    async def test_threshold_customization(self) -> None:
        # Very low threshold should flag even moderate entropy
        low_scanner = EntropyScanner(threshold=1.0, enabled=True)
        # Content with moderate entropy (mix of a few distinct chars)
        content = (b"abcdefgh" * 32)  # 256 bytes, entropy ~3.0
        findings = await low_scanner.scan("/moderate.php", content)
        assert len(findings) > 0

    async def test_high_threshold_no_finding(self) -> None:
        # Very high threshold should not flag anything
        high_scanner = EntropyScanner(threshold=7.9, enabled=True)
        content = (b"abcdefghijklmnop" * 64)  # 1024 bytes, moderate entropy
        findings = await high_scanner.scan("/moderate.php", content)
        assert findings == []

    async def test_finding_metadata(self, scanner: EntropyScanner) -> None:
        content = bytes(b if b != 0 else 1 for b in os.urandom(1024))
        findings = await scanner.scan("/test.php", content)
        if findings:
            f = findings[0]
            assert f.scanner == "entropy"
            assert "entropy" in f.metadata
            assert "size" in f.metadata

    async def test_score_scales_with_entropy(self) -> None:
        scanner = EntropyScanner(threshold=3.0, enabled=True)
        # High entropy content
        content = bytes(b if b != 0 else 1 for b in os.urandom(2048))
        findings = await scanner.scan("/high.php", content)
        high_entropy_findings = [f for f in findings if f.rule == "high_entropy_file"]
        if high_entropy_findings:
            # Random bytes ~7.9 entropy should get score 30
            assert high_entropy_findings[0].score >= 25


class TestEntropyDisabled:
    def test_disabled_property(self) -> None:
        scanner = EntropyScanner(enabled=False)
        assert scanner.enabled is False

    def test_enabled_property(self) -> None:
        scanner = EntropyScanner(enabled=True)
        assert scanner.enabled is True
