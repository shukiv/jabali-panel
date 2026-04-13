"""UFW input validators -- allowlist-based, no injection possible."""
from __future__ import annotations

import ipaddress
import re

VALID_PROTOCOLS = frozenset({"tcp", "udp", "any"})
VALID_ACTIONS = frozenset({"allow", "deny", "reject", "limit"})
VALID_DIRECTIONS = frozenset({"in", "out"})

_PORT_RE = re.compile(r"^(\d{1,5})(:\d{1,5})?$")
_SERVICE_RE = re.compile(r"^[a-zA-Z][a-zA-Z0-9_-]{0,30}$")
_APP_PROFILE_RE = re.compile(r"^[a-zA-Z0-9][a-zA-Z0-9 ._-]{0,63}$")
_COMMENT_RE = re.compile(r"^[a-zA-Z0-9 .,:;!?()#@/_-]+$")


def validate_ip(ip: str) -> str | None:
    """Validate and normalize an IP address or CIDR network. Returns normalized string or None."""
    s = ip.strip()
    if not s:
        return None
    try:
        addr = ipaddress.ip_address(s)
        return str(addr)
    except ValueError:
        pass
    try:
        net = ipaddress.ip_network(s, strict=False)
        return str(net)
    except ValueError:
        return None


def validate_port(port: str) -> str | None:
    """Validate a port number, range (start:end), or service name. Returns sanitized string or None."""
    s = port.strip()
    if not s:
        return None
    m = _PORT_RE.match(s)
    if m:
        start = int(m.group(1))
        if start < 1 or start > 65535:
            return None
        if m.group(2):
            end = int(m.group(2)[1:])
            if end < 1 or end > 65535 or start >= end:
                return None
            return "%d:%d" % (start, end)
        return str(start)
    if _SERVICE_RE.match(s):
        return s
    return None


def validate_protocol(proto: str) -> str | None:
    """Validate protocol name. Returns lowercase string or None."""
    s = proto.strip().lower()
    if s in VALID_PROTOCOLS:
        return s
    return None


def validate_action(action: str) -> str | None:
    """Validate UFW action. Returns lowercase string or None."""
    s = action.strip().lower()
    if s in VALID_ACTIONS:
        return s
    return None


def validate_direction(direction: str) -> str | None:
    """Validate UFW direction. Returns lowercase string or None."""
    s = direction.strip().lower()
    if s in VALID_DIRECTIONS:
        return s
    return None


def validate_app_profile(name: str) -> str | None:
    """Validate a UFW application profile name. Returns stripped string or None."""
    s = name.strip()
    if _APP_PROFILE_RE.match(s):
        return s
    return None


def validate_rule_number(num: int) -> bool:
    """Check that a rule number is a valid integer in range 1-9999."""
    return isinstance(num, int) and 1 <= num <= 9999


def validate_comment(text: str) -> str | None:
    """Validate a rule comment. Alphanumeric + safe punctuation only. Returns stripped string or None."""
    s = text.strip()
    if not s or len(s) > 256:
        return None
    if _COMMENT_RE.match(s):
        return s
    return None
