"""Behavior tracker — detect suspicious file lifecycle patterns."""

from __future__ import annotations

import asyncio
import logging
import re
import time
from dataclasses import dataclass, field
from pathlib import PurePosixPath

from lib.models import FileEvent, Finding

logger = logging.getLogger(__name__)

# Regex for random/hash-like filenames (hex strings, or alphanumeric with
# at least one digit mixed in — pure alphabetic names like "functions" are not random)
_RANDOM_NAME_RE = re.compile(
    r"^[a-f0-9]{6,}$|^(?=.*\d)[a-z0-9]{8,}$|^tmp[_-][a-z0-9]{4,}$",
    re.IGNORECASE,
)

# Suspicious filename patterns (common malware names)
_SUSPICIOUS_NAMES = frozenset({
    "shell", "cmd", "backdoor", "webshell", "c99", "r57", "wso",
    "b374k", "alfa", "mini", "bypass", "upload", "config2",
})


@dataclass
class _FileLifecycle:
    """Track events for a single file path."""

    path: str
    username: str | None = None
    created_at: float | None = None
    modified_at: float | None = None
    event_count: int = 0
    last_event: float = field(default_factory=time.monotonic)

    def record(self, event: FileEvent) -> None:
        self.event_count += 1
        self.last_event = time.monotonic()
        self.username = event.username
        if event.event_type == "create":
            self.created_at = self.last_event
        elif event.event_type == "modify":
            self.modified_at = self.last_event

    @property
    def age(self) -> float:
        """Seconds since first event."""
        if self.created_at is not None:
            return time.monotonic() - self.created_at
        return 0.0

    @property
    def create_to_modify_seconds(self) -> float | None:
        """Seconds between create and first modify, or None."""
        if self.created_at is not None and self.modified_at is not None:
            return self.modified_at - self.created_at
        return None


@dataclass
class _UserActivity:
    """Track per-user file creation rate."""

    file_count: int = 0
    window_start: float = field(default_factory=time.monotonic)

    def record(self) -> None:
        now = time.monotonic()
        # Reset window every 60 seconds
        if now - self.window_start > 60:
            self.file_count = 0
            self.window_start = now
        self.file_count += 1


class BehaviorTracker:
    """Track file lifecycle events and detect suspicious temporal patterns."""

    def __init__(self, ttl: int = 300) -> None:
        self._ttl = ttl  # seconds to keep tracking entries
        self._files: dict[str, _FileLifecycle] = {}  # path -> lifecycle
        self._users: dict[str, _UserActivity] = {}   # username -> activity
        self._last_cleanup = time.monotonic()
        self._lock = asyncio.Lock()

    async def record_event(self, event: FileEvent) -> list[Finding]:
        """Record a file event and return any behavioral findings."""
        async with self._lock:
            self._maybe_cleanup()

            # Track per-file lifecycle
            lifecycle = self._files.get(event.path)
            if lifecycle is None:
                lifecycle = _FileLifecycle(path=event.path)
                self._files[event.path] = lifecycle
            lifecycle.record(event)

            # Track per-user activity
            if event.username:
                user_activity = self._users.get(event.username)
                if user_activity is None:
                    user_activity = _UserActivity()
                    self._users[event.username] = user_activity
                user_activity.record()

            findings: list[Finding] = []

            # Check 1: Rapid create-then-modify
            ctm = lifecycle.create_to_modify_seconds
            if ctm is not None and ctm < 5.0:
                findings.append(Finding(
                    scanner="behavior",
                    rule="rapid_create_modify",
                    score=25,
                    description="File created and modified within %.1fs" % ctm,
                    metadata={"path": event.path, "seconds": round(ctm, 1)},
                ))

            # Check 2: New file in uploads directory
            if event.event_type == "create" and event.in_uploads_dir:
                findings.append(Finding(
                    scanner="behavior",
                    rule="new_file_in_uploads",
                    score=20,
                    description="New file created in uploads directory",
                    metadata={"path": event.path},
                ))

            # Check 3: Random/hash-like filename
            if event.event_type == "create":
                stem = PurePosixPath(event.path).stem
                if _RANDOM_NAME_RE.match(stem):
                    findings.append(Finding(
                        scanner="behavior",
                        rule="random_filename",
                        score=15,
                        description="Random/hash-like filename: %s" % stem,
                        metadata={"path": event.path, "stem": stem},
                    ))
                # Also check against known suspicious names
                if stem.lower() in _SUSPICIOUS_NAMES:
                    findings.append(Finding(
                        scanner="behavior",
                        rule="suspicious_filename",
                        score=20,
                        description="Suspicious filename: %s" % stem,
                        metadata={"path": event.path, "stem": stem},
                    ))

            # Check 4: Burst file creation by same user (>100 files in 60s)
            # Threshold is high because CMS installs (WordPress, Joomla) routinely
            # create hundreds of files during extraction.
            if event.username and event.event_type == "create":
                ua = self._users.get(event.username)
                if ua and ua.file_count > 100:
                    findings.append(Finding(
                        scanner="behavior",
                        rule="burst_file_creation",
                        score=30,
                        description="User %s created %d files in 60s" % (
                            event.username, ua.file_count,
                        ),
                        metadata={
                            "username": event.username,
                            "count": ua.file_count,
                        },
                    ))

            # Check 5: Many events on same file (>5 in <30s = suspicious churn)
            if lifecycle.event_count > 5 and lifecycle.age < 30:
                findings.append(Finding(
                    scanner="behavior",
                    rule="rapid_file_churn",
                    score=15,
                    description="File modified %d times in %.0fs" % (
                        lifecycle.event_count, lifecycle.age,
                    ),
                    metadata={
                        "path": event.path,
                        "count": lifecycle.event_count,
                    },
                ))

            return findings

    def _maybe_cleanup(self) -> None:
        """Evict stale entries older than TTL."""
        now = time.monotonic()
        if now - self._last_cleanup < 30:  # cleanup at most every 30s
            return
        self._last_cleanup = now
        cutoff = now - self._ttl
        stale = [p for p, lc in self._files.items() if lc.last_event < cutoff]
        for p in stale:
            del self._files[p]
        stale_users = [
            u for u, a in self._users.items()
            if now - a.window_start > self._ttl
        ]
        for u in stale_users:
            del self._users[u]
