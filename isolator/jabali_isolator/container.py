"""NSpawnManager — create, start, stop, destroy nspawn containers for shell isolation."""

from __future__ import annotations

import asyncio
import contextlib
import fcntl
import logging
import re
import shutil
from pathlib import Path

from jabali_isolator import config as _cfg
from jabali_isolator.config import validate_cpu, validate_memory
from jabali_isolator.machine import SUFFIX, machine_name, service_name
from jabali_isolator.rootfs import create_rootfs, destroy_rootfs, rootfs_exists
from jabali_isolator.units import remove_unit_files, unit_files_exist, write_nspawn_unit, write_service_dropin

logger = logging.getLogger(__name__)


class IsolatorError(Exception):
    """Raised when a container operation fails."""


def _validate_user(user: str) -> None:
    """Validate username to prevent command injection."""
    if not re.match(_cfg.USERNAME_RE, user):
        raise IsolatorError(f"Invalid username: {user!r}")


@contextlib.contextmanager
def _user_lock(user: str):
    """Acquire an advisory per-user lock to prevent concurrent operations.

    Uses fcntl.flock on /run/jabali-isolator/{user}.lock.  The lock is
    released when the context manager exits (or the process dies).
    """
    lock_dir = Path(_cfg.LOCK_DIR)
    lock_dir.mkdir(parents=True, exist_ok=True)
    lock_path = lock_dir / f"{user}.lock"
    fd = lock_path.open("w")
    try:
        fcntl.flock(fd, fcntl.LOCK_EX)
        yield
    finally:
        fcntl.flock(fd, fcntl.LOCK_UN)
        fd.close()


async def _run(cmd: list[str], timeout: int = 30) -> tuple[int, str, str]:
    """Run a subprocess safely (list args, no shell) and return (returncode, stdout, stderr)."""
    proc = await asyncio.create_subprocess_exec(
        *cmd,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
    )
    try:
        stdout, stderr = await asyncio.wait_for(proc.communicate(), timeout=timeout)
    except asyncio.TimeoutError as e:
        proc.kill()
        await proc.wait()
        raise IsolatorError(f"Command timed out: {' '.join(cmd)}") from e

    return proc.returncode, stdout.decode(errors="replace").strip(), stderr.decode(errors="replace").strip()


def is_available() -> bool:
    """Check if systemd-nspawn is installed."""
    return shutil.which("systemd-nspawn") is not None


async def create(
    user: str,
    memory: str = _cfg.DEFAULT_MEMORY,
    cpu: str = _cfg.DEFAULT_CPU,
) -> dict:
    """Create a container for the given user.

    Builds the rootfs and writes systemd unit files.  The container runs
    sleep infinity as its main process (for shell access via nsenter).
    Does NOT start the container — call start() after.

    Returns a dict with creation details.
    """
    _validate_user(user)

    try:
        validate_memory(memory)
        validate_cpu(cpu)
    except ValueError as e:
        raise IsolatorError(str(e)) from e

    if not is_available():
        raise IsolatorError("systemd-nspawn is not installed")

    with _user_lock(user):
        # Build rootfs (raises KeyError if user doesn't exist)
        try:
            rootfs_path = create_rootfs(user)
        except KeyError as e:
            raise IsolatorError(f"User {user!r} does not exist on this system") from e

        # Write unit files
        write_nspawn_unit(user)
        write_service_dropin(user, memory=memory, cpu=cpu)

        # Reload systemd to pick up new units
        rc, _, err = await _run(["systemctl", "daemon-reload"])
        if rc != 0:
            logger.warning("systemctl daemon-reload failed: %s", err)

    logger.info("Created container for %s", user)
    return {
        "user": user,
        "rootfs": str(rootfs_path),
        "memory": memory,
        "cpu": cpu,
    }


