"""Pre-filter to decide whether a file should be scanned."""

from __future__ import annotations

import os
from pathlib import PurePosixPath

from lib.config import JabaliConfig

# MIME types that are clearly binary and should be skipped for text-extension files
_BINARY_MIMES = frozenset({
    "application/octet-stream",
    "application/x-executable",
    "application/x-sharedlib",
    "application/x-object",
    "application/x-mach-binary",
    "application/x-pie-executable",
    "image/png",
    "image/jpeg",
    "image/gif",
    "image/webp",
    "audio/mpeg",
    "audio/ogg",
    "video/mp4",
    "video/webm",
    "application/zip",
    "application/gzip",
    "application/x-bzip2",
    "application/x-xz",
    "application/x-tar",
    "application/x-rar",
    "application/x-7z-compressed",
})


class PreFilter:
    """Decides whether a file path should be queued for scanning."""

    def __init__(self, config: JabaliConfig) -> None:
        self.scan_extensions: set[str] = set(config.scan_extensions)
        self.max_file_size: int = config.max_file_size
        self.skip_dirs: set[str] = set(config.skip_dirs)

    def should_scan(self, path: str) -> bool:
        """Check extension, skip dirs, and file size."""
        p = PurePosixPath(path)

        # Check if any path component is in skip_dirs
        for part in p.parts:
            if part in self.skip_dirs:
                return False

        # Check extension
        if p.suffix.lower() not in self.scan_extensions:
            return False

        # Check file size (0 = skip missing/unreadable files)
        try:
            size = os.path.getsize(path)
        except OSError:
            return False
        if size == 0 or size > self.max_file_size:
            return False

        return True
