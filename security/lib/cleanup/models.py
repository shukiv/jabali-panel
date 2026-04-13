"""Cleanup data models."""
from __future__ import annotations

import uuid
from datetime import datetime, timezone

from pydantic import BaseModel, Field


def _utcnow() -> datetime:
    return datetime.now(timezone.utc)


class CleanupResult(BaseModel):
    id: str = Field(default_factory=lambda: uuid.uuid4().hex[:16])
    timestamp: datetime = Field(default_factory=_utcnow)
    path: str
    strategy: str          # "cms_wordpress", "cms_joomla", "injection_removal", "quarantine_fallback"
    success: bool
    backup_path: str = ""
    changes_made: list[str] = Field(default_factory=list)
    error: str = ""
    username: str | None = None
