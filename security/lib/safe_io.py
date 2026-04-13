"""Safe I/O helpers — O_NOFOLLOW file reads to prevent symlink TOCTOU races."""

from __future__ import annotations

import os


def safe_read_bytes(path: str, max_size: int = 2 * 1024 * 1024) -> bytes:
    """Read a file using O_NOFOLLOW to atomically reject symlinks.

    Raises OSError (ELOOP) if *path* is a symbolic link.
    """
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    try:
        return os.read(fd, max_size)
    finally:
        os.close(fd)
