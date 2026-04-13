"""Pure-Python asyncio inotify watcher using ctypes."""

from __future__ import annotations

import asyncio
import ctypes
import ctypes.util
import errno
import glob
import logging
import os
import struct
from datetime import datetime, timezone
from pathlib import Path

from lib.filter import PreFilter
from lib.models import FileEvent
from lib.queue import ScanQueue
from lib.tenant import resolve_user

logger = logging.getLogger(__name__)

# ---------------------------------------------------------------------------
# libc bindings
# ---------------------------------------------------------------------------

_libc_path = ctypes.util.find_library("c")
if _libc_path is None:
    raise OSError("libc not found")
_libc = ctypes.CDLL(_libc_path, use_errno=True)

_libc.inotify_init1.argtypes = [ctypes.c_int]
_libc.inotify_init1.restype = ctypes.c_int

_libc.inotify_add_watch.argtypes = [ctypes.c_int, ctypes.c_char_p, ctypes.c_uint32]
_libc.inotify_add_watch.restype = ctypes.c_int

_libc.inotify_rm_watch.argtypes = [ctypes.c_int, ctypes.c_int]
_libc.inotify_rm_watch.restype = ctypes.c_int

# ---------------------------------------------------------------------------
# inotify constants
# ---------------------------------------------------------------------------

IN_CREATE = 0x00000100
IN_MODIFY = 0x00000002
IN_MOVED_TO = 0x00000080
IN_DELETE_SELF = 0x00000400
IN_IGNORED = 0x00008000
IN_ISDIR = 0x40000000
IN_NONBLOCK = 0x00000800

WATCH_MASK = IN_CREATE | IN_MODIFY | IN_MOVED_TO | IN_DELETE_SELF

# inotify_event header: int wd, uint32 mask, uint32 cookie, uint32 len
_EVENT_HEADER_FMT = "iIII"
_EVENT_HEADER_SIZE = struct.calcsize(_EVENT_HEADER_FMT)  # 16 bytes


_RESCAN_INTERVAL = 60  # seconds between glob re-scans