async def destroy(user: str) -> bool:
    """Destroy a container: stop if running, remove rootfs and unit files.

    Returns True if anything was removed, False if nothing existed.
    """
    _validate_user(user)

    with _user_lock(user):
        existed = False

        # Stop unconditionally — stop() tolerates not-running
        await _stop_unlocked(user)

        # Disable auto-start (may exist from older installs)
        rc, _, err = await _run(["systemctl", "disable", service_name(user)])
        if rc != 0:
            logger.warning("systemctl disable failed: %s", err)

        # Remove unit files
        if unit_files_exist(user):
            remove_unit_files(user)
            existed = True

        # Remove rootfs
        if destroy_rootfs(user):
            existed = True

        if existed:
            await _run(["systemctl", "daemon-reload"])
            logger.info("Destroyed container for %s", user)

    return existed


async def _stop_unlocked(user: str) -> bool:
    """Stop a container without acquiring the user lock (for internal use by destroy)."""
    machine = machine_name(user)
    rc, _, err = await _run(["machinectl", "stop", machine])
    if rc != 0:
        if "not running" in err.lower() or "not found" in err.lower():
            return True
        raise IsolatorError(f"Failed to stop {machine}: {err}")
    logger.info("Stopped container %s", machine)
    return True


async def start(user: str) -> bool:
    """Start a container.  Returns True on success."""
    _validate_user(user)

    with _user_lock(user):
        machine = machine_name(user)

        if not rootfs_exists(user):
            raise IsolatorError(f"Container for {user!r} does not exist — run create first")

        rc, _, err = await _run(["machinectl", "start", machine])
        if rc != 0:
            raise IsolatorError(f"Failed to start {machine}: {err}")

        logger.info("Started container %s", machine)
    return True


async def stop(user: str) -> bool:
    """Stop a running container.  Returns True on success."""
    _validate_user(user)

    with _user_lock(user):
        return await _stop_unlocked(user)


async def restart(user: str) -> bool:
    """Restart a container."""
    _validate_user(user)

    with _user_lock(user):
        await _stop_unlocked(user)
        machine = machine_name(user)

        if not rootfs_exists(user):
            raise IsolatorError(f"Container for {user!r} does not exist — run create first")

        rc, _, err = await _run(["machinectl", "start", machine])
        if rc != 0:
            raise IsolatorError(f"Failed to start {machine}: {err}")

        logger.info("Started container %s", machine)
    return True


async def status(user: str) -> dict:
    """Get container status.

    Returns a dict with keys: user, machine, state, exists.
    State is "running", "stopped", or "missing".
    """
    _validate_user(user)
    machine = machine_name(user)
    exists = rootfs_exists(user)

    if not exists:
        return {"user": user, "machine": machine, "state": "missing", "exists": False, "enabled": False}

    (rc, out, err), (rc_en, out_en, _) = await asyncio.gather(
        _run(["machinectl", "show", machine, "--property=State"], timeout=10),
        _run(["systemctl", "is-enabled", service_name(user)], timeout=10),
    )
    enabled = rc_en == 0 and out_en.strip().startswith("enabled")

    if rc != 0:
        return {"user": user, "machine": machine, "state": "stopped", "exists": True, "enabled": enabled}

    # Parse "State=running" or "State=closing" etc.
    state = "stopped"
    for line in out.splitlines():
        if line.startswith("State="):
            state = line.split("=", 1)[1].strip()
            break

    return {"user": user, "machine": machine, "state": state, "exists": True, "enabled": enabled}


async def list_all() -> list[dict]:
    """List all jabali-isolator managed containers.

    Scans /var/lib/machines/*{SUFFIX}/ directories.
    """
    machines = Path(_cfg.MACHINES_DIR)
    if not machines.is_dir():
        return []

    users = [
        entry.name.removesuffix(SUFFIX)
        for entry in sorted(machines.iterdir())
        if entry.is_dir() and entry.name.endswith(SUFFIX)
    ]
    if not users:
        return []

    return list(await asyncio.gather(*(status(u) for u in users)))
