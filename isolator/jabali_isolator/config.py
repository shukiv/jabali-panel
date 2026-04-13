"""Default paths and resource limits."""

from __future__ import annotations

import re

MACHINES_DIR = "/var/lib/machines"
NSPAWN_DIR = "/etc/systemd/nspawn"
SERVICE_DROPIN_BASE = "/etc/systemd/system"
LOCK_DIR = "/run/jabali-isolator"

DEFAULT_MEMORY = "256M"
DEFAULT_CPU = "100%"

# Directories to bind-mount read-only from the host into every container.
HOST_RO_BINDS = ["/usr", "/lib", "/lib64", "/bin", "/sbin", "/etc/ssl/certs"]

# Username validation: only allow safe characters (no shell metacharacters).
USERNAME_RE = r"^[a-z_][a-z0-9_.-]{0,31}$"

# Resource limit patterns (systemd notation).
_MEMORY_RE = re.compile(r"^\d+[KMGT]?$", re.IGNORECASE)
_CPU_RE = re.compile(r"^\d+%$")


def validate_memory(value: str) -> str:
    """Validate a systemd memory limit string (e.g. '512M', '1G'). Returns the value or raises ValueError."""
    if not _MEMORY_RE.match(value):
        raise ValueError(f"Invalid memory limit: {value!r} (expected e.g. '512M', '1G', '2048K')")
    return value


def validate_cpu(value: str) -> str:
    """Validate a systemd CPU quota string (e.g. '100%', '200%'). Returns the value or raises ValueError."""
    if not _CPU_RE.match(value):
        raise ValueError(f"Invalid CPU quota: {value!r} (expected e.g. '100%', '200%')")
    return value
