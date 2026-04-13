"""Pydantic v2 data models for jabali-security."""

from __future__ import annotations

import uuid
from datetime import datetime, timezone
from typing import Literal

from pydantic import BaseModel, Field


def _hex_id(length: int) -> str:
    return uuid.uuid4().hex[:length]


def _utcnow() -> datetime:
    return datetime.now(timezone.utc)


class FileEvent(BaseModel):
    id: str = Field(default_factory=lambda: _hex_id(12))
    event_type: str
    path: str
    username: str | None = None
    timestamp: datetime = Field(default_factory=_utcnow)
    size: int = 0
    in_uploads_dir: bool = False


class Finding(BaseModel):
    scanner: str
    rule: str
    score: int
    description: str
    namespace: str = ""
    metadata: dict = Field(default_factory=dict)


class ThreatScore(BaseModel):
    total: int
    findings: list[Finding]
    action: Literal["ignore", "log", "quarantine", "suspend"]


class Incident(BaseModel):
    id: str = Field(default_factory=lambda: _hex_id(16))
    file_event: FileEvent
    findings: list[Finding]
    total_score: int
    severity: Literal["low", "medium", "high", "critical"]
    action_taken: str
    timestamp: datetime = Field(default_factory=_utcnow)
    username: str | None = None
    resolved: bool = False
    notes: str = ""

    @property
    def summary(self) -> str:
        """Top 3 findings descriptions joined."""
        return " | ".join(f.description for f in self.findings[:3])


class QuarantineRecord(BaseModel):
    id: str = Field(default_factory=lambda: _hex_id(16))
    original_path: str
    quarantine_path: str
    username: str | None = None
    timestamp: datetime = Field(default_factory=_utcnow)
    reason: str
    incident_id: str | None = None
    sha256: str
    restored: bool = False
    deleted: bool = False


class ProcessThreat(BaseModel):
    pid: int
    ppid: int
    cmdline: str
    parent_cmdline: str
    username: str | None = None
    score: int
    description: str
    timestamp: datetime = Field(default_factory=_utcnow)
