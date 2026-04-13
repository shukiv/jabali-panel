"""Thin asyncio.Queue wrapper for scan events."""

from __future__ import annotations

import asyncio

from lib.models import FileEvent


class ScanQueue:
    """Bounded async queue for FileEvent objects."""

    DEFAULT_MAXSIZE = 50_000

    def __init__(self, maxsize: int = DEFAULT_MAXSIZE) -> None:
        self._queue: asyncio.Queue[FileEvent] = asyncio.Queue(maxsize=maxsize)
        self._dropped = 0

    async def put(self, event: FileEvent) -> None:
        """Blocking put — apply backpressure when queue is full."""
        await self._queue.put(event)

    def put_nowait(self, event: FileEvent) -> bool:
        """Non-blocking put. Returns False and drops the event if queue is full."""
        try:
            self._queue.put_nowait(event)
            return True
        except asyncio.QueueFull:
            self._dropped += 1
            return False

    async def get(self) -> FileEvent:
        return await self._queue.get()

    def task_done(self) -> None:
        self._queue.task_done()

    def qsize(self) -> int:
        return self._queue.qsize()

