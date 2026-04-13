"""Process monitor — poll /proc for suspicious process trees."""

from __future__ import annotations

import asyncio
import logging
import os
import re
from pathlib import Path
from typing import Awaitable, Callable

from lib.models import ProcessThreat

logger = logging.getLogger(__name__)

# Parent process names that indicate web context
_WEB_PARENTS = frozenset({
    "php-fpm", "php-cgi", "php", "apache2", "httpd", "nginx", "lsphp",
    "litespeed", "lshttpd",
})

# Child process names that are suspicious when spawned from web context
_SUSPICIOUS_CHILDREN = frozenset({
    "bash", "sh", "dash", "zsh", "ksh", "csh",
    "python", "python3", "python2",
    "perl", "ruby", "node",
})

# Regex patterns for suspicious cmdline content
# Each tuple: (rule_name, compiled_pattern, score, description)
_CMDLINE_PATTERNS: list[tuple[str, re.Pattern[str], int, str]] = [
    (
        "reverse_shell_bash",
        re.compile(r"bash\s+-i\s+>&\s*/dev/tcp/", re.IGNORECASE),
        80,
        "Bash reverse shell via /dev/tcp",
    ),
    (
        "reverse_shell_nc",
        re.compile(r"nc\s+.*-e\s+/bin/", re.IGNORECASE),
        80,
        "Netcat reverse shell",
    ),
    (
        "reverse_shell_python",
        re.compile(r"python[23]?\s+-c\s+.{0,500}(?:socket|pty\.spawn)", re.IGNORECASE),
        80,
        "Python reverse shell",
    ),
    (
        "reverse_shell_perl",
        re.compile(r"perl\s+-e\s+.{0,500}(?:Socket|socket)", re.IGNORECASE),
        80,
        "Perl reverse shell",
    ),
    (
        "download_exec",
        re.compile(r"(?:wget|curl)\s+.{0,500}\|\s*(?:bash|sh|python|perl)", re.IGNORECASE),
        60,
        "Download and pipe to interpreter",
    ),
    (
        "download_tmp",
        re.compile(r"(?:wget|curl)\s+.*-[oO]\s*/tmp/", re.IGNORECASE),
        40,
        "Download to /tmp",
    ),
    (
        "chmod_exec",
        re.compile(r"chmod\s+\+x\s+/(?:tmp|var/tmp|home)", re.IGNORECASE),
        40,
        "chmod +x in sensitive directory",
    ),
    (
        "exec_tmp_binary",
        re.compile(r"\./[a-z0-9]{1,20}\s*$"),
        30,
        "Executing unnamed binary",
    ),
    (
        "crypto_miner_flags",
        re.compile(r"--(?:coin|algo|pool|stratum|donate-level)", re.IGNORECASE),
        50,
        "Cryptocurrency miner flags",
    ),
    (
        "base64_decode_exec",
        re.compile(r"(?:echo|printf)\s+.{0,500}\|\s*base64\s+-d\s*\|", re.IGNORECASE),
        50,
        "Base64 decode and execute",
    ),
]


class ProcessMonitor:
    """Monitor /proc for suspicious process activity."""

    def __init__(self, poll_interval: int = 2, enabled: bool = True) -> None:
        self._poll_interval = max(1, poll_interval)
        self._enabled = enabled
        self._seen_pids: set[int] = set()

    async def run(self, callback: Callable[[list[ProcessThreat]], Awaitable[None]]) -> None:
        """Poll loop. Calls callback(list[ProcessThreat]) when threats found."""
        if not self._enabled:
            logger.info("Process monitor disabled")
            return

        logger.info("Process monitor started (interval=%ds)", self._poll_interval)
        while True:
            try:
                threats = await asyncio.to_thread(self._poll)
                new_threats = [t for t in threats if t.pid not in self._seen_pids]
                if new_threats:
                    for t in new_threats:
                        self._seen_pids.add(t.pid)
                    await callback(new_threats)
            except Exception:
                logger.exception("Process monitor poll error")

            # Periodically clean up seen PIDs for processes that no longer exist
            if len(self._seen_pids) > 10000:
                self._cleanup_seen()

            await asyncio.sleep(self._poll_interval)

    def _poll(self) -> list[ProcessThreat]:
        """Scan /proc for suspicious processes. Runs in executor thread."""
        threats: list[ProcessThreat] = []

        try:
            pids = [int(d) for d in os.listdir("/proc") if d.isdigit()]
        except OSError:
            return threats

        for pid in pids:
            try:
                info = self._read_proc_info(pid)
                if info is None:
                    continue

                cmdline, comm, ppid, uid = info

                # Get parent info
                parent_info = self._read_proc_info(ppid) if ppid > 1 else None
                parent_cmdline = parent_info[0] if parent_info else ""
                parent_comm = parent_info[1] if parent_info else ""

                username = self._uid_to_username(uid)

                # Check 1: Anomalous parent-child chain
                if parent_comm in _WEB_PARENTS and comm in _SUSPICIOUS_CHILDREN:
                    threats.append(ProcessThreat(
                        pid=pid,
                        ppid=ppid,
                        cmdline=cmdline[:500],
                        parent_cmdline=parent_cmdline[:500],
                        username=username,
                        score=60,
                        description="Web process %s (pid=%d) spawned %s (pid=%d)" % (
                            parent_comm, ppid, comm, pid,
                        ),
                    ))

                # Check 2: Suspicious cmdline patterns
                for rule_name, pattern, score, desc in _CMDLINE_PATTERNS:
                    if pattern.search(cmdline):
                        threats.append(ProcessThreat(
                            pid=pid,
                            ppid=ppid,
                            cmdline=cmdline[:500],
                            parent_cmdline=parent_cmdline[:500],
                            username=username,
                            score=score,
                            description=desc,
                        ))
                        break  # One cmdline pattern match per process

            except (ProcessLookupError, FileNotFoundError, PermissionError):
                continue  # Process vanished or inaccessible

        return threats

    @staticmethod
    def _read_proc_info(pid: int) -> tuple[str, str, int, int] | None:
        """Read cmdline, comm, ppid, uid from /proc/{pid}/. Returns None on failure."""
        proc = Path("/proc") / str(pid)
        try:
            # cmdline: null-separated args
            cmdline_raw = (proc / "cmdline").read_bytes()
            if not cmdline_raw:
                return None
            cmdline = cmdline_raw.replace(b"\x00", b" ").decode("utf-8", errors="replace").strip()

            # comm: process name (up to 16 chars)
            comm = (proc / "comm").read_text().strip()

            # stat: parse ppid — handle comm in parens: "pid (comm) state ppid ..."
            stat_line = (proc / "stat").read_text()
            close_paren = stat_line.rfind(")")
            fields_after = stat_line[close_paren + 2:].split()
            ppid = int(fields_after[1])  # field 0=state, field 1=ppid

            # Get UID from status file
            uid = 0
            for line in (proc / "status").read_text().splitlines():
                if line.startswith("Uid:"):
                    uid = int(line.split()[1])  # Real UID
                    break

            return (cmdline, comm, ppid, uid)
        except (OSError, ValueError, IndexError):
            return None

    @staticmethod
    def _uid_to_username(uid: int) -> str | None:
        """Resolve UID to username. Returns None if not found."""
        try:
            import pwd
            return pwd.getpwuid(uid).pw_name
        except (KeyError, ImportError):
            return None

    def _cleanup_seen(self) -> None:
        """Remove PIDs from seen set that no longer exist."""
        alive = set()
        for pid in self._seen_pids:
            if Path("/proc/%d" % pid).exists():
                alive.add(pid)
        self._seen_pids = alive
