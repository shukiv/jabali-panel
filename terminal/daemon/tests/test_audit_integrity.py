"""TDD: Audit session integrity and state safety."""

import os
import secrets
import tempfile

import pytest

from daemon.audit import AuditSession
from daemon.config import TerminalConfig


@pytest.fixture
def temp_dir():
    """Create a temporary directory for test artifacts."""
    with tempfile.TemporaryDirectory() as tmpdir:
        yield tmpdir


@pytest.fixture
def test_config(temp_dir):
    """Create a test config."""
    return TerminalConfig(
        hmac_secret=secrets.token_hex(32),
        audit_hmac_secret=secrets.token_hex(32),
        socket_path=f"{temp_dir}/test.sock",
        nonce_db_path=f"{temp_dir}/nonces.db",
        audit_log_dir=f"{temp_dir}/logs",
        shell="/bin/bash",
        session_idle_seconds=30,
        session_hard_seconds=3600,
    )


def test_audit_session_write_after_close_raises_error(test_config):
    """
    RED: Writing to audit session after close() should raise RuntimeError.

    This is the basic check that the _closed flag prevents post-close writes.
    """
    audit = AuditSession(
        test_config.audit_log_dir,
        123,
        "admin_test",
        "127.0.0.1",
        "deadbeef",
        test_config.audit_hmac_secret,
    )

    # Close the session
    audit.close()

    # Try to write after close — should raise RuntimeError
    with pytest.raises(RuntimeError, match="AuditSession is closed"):
        audit.write_stdout(b"This should fail")

    with pytest.raises(RuntimeError, match="AuditSession is closed"):
        audit.write_stdin(b"This should fail")

    with pytest.raises(RuntimeError, match="AuditSession is closed"):
        audit.write_warning("This should fail")


def test_audit_session_idempotent_close(test_config):
    """
    GREEN: Calling close() multiple times should be safe (idempotent).

    The _closed flag gates the close logic, so calling close() again should
    be a no-op.
    """
    audit = AuditSession(
        test_config.audit_log_dir,
        123,
        "admin_test",
        "127.0.0.1",
        "deadbeef",
        test_config.audit_hmac_secret,
    )

    # Close the session
    audit.close()

    # Try to close again — should not raise
    audit.close()

    # Verify the log file exists and is properly sealed
    assert os.path.exists(audit.log_path)
    assert os.path.exists(audit.sig_path)

    # Read the log to ensure it's properly formatted
    with open(audit.log_path) as f:
        log_content = f.read()

    assert "Session start:" in log_content
    assert "Session end:" in log_content


def test_audit_session_concurrent_close_and_write(test_config):
    """
    RED: Concurrent writes and close should not corrupt the log.

    If close() and write() happen concurrently, the write should either
    succeed before the file is closed, or fail cleanly with RuntimeError,
    but NOT write garbage to the file.
    """
    audit = AuditSession(
        test_config.audit_log_dir,
        123,
        "admin_test",
        "127.0.0.1",
        "deadbeef",
        test_config.audit_hmac_secret,
    )

    # Simulate concurrent operations
    # This is synchronous code, so we'll test that the state machine is correct
    # by checking that writes fail after close, and the log is not corrupted

    # Write some data
    audit.write_stdout(b"Test output 1\n")

    # Close the session
    audit.close()

    # Verify the log is properly sealed
    assert os.path.exists(audit.sig_path)

    # Try to write after close
    with pytest.raises(RuntimeError):
        audit.write_stdout(b"Test output 2\n")

    # Read the log and verify it's not corrupted
    with open(audit.log_path) as f:
        log_content = f.read()

    assert "[STDOUT] Test output 1" in log_content
    assert "[STDOUT] Test output 2" not in log_content, "Post-close write should not appear"
    assert "Session end:" in log_content
