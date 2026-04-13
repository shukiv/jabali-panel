"""Reusable async log file tailer with rotation and truncation detection."""
from __future__ import annotations

import asyncio
import logging
from pathlib import Path
from typing import Awaitable, Callable

logger = logging.getLogger(__name__)


class AsyncLogTailer:
    """Base class for tailing log files asynchronously.

    Handles:
    - Seeking to end of file on start (skips old entries)
    - Log rotation detection via inode change
    - Log truncation detection via file size
    - Graceful stop via ``stop()``
    """

    def __init__(self, log_path: str, *, poll_interval: float = 0.5) -> None:
        self._log_path = log_path
        self._poll_interval = poll_interval
        self._running = False

    async def tail(self, on_line: Callable[[str], Awaitable[None]]) -> None:
        """Tail the log file, calling *on_line* for each new line.

        Blocks until ``stop()`` is called or the task is cancelled.
        """
        self._running = True
        p = Path(self._log_path)

        try:
            current_inode = p.stat().st_ino
            fh = await asyncio.to_thread(open, p, "r", encoding="utf-8", errors="replace")  # noqa: SIM115
            fh.seek(0, 2)  # Seek to end
        except OSError:
            logger.error("Cannot open log file: %s", self._log_path)
            return

        logger.info("Tailing %s", self._log_path)

        try:
            while self._running:
                line = await asyncio.to_thread(fh.readline)
                if line:
                    await on_line(line)
                else:
                    # No new data -- check for log rotation / truncation
                    try:
                        new_stat = p.stat()
                        if new_stat.st_ino != current_inode:
                            # File was rotated -- reopen
                            logger.info("Log rotation detected for %s", self._log_path)
                            fh.close()
                            fh = await asyncio.to_thread(open, p, "r", encoding="utf-8", errors="replace")  # noqa: SIM115
                            current_inode = new_stat.st_ino
                        elif new_stat.st_size < fh.tell():
                            # File was truncated -- seek to beginning
                            logger.info("Log truncation detected for %s", self._log_path)
                            fh.seek(0)
                    except OSError:
                        pass
                    await asyncio.sleep(self._poll_interval)
        finally:
            fh.close()

    async def stop(self) -> None:
        self._running = False
