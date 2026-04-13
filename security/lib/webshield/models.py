"""WebShield data models."""
from __future__ import annotations

from pydantic import BaseModel


class WebShieldStatus(BaseModel):
    installed: bool = False
    nginx_available: bool = False
    rate_limiting: bool = False
    bot_filtering: bool = False
    challenge_enabled: bool = False
    blocked_ips_count: int = 0
    config_dir: str = ""
    bot_blocked_24h: int = 0
    rate_limited_24h: int = 0
    challenged_24h: int = 0


class BotRule(BaseModel):
    name: str
    pattern: str
    action: str = "block"    # "block", "challenge", "allow"
    category: str = ""       # "malicious", "suspicious", "crawler", "verified"
    enabled: bool = True


class GeoRule(BaseModel):
    country_code: str        # ISO 3166-1 alpha-2 (e.g., "CN", "RU")
    country_name: str = ""
    action: str = "block"    # "block", "challenge", "log"
    enabled: bool = True
