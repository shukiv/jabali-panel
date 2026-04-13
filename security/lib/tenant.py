"""Tenant / username resolution from filesystem paths."""

from __future__ import annotations

import re

_HOME_RE = re.compile(r"^/home/([^/]+)")


def resolve_user(path: str) -> str | None:
    """Extract username from path like /home/{user}/...

    Returns None if the path is not under /home/.
    """
    m = _HOME_RE.match(path)
    if m:
        return m.group(1)
    return None
