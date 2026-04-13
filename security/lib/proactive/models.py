"""Proactive defense data models."""
from __future__ import annotations

import uuid
from datetime import datetime, timezone

from pydantic import BaseModel, Field


def _utcnow() -> datetime:
    return datetime.now(timezone.utc)


class KillRecord(BaseModel):
    id: str = Field(default_factory=lambda: uuid.uuid4().hex[:16])
    timestamp: datetime = Field(default_factory=_utcnow)
    pid: int
    ppid: int
    cmdline: str
    username: str | None = None
    reason: str
    score: int
    success: bool = True
