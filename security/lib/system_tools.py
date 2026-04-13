"""Safe subprocess wrappers — all use list args, never shell=True."""

from __future__ import annotations

import asyncio


async def run_freshclam() -> tuple[bool, str]:
    """Run ``freshclam --quiet`` and return (success, output)."""
    from lib.privilege import sudo_prefix
    cmd = [*sudo_prefix(), "freshclam", "--quiet"]
    try:
        proc = await asyncio.create_subprocess_exec(
            *cmd,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.STDOUT,
        )
        stdout, _ = await proc.communicate()
        output = stdout.decode() if stdout else ""
        return (proc.returncode == 0, output)
    except OSError as exc:
        return (False, str(exc))
