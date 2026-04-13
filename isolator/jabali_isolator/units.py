"""systemd unit file generators for nspawn containers.

Generates two files per container:
  1. /etc/systemd/nspawn/{user}-php.nspawn     — container filesystem + network config
  2. /etc/systemd/system/systemd-nspawn@{user}-php.service.d/limits.conf — resource limits
"""

from __future__ import annotations

import logging
import os
import shutil
from pathlib import Path

from jabali_isolator.config import DEFAULT_CPU, DEFAULT_MEMORY, HOST_RO_BINDS
from jabali_isolator.machine import dropin_dir, nspawn_path

logger = logging.getLogger(__name__)


def _validate_nspawn_inputs(user: str) -> None:
    """Validate inputs before interpolating into systemd unit files."""
    import re

    from jabali_isolator.config import USERNAME_RE

    if not re.match(USERNAME_RE, user):
        raise ValueError(f"Invalid username for unit file: {user!r}")


def generate_nspawn_unit(user: str) -> str:
    """Return the content of a .nspawn unit file for the given user.

    The container runs sleep infinity as its main process, providing
    namespaces for SSH shell access via nsenter.  Web serving uses
    host PHP-FPM pools directly (no FPM inside the container).
    """
    _validate_nspawn_inputs(user)
    ro_lines = "\n".join(f"BindReadOnly={p}" for p in HOST_RO_BINDS)

    return f"""\
# Managed by jabali-isolator — do not edit manually
[Exec]
PrivateUsers=no
Boot=no
ProcessTwo=yes
Parameters=/bin/sleep infinity

[Files]
{ro_lines}
BindReadOnly=/etc/php
Bind=/home/{user}
TemporaryFileSystem=/tmp:mode=1777

[Network]
VirtualEthernet=no
"""


def generate_service_dropin(memory: str = DEFAULT_MEMORY, cpu: str = DEFAULT_CPU) -> str:
    """Return the content of a systemd service drop-in for resource limits."""
    return f"""\
# Managed by jabali-isolator — do not edit manually
[Service]
MemoryMax={memory}
CPUQuota={cpu}
"""


def write_nspawn_unit(user: str) -> Path:
    """Write the .nspawn unit file.  Returns the file path."""
    path = nspawn_path(user)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(generate_nspawn_unit(user))
    os.chmod(path, 0o644)
    logger.info("Wrote nspawn unit: %s", path)
    return path


def write_service_dropin(user: str, memory: str = DEFAULT_MEMORY, cpu: str = DEFAULT_CPU) -> Path:
    """Write the service drop-in for resource limits.  Returns the drop-in path."""
    dropin = dropin_dir(user)
    dropin.mkdir(parents=True, exist_ok=True)
    conf = dropin / "limits.conf"
    conf.write_text(generate_service_dropin(memory, cpu))
    os.chmod(conf, 0o644)
    logger.info("Wrote service drop-in: %s", conf)
    return conf


def remove_unit_files(user: str) -> None:
    """Remove the .nspawn file and service drop-in directory for a user."""
    np = nspawn_path(user)
    if np.exists():
        np.unlink()
        logger.info("Removed %s", np)

    dd = dropin_dir(user)
    if dd.exists():
        from jabali_isolator.config import SERVICE_DROPIN_BASE

        resolved = dd.resolve()
        expected_parent = Path(SERVICE_DROPIN_BASE).resolve()
        if not resolved.is_relative_to(expected_parent):
            raise RuntimeError(f"Dropin path escapes SERVICE_DROPIN_BASE: {dd} -> {resolved}")
        shutil.rmtree(dd)
        logger.info("Removed %s", dd)


def unit_files_exist(user: str) -> bool:
    return nspawn_path(user).exists()
