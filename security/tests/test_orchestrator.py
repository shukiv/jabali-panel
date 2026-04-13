"""Tests for lib.scanner.ScanOrchestrator — multi-scanner aggregation."""

from __future__ import annotations

import pytest

from lib.config import JabaliConfig
from lib.scanner import ScanOrchestrator


@pytest.fixture
def config():
    return JabaliConfig(
        clamav_enabled="no",
    )


# Test payloads — these are intentional malicious patterns for scanner testing
_MALICIOUS_PHP = (
    b"<?php "
    + b"ev" + b"al(base64_dec" + b"ode(\""
    + b"A" * 500
    + b"\")); ?>"
)
_CLEAN_PHP = b'<?php echo "hello"; ?>'


class TestScanOrchestrator:
    async def test_aggregates_findings_from_multiple_scanners(self, config):
        orch = ScanOrchestrator(config)
        findings = await orch.scan("/test.php", _MALICIOUS_PHP)
        scanners_that_found = {f.scanner for f in findings}
        assert "heuristic" in scanners_that_found
        # May or may not trigger entropy depending on content

    async def test_clean_content_returns_empty(self, config):
        orch = ScanOrchestrator(config)
        findings = await orch.scan("/clean.php", _CLEAN_PHP)
        assert findings == []

    async def test_clamav_excluded_when_disabled(self):
        config = JabaliConfig(
            clamav_enabled="no",
        )
        orch = ScanOrchestrator(config)
        # Core scanners (heuristic, entropy, yara) are always enabled
        assert "heuristic" in orch.scanner_names
        assert "entropy" in orch.scanner_names
        assert "clamav" not in orch.scanner_names

    async def test_scanner_names_reflects_enabled(self, config):
        orch = ScanOrchestrator(config)
        names = orch.scanner_names
        assert "heuristic" in names
        assert "entropy" in names

    async def test_binary_content_skipped_by_scanners(self, config):
        orch = ScanOrchestrator(config)
        binary = b"\x00\x01\x02" * 100
        findings = await orch.scan("/binary.bin", binary)
        assert findings == []
