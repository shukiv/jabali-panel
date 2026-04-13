"""UFW data models."""
from __future__ import annotations

from pydantic import BaseModel, Field


class UfwRule(BaseModel):
    number: int
    to: str
    action: str
    from_ip: str = Field(default="", alias="from")
    direction: str = ""
    v6: bool = False
    raw: str = ""


class UfwStatus(BaseModel):
    available: bool = False
    active: bool = False
    default_incoming: str = ""
    default_outgoing: str = ""
    default_routed: str = ""
    rules: list[UfwRule] = []
    rules_count: int = 0


class UfwAppProfile(BaseModel):
    name: str
    title: str = ""
    description: str = ""
    ports: str = ""
