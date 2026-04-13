"""WAF data models."""
from __future__ import annotations

import uuid
from datetime import datetime, timezone

from pydantic import BaseModel, Field


def _utcnow() -> datetime:
    return datetime.now(timezone.utc)


def _hex_id() -> str:
    return uuid.uuid4().hex[:16]


class WafEvent(BaseModel):
    id: str = Field(default_factory=_hex_id)
    timestamp: datetime = Field(default_factory=_utcnow)
    client_ip: str
    server_ip: str = ""
    uri: str
    method: str = "GET"
    status_code: int = 0
    rule_id: int = 0
    rule_msg: str = ""
    severity: str = ""          # CRITICAL, ERROR, WARNING, NOTICE
    action: str = ""            # deny, drop, pass, redirect
    matched_data: str = ""
    request_headers: dict = Field(default_factory=dict)
    hostname: str = ""
    username: str | None = None  # resolved from URI path
