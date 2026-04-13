"""Proactive process killer -- kill suspicious processes above threshold."""
from __future__ import annotations

import asyncio
import logging
import os

from lib.models import ProcessThreat
from lib.proactive.models import KillRecord

logger = logging.getLogger(__name__)

# Process names that should NEVER be killed regardless of score
_SAFE_PROCESSES = frozenset({
    "init", "systemd", "sshd", "cron", "crond", "rsyslogd", "journald",
    "nginx", "apache2", "httpd", "mysqld", "mariadbd", "postgres",
    "php-fpm", "stalwart-mail", "pdns_server", "fail2ban-server",
    "jabali-security",
})


class ProactiveProcessKiller:
    """Kill suspicious processes that score above a threshold."""

    def __init__(
        self,
        enabled: bool = False,
        threshold: int = 70,
        min_uid: int = 1000,
        whitelist_commands: list[str] | None = None,
    ) -> None:
        self._enabled = enabled
        self._threshold = threshold
        self._min_uid = min_uid
        self._whitelist_commands = set(whitelist_commands or [])
        self._kill_records: list[KillRecord] = []
        self._max_records = 1000

    async def handle_threats(self, threats: list[ProcessThreat]) -> None:
        """Process monitor callback. Evaluate and kill threats above threshold."""
        for threat in threats:
            # Always log the threat
            logger.warning(
                "PROCESS THREAT: pid=%d ppid=%d score=%d user=%s cmd=%s -- %s",
                threat.pid, threat.ppid, threat.score,
                threat.username, threat.cmdline[:100], threat.description,
            )

            if not self._enabled:
                continue

            if not self._should_kill(threat):
                continue

            success = await self._kill_process(threat.pid)
            record = KillRecord(
                pid=threat.pid,
                ppid=threat.ppid,
                cmdline=threat.cmdline[:500],
                username=threat.username,
                reason=threat.description,
                score=threat.score,
                success=success,
            )
            self._kill_records.append(record)
            if len(self._kill_records) > self._max_records:
                self._kill_records = self._kill_records[-self._max_records:]

            if success:
                logger.critical(
                    "KILLED: pid=%d user=%s score=%d -- %s",
                    threat.pid, threat.username, threat.score, threat.description,
                )
            else:
                logger.error(
                    "KILL FAILED: pid=%d user=%s score=%d -- %s",
                    threat.pid, threat.username, threat.score, threat.description,
                )

    def _should_kill(self, threat: ProcessThreat) -> bool:
        """Decide if a process should be killed."""
        # Must meet score threshold
        if threat.score < self._threshold:
            return False

        # Never kill system processes (UID < min_uid)
        try:
            proc_uid = self._get_uid(threat.pid)
            if proc_uid is not None and proc_uid < self._min_uid:
                logger.debug("Skipping kill for system process pid=%d uid=%d", threat.pid, proc_uid)
                return False
        except (OSError, ValueError):
            pass

        # Never kill safe process names
        comm = self._get_comm(threat.pid)
        if comm and comm in _SAFE_PROCESSES:
            logger.debug("Skipping kill for safe process: %s pid=%d", comm, threat.pid)
            return False

        # Check whitelist (command substrings)
        for wl in self._whitelist_commands:
            if wl in threat.cmdline:
                logger.debug("Skipping kill for whitelisted command: %s pid=%d", wl, threat.pid)
                return False

        return True

    @staticmethod
    async def _kill_process(pid: int) -> bool:
        """Send SIGTERM first, wait 5s, then SIGKILL if still alive."""
        import signal

        try:
            os.kill(pid, signal.SIGTERM)
        except ProcessLookupError:
            return True  # Already dead
        except PermissionError:
            logger.error("Permission denied killing pid=%d", pid)
            return False
        except OSError as exc:
            logger.error("Failed to signal pid=%d: %s", pid, exc)
            return False

        # Wait up to 5 seconds for graceful exit
        for _ in range(10):
            await asyncio.sleep(0.5)
            try:
                os.kill(pid, 0)  # Check if still alive
            except ProcessLookupError:
                return True  # Died gracefully

        # Still alive — force kill
        try:
            os.kill(pid, signal.SIGKILL)
            logger.info("Process pid=%d did not exit after SIGTERM, sent SIGKILL", pid)
            return True
        except ProcessLookupError:
            return True
        except OSError:
            return False

    @staticmethod
    def _get_uid(pid: int) -> int | None:
        """Get real UID of a process from /proc."""
        try:
            for line in open("/proc/%d/status" % pid):  # noqa: SIM115
                if line.startswith("Uid:"):
                    return int(line.split()[1])
        except (OSError, ValueError, IndexError):
            pass
        return None

    @staticmethod
    def _get_comm(pid: int) -> str | None:
        """Get process name from /proc."""
        try:
            from pathlib import Path
            return Path("/proc/%d/comm" % pid).read_text().strip()
        except OSError:
            return None

    @property
    def recent_kills(self) -> list[KillRecord]:
        return list(self._kill_records)

    @property
    def kill_count(self) -> int:
        return len(self._kill_records)

    @property
    def enabled(self) -> bool:
        return self._enabled
