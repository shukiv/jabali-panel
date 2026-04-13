"""Tests for configuration loading."""

import os
import tempfile

import pytest

from daemon.config import load_config


def test_load_config_valid():
    """Test loading valid configuration."""
    with tempfile.NamedTemporaryFile(mode="w", suffix=".conf", delete=False) as f:
        f.write('socket_path="/run/test.sock"\n')
        f.write('audit_log_dir="/var/log/test"\n')
        f.write('nonce_db_path="/var/lib/test/nonces.db"\n')
        f.write('session_idle_seconds="300"\n')
        f.write('session_hard_seconds="3600"\n')
        f.write('max_concurrent_sessions="4"\n')
        f.write('allowed_ips=""\n')
        f.write('shell="/bin/bash"\n')
        f.write(f'hmac_secret="{"0" * 64}"\n')
        f.write(f'audit_hmac_secret="{"1" * 64}"\n')
        f.flush()

        try:
            config = load_config(f.name)
            assert config.socket_path == "/run/test.sock"
            assert config.session_idle_seconds == 300
        finally:
            os.unlink(f.name)


def test_load_config_missing_secret():
    """Test loading config with missing HMAC secret."""
    with tempfile.NamedTemporaryFile(mode="w", suffix=".conf", delete=False) as f:
        f.write('shell="/bin/bash"\n')
        f.write('hmac_secret=""\n')  # Empty secret
        f.flush()

        try:
            with pytest.raises(ValueError, match="cannot be empty"):
                load_config(f.name)
        finally:
            os.unlink(f.name)


def test_load_config_short_secret():
    """Test loading config with too-short HMAC secret."""
    with tempfile.NamedTemporaryFile(mode="w", suffix=".conf", delete=False) as f:
        f.write('shell="/bin/bash"\n')
        f.write(f'hmac_secret="{"0" * 32}"\n')  # Only 32 hex chars (16 bytes)
        f.write(f'audit_hmac_secret="{"1" * 64}"\n')
        f.flush()

        try:
            with pytest.raises(ValueError, match="must be ≥64 hex chars"):
                load_config(f.name)
        finally:
            os.unlink(f.name)


def test_config_nonexistent_file():
    """Test loading non-existent config file."""
    with pytest.raises(FileNotFoundError):
        load_config("/nonexistent/path/config.conf")


def test_config_invalid_line():
    """Test loading config with invalid line format."""
    with tempfile.NamedTemporaryFile(mode="w", suffix=".conf", delete=False) as f:
        f.write('invalid_line_format\n')
        f.flush()

        try:
            with pytest.raises(ValueError, match="Invalid config line"):
                load_config(f.name)
        finally:
            os.unlink(f.name)