class InotifyWatcher:
    """Watch directories for file changes via Linux inotify."""

    def __init__(self, watch_dirs: list[str], pre_filter: PreFilter) -> None:
        self._watch_dirs = watch_dirs
        self._pre_filter = pre_filter
        self._fd: int = -1
        self._wd_to_path: dict[int, str] = {}
        self._watched_roots: set[str] = set()  # root dirs already watched
        self._queue: ScanQueue | None = None
        self._loop: asyncio.AbstractEventLoop | None = None
        self._stop_event: asyncio.Event | None = None
        self._rescan_task: asyncio.Task | None = None

    # ------------------------------------------------------------------
    # Public API
    # ------------------------------------------------------------------

    async def start(self, queue: ScanQueue) -> None:
        """Start the inotify watcher and push events onto *queue*."""
        self._queue = queue
        self._loop = asyncio.get_running_loop()
        self._stop_event = asyncio.Event()

        fd = _libc.inotify_init1(IN_NONBLOCK)
        if fd < 0:
            err = ctypes.get_errno()
            raise OSError(err, os.strerror(err))
        self._fd = fd

        # Expand globs and add recursive watches.
        self._resolve_and_watch()

        logger.info("inotify watcher started: %d watches on fd %d", self.watch_count, self._fd)

        # Register the fd with the event loop for readable events.
        self._loop.add_reader(self._fd, self._on_readable)

        # Start periodic re-scan for new directories matching globs.
        self._rescan_task = asyncio.create_task(self._rescan_loop())

        # Block until stop() is called.
        await self._stop_event.wait()

    async def stop(self) -> None:
        """Remove the fd reader, clean up all watches, and close the fd."""
        if self._rescan_task is not None:
            self._rescan_task.cancel()
            self._rescan_task = None

        if self._loop is not None and self._fd >= 0:
            try:
                self._loop.remove_reader(self._fd)
            except Exception:
                logger.debug("remove_reader failed during stop", exc_info=True)

        # Remove individual watches (best-effort).
        for wd in list(self._wd_to_path):
            _libc.inotify_rm_watch(self._fd, wd)
        self._wd_to_path.clear()

        if self._fd >= 0:
            os.close(self._fd)
            self._fd = -1

        if self._stop_event is not None:
            self._stop_event.set()

        logger.info("inotify watcher stopped")

    @property
    def watch_count(self) -> int:
        """Number of active directory watches."""
        return len(self._wd_to_path)

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    def _resolve_and_watch(self) -> int:
        """Expand watch globs and add watches for any new root directories.

        Returns the number of new root directories added.
        """
        added = 0
        for pattern in self._watch_dirs:
            for dir_path in glob.glob(pattern):
                if dir_path in self._watched_roots:
                    continue
                if os.path.islink(dir_path):
                    logger.warning("skipping symlink in watch dirs: %s", dir_path)
                    continue
                if os.path.isdir(dir_path):
                    self._add_watch_recursive(dir_path)
                    self._watched_roots.add(dir_path)
                    added += 1
        return added

    async def _rescan_loop(self) -> None:
        """Periodically re-expand watch globs to pick up new directories."""
        while True:
            await asyncio.sleep(_RESCAN_INTERVAL)
            try:
                added = self._resolve_and_watch()
                if added:
                    logger.info("Rescan: added %d new root directories (%d total watches)",
                                added, self.watch_count)
            except Exception:
                logger.exception("Error during watch directory rescan")

    def _add_watch_recursive(self, dir_path: str) -> None:
        """Add an inotify watch for *dir_path* and all its subdirectories.

        Hidden directories (names starting with ``"."``) are skipped.
        """
        self._add_single_watch(dir_path)
        try:
            entries = os.scandir(dir_path)
        except PermissionError:
            logger.warning("permission denied scanning: %s", dir_path)
            return

        with entries:
            for entry in entries:
                if entry.is_dir(follow_symlinks=False) and not entry.name.startswith("."):
                    self._add_watch_recursive(entry.path)

    def _add_single_watch(self, dir_path: str) -> None:
        """Add a single inotify watch for *dir_path*."""
        wd = _libc.inotify_add_watch(
            self._fd,
            dir_path.encode("utf-8", errors="surrogateescape"),
            WATCH_MASK,
        )
        if wd < 0:
            err = ctypes.get_errno()
            if err == errno.ENOSPC:
                logger.error(
                    "inotify watch limit reached (ENOSPC) — cannot watch: %s. "
                    "Raise fs.inotify.max_user_watches via sysctl.",
                    dir_path,
                )
                return
            if err == errno.EACCES:
                logger.warning("permission denied adding watch: %s", dir_path)
                return
            if err == errno.ENOENT:
                logger.debug("directory vanished before watch: %s", dir_path)
                return
            raise OSError(err, os.strerror(err))
        self._wd_to_path[wd] = dir_path

    def _read_events(self) -> list[FileEvent]:
        """Read all available inotify events from the fd and return FileEvents."""
        try:
            buf = os.read(self._fd, 65536)
        except OSError as exc:
            if exc.errno in (errno.EAGAIN, errno.EWOULDBLOCK):
                return []
            raise

        events: list[FileEvent] = []
        offset = 0

        while offset + _EVENT_HEADER_SIZE <= len(buf):
            wd, mask, _cookie, name_len = struct.unpack_from(_EVENT_HEADER_FMT, buf, offset)
            offset += _EVENT_HEADER_SIZE

            name_bytes = buf[offset : offset + name_len]
            offset += name_len

            # Strip trailing null padding from the name.
            name = name_bytes.rstrip(b"\x00").decode("utf-8", errors="surrogateescape")

            # Handle directory deletion — clean up stale watches
            if mask & (IN_DELETE_SELF | IN_IGNORED):
                if wd in self._wd_to_path:
                    del self._wd_to_path[wd]
                continue

            if not name:
                continue

            watch_dir = self._wd_to_path.get(wd)
            if watch_dir is None:
                continue

            full_path = os.path.join(watch_dir, name)

            # Auto-add watches for newly created subdirectories.
            if mask & IN_CREATE and mask & IN_ISDIR:
                if not name.startswith("."):
                    self._add_watch_recursive(full_path)
                continue  # Don't queue directory creation events.

            # Ignore directory events that aren't new subdirs.
            if mask & IN_ISDIR:
                continue

            if not self._pre_filter.should_scan(full_path):
                continue

            if mask & IN_CREATE:
                event_type = "create"
            elif mask & IN_MODIFY:
                event_type = "modify"
            else:
                event_type = "move"

            try:
                size = os.path.getsize(full_path)
            except (FileNotFoundError, OSError):
                size = 0

            events.append(
                FileEvent(
                    event_type=event_type,
                    path=full_path,
                    username=resolve_user(full_path),
                    timestamp=datetime.now(timezone.utc),
                    size=size,
                    in_uploads_dir=any(p in ("upload", "uploads") for p in Path(full_path).parts),
                )
            )

        return events

    def _on_readable(self) -> None:
        """Callback invoked by the event loop when the inotify fd is readable."""
        events = self._read_events()
        if not events or self._queue is None or self._loop is None:
            return

        for event in events:
            self._loop.create_task(self._queue.put(event))
