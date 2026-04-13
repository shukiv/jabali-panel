"""Privilege helpers for running commands as a non-root service user.

The jabali-security daemon runs as a dedicated 'jabali-security' system user
with Linux capabilities (CAP_DAC_READ_SEARCH, CAP_NET_ADMIN, etc.) and a
sudoers file that allows specific privileged commands (systemctl, ufw, cscli,
nginx).  This module provides helpers for building those command lists.

Backward compatible: if the daemon still runs as root (pre-upgrade), no
sudo prefix is added.
"""

from __future__ import annotations

import os
import shutil

_SUDO = shutil.which("sudo") or "/usr/bin/sudo"

# Cache at import time -- UID does not change during process lifetime.
_IS_ROOT = os.getuid() == 0


def sudo_prefix() -> list[str]:
    """Return ``['sudo']`` when not root, empty list when root."""
    if _IS_ROOT:
        return []
    return [_SUDO]


def sudo_cmd(*args: str) -> list[str]:
    """Build a command list with sudo prefix when needed.

    >>> sudo_cmd("/usr/bin/systemctl", "reload", "nginx")
    ['/usr/bin/sudo', '/usr/bin/systemctl', 'reload', 'nginx']  # non-root
    ['/usr/bin/systemctl', 'reload', 'nginx']                   # root
    """
    return [*sudo_prefix(), *args]


def is_service_user() -> bool:
    """True if running as the jabali-security service user or root."""
    if _IS_ROOT:
        return True
    try:
        import pwd
        return pwd.getpwuid(os.getuid()).pw_name == "jabali-security"
    except (KeyError, ImportError):
        return False
