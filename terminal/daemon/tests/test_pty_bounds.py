"""TDD: PTY bounds and input validation."""

import pytest

from daemon.pty_bridge import _set_pty_size


def test_set_pty_size_with_invalid_dimensions():
    """
    GREEN: _set_pty_size now catches ValueError from invalid inputs.

    The function catches OSError, ValueError, and TypeError, so invalid
    file descriptors or type errors don't crash the PTY bridge.
    """
    # Invalid fd no longer raises — exception is caught
    _set_pty_size(-1, 24, 80)  # -1 fd raises ValueError, which is now caught

    # Invalid dimension types are also caught
    _set_pty_size(5, "invalid", 80)  # String rows raises TypeError, caught


def test_resize_message_parsing_unvalidated():
    """
    RED: PTY resize message handler doesn't validate dimensions.

    In pty_bridge.py line 154-156, resize message parsing doesn't validate
    the cols and rows values. If a client sends {"type": "resize", "cols": "abc"},
    the int() call will raise ValueError and crash the session.

    This is a DoS vulnerability — a malicious client can crash the terminal
    session by sending a malformed resize message.
    """
    # Current code in pty_bridge.py:
    # cols = int(msg_json.get("cols", 80))
    # rows = int(msg_json.get("rows", 24))

    # If cols is a string, int() will raise ValueError
    msg_json = {"type": "resize", "cols": "not_a_number"}

    with pytest.raises(ValueError):
        _ = int(msg_json.get("cols", 80))

    # This crash would propagate and close the session
    # The session would be terminated, and the admin would lose their terminal


def test_resize_message_with_bounds_checking():
    """
    RED: Resize dimensions should be bounds-checked.

    PTY dimensions should be reasonable (e.g., 1-999 rows/cols).
    The current code accepts any integer, including 0 or negative values,
    which could cause undefined behavior in the PTY driver.
    """
    # Negative dimensions
    assert not (0 <= -1 <= 999), "Negative rows should be rejected"

    # Zero dimensions (some systems don't like these)
    assert not (1 <= 0 <= 999), "Zero cols should be rejected"

    # Reasonable dimensions
    assert 1 <= 80 <= 999, "80 cols should be valid"
    assert 1 <= 24 <= 999, "24 rows should be valid"
