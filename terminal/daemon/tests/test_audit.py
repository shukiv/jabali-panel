"""Tests for audit logging."""

import os
import secrets
import tempfile

import pytest

from daemon.audit import AuditSession, scan_unclosed_logs


@pytest.fixture
def audit_config():
    """Create audit config for testing."""
    return {
        "log_dir": tempfile.mkdtemp(),
        "admin_id": 1,
        "admin_name": "testadmin",
        "ip": "127.0.0.1",
        "session_id": secrets.token_hex(8),
        "audit_hmac_secret": "0" * 64,
    }


def test_audit_session_create(audit_config):
    """Test creating an audit session."""
    session = AuditSession(
        audit_config["log_dir"],
        audit_config["admin_id"],
        audit_config["admin_name"],
        audit_config["ip"],
        audit_config["session_id"],
        audit_config["audit_hmac_secret"],
    )

    assert os.path.exists(session.log_path)

    # Clean up
    session.close()
    assert os.path.exists(session.sig_path)

    # Clean up
    os.unlink(session.log_path)
    os.unlink(session.sig_path)
    os.rmdir(audit_config["log_dir"])


def test_audit_session_write(audit_config):
    """Test writing to audit session."""
    session = AuditSession(
        audit_config["log_dir"],
        audit_config["admin_id"],
        audit_config["admin_name"],
        audit_config["ip"],
        audit_config["session_id"],
        audit_config["audit_hmac_secret"],
    )

    session.write_stdin(b"echo hello\n")
    session.write_stdout(b"hello\n")
    session.write_warning("test warning")

    session.close(exit_code=0)

    # Read log
    with open(session.log_path) as f:
        content = f.read()

    assert "Session start" in content
    assert "STDIN" in content
    assert "STDOUT" in content
    assert "WARNING: test warning" in content
    assert "Session end" in content

    # Clean up
    os.unlink(session.log_path)
    os.unlink(session.sig_path)
    os.rmdir(audit_config["log_dir"])


@pytest.mark.asyncio
async def test_scan_unclosed_logs(audit_config):
    """Test scanning for unclosed logs on startup."""
    log_dir = audit_config["log_dir"]

    # Create a log without a signature
    log_path = os.path.join(log_dir, "2024-01-01_testadmin_abc123.log")
    with open(log_path, "w") as f:
        f.write("# Session start: 2024-01-01T12:00:00Z, admin=testadmin, ip=127.0.0.1, session=abc123\n")
        f.write("[STDOUT] test output\n")

    # Scan for unclosed logs
    await scan_unclosed_logs(log_dir, audit_config["audit_hmac_secret"])

    # Check that signature was created
    sig_path = f"{log_path}.sig"
    assert os.path.exists(sig_path)

    # Clean up
    os.unlink(log_path)
    os.unlink(sig_path)
    os.rmdir(log_dir)
