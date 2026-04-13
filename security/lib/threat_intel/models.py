"""Threat intelligence data models."""
from __future__ import annotations

from datetime import datetime, timezone

from pydantic import BaseModel, Field


def _utcnow() -> datetime:
    return datetime.now(timezone.utc)


class ReputationResult(BaseModel):
    entity: str              # IP or hash
    entity_type: str         # "ip" or "hash"
    is_malicious: bool = False
    score: int = 0           # 0-100 reputation score
    feeds: list[str] = Field(default_factory=list)
    details: dict = Field(default_factory=dict)


class FeedStatus(BaseModel):
    name: str
    source_url: str = ""
    last_update: datetime | None = None
    entry_count: int = 0
    enabled: bool = True
    feed_type: str = "ip"    # "ip" or "hash"
