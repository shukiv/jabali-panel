"""Enrich threat_intel reputation results with CrowdSec context.

Kept separate from FeedManager so feed downloads stay decoupled from
the LAPI bouncer; composition happens at the API layer (or wherever a
caller has both components available).
"""

from __future__ import annotations

from lib.crowdsec.client import CrowdSecClient
from lib.threat_intel.models import ReputationResult


def enrich_reputation(
    result: ReputationResult,
    client: CrowdSecClient | None,
) -> ReputationResult:
    """Return a new ReputationResult enriched with CrowdSec data.

    Pure function: does not mutate ``result``. Returns the input
    unchanged when no CrowdSec client is available or no active
    decisions exist for the entity.
    """
    if client is None:
        return result

    decisions = client.check_ip(result.entity)
    if not decisions:
        return result

    cs_score = client.check_ip_score(result.entity)
    feeds = list(result.feeds)
    if "crowdsec" not in feeds:
        feeds.append("crowdsec")

    # Scoring semantics match FeedManager: additive, clamped 0..100
    new_score = min(result.score + cs_score, 100)

    return ReputationResult(
        entity=result.entity,
        entity_type=result.entity_type,
        is_malicious=True,
        score=new_score,
        feeds=feeds,
        details=result.details,
    )
