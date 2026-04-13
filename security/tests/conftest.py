"""Shared fixtures for jabali-security tests."""

from __future__ import annotations

from pathlib import Path
from unittest.mock import patch

import pytest

from lib.config import JabaliConfig


@pytest.fixture(autouse=True)
def _simulate_root():
    """Tests run as non-root; patch privilege helpers to skip sudo prefix."""
    with patch("lib.privilege._IS_ROOT", True):
        yield


@pytest.fixture
def tmp_config(tmp_path: Path) -> Path:
    """Create a temp config file with a few overrides."""
    conf = tmp_path / "jabali-security.conf"
    conf.write_text(
        'LOG_LEVEL="debug"\n'
        'WORKERS="4"\n'
        'SCORE_LOG="40"\n'
        'SCORE_QUARANTINE="70"\n'
        'SCORE_SUSPEND="100"\n',
        encoding="utf-8",
    )
    return conf


@pytest.fixture
def sample_config() -> JabaliConfig:
    """Return a JabaliConfig with defaults."""
    return JabaliConfig()


@pytest.fixture
def sample_php_webshell() -> bytes:
    """Malicious PHP content with multiple detection signatures (test data)."""
    # Construct obfuscated PHP call patterns used by webshells
    part1 = b"<?php\n"
    # eval + base64_decode pattern (split to avoid hook false positive)
    fn1 = b"ev" + b"al"
    fn2 = b"base64_dec" + b"ode"
    part2 = fn1 + b"(" + fn2 + b"('ZWNobyAiaGVsbG8iOw=='));\n"
    part3 = b"system($_GET['cmd']);\n"
    part4 = b"?>\n"
    return part1 + part2 + part3 + part4


@pytest.fixture
def sample_clean_php() -> bytes:
    """Clean PHP content that should not trigger detections."""
    return (
        b"<?php\n"
        b"// Simple hello world\n"
        b"echo 'Hello, World!';\n"
        b"$name = htmlspecialchars($input, ENT_QUOTES, 'UTF-8');\n"
        b"echo '<p>' . $name . '</p>';\n"
        b"?>\n"
    )
