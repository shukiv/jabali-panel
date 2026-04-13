"""Version and path constants for jabali-security."""

from __future__ import annotations

import os
import pwd
from pathlib import Path

VERSION = "0.1.0"
APP_NAME = "jabali-security"


def _is_service_context() -> bool:
    """True when running as root or the dedicated jabali-security user."""
    uid = os.getuid()
    if uid == 0:
        return True
    try:
        return pwd.getpwuid(uid).pw_name == "jabali-security"
    except KeyError:
        return False


_service_context = _is_service_context()

if _service_context:
    CONFIG_DIR = Path("/etc/jabali-security")
    LOG_DIR = Path("/var/log/jabali-security")
    DATA_DIR = Path("/var/lib/jabali-security")
    QUARANTINE_DIR = Path("/var/security/quarantine")
    RULES_DIR = Path("/usr/local/jabali-security/rules")
else:
    _base = Path.home() / ".config" / "jabali-security"
    CONFIG_DIR = _base
    LOG_DIR = _base / "logs"
    DATA_DIR = _base / "data"
    QUARANTINE_DIR = _base / "quarantine"
    RULES_DIR = _base / "rules"

CONFIG_FILE = CONFIG_DIR / "jabali-security.conf"
