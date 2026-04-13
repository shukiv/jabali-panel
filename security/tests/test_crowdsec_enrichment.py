"""Tests for CrowdSec enrichment of threat_intel ReputationResult."""

from __future__ import annotations

from lib.crowdsec.client import CrowdSecClient
from lib.crowdsec.enrichment import enrich_reputation
from lib.threat_intel.models import ReputationResult


def _result(score: int = 0, feeds: list[str] | None = None, malicious: bool = False) -> ReputationResult:
    return ReputationResult(
        entity="1.2.3.4",
        entity_type="ip",
        is_malicious=malicious,
        score=score,
        feeds=feeds or [],
    )


def _client_with(ip: str, scenario: str, origin: str = "crowdsec") -> CrowdSecClient:
    c = CrowdSecClient(lapi_url="http://x", api_key="k")
    c._apply_stream({
        "new": [{"id": 1, "value": ip, "origin": origin, "scenario": scenario}],
        "deleted": None,
    })
    return c


class TestEnrichReputation:
    def test_no_client_returns_input_unchanged(self) -> None:
        r = _result(score=30, feeds=["spamhaus_drop"])
        out = enrich_reputation(r, None)
        assert out.score == 30
        assert out.feeds == ["spamhaus_drop"]
        assert out.is_malicious is False

    def test_ip_not_in_cache_returns_input_unchanged(self) -> None:
        c = _client_with("9.9.9.9", "crowdsecurity/ssh-bf")
        r = _result(score=30, feeds=["spamhaus_drop"])
        out = enrich_reputation(r, c)
        assert out.score == 30
        assert out.feeds == ["spamhaus_drop"]
        assert out.is_malicious is False

    def test_ip_in_cache_adds_crowdsec_feed(self) -> None:
        c = _client_with("1.2.3.4", "crowdsecurity/ssh-bf")
        r = _result()
        out = enrich_reputation(r, c)
        assert "crowdsec" in out.feeds

    def test_ip_in_cache_bumps_score_and_marks_malicious(self) -> None:
        # ssh-bf → 60; empty start → score becomes 60
        c = _client_with("1.2.3.4", "crowdsecurity/ssh-bf")
        r = _result(score=0)
        out = enrich_reputation(r, c)
        assert out.score == 60
        assert out.is_malicious is True

    def test_capi_origin_gets_bonus(self) -> None:
        # ssh-bf (60) + CAPI (20) → 80
        c = _client_with("1.2.3.4", "crowdsecurity/ssh-bf", origin="CAPI")
        r = _result(score=0)
        out = enrich_reputation(r, c)
        assert out.score == 80

    def test_score_clamped_at_100(self) -> None:
        # Existing feed score 30 + CrowdSec score 80 would overflow
        c = _client_with("1.2.3.4", "crowdsecurity/http-backdoors", origin="CAPI")
        r = _result(score=30)
        out = enrich_reputation(r, c)
        assert out.score <= 100

    def test_existing_feeds_preserved(self) -> None:
        c = _client_with("1.2.3.4", "crowdsecurity/ssh-bf")
        r = _result(feeds=["spamhaus_drop", "tor_exit_nodes"])
        out = enrich_reputation(r, c)
        assert "spamhaus_drop" in out.feeds
        assert "tor_exit_nodes" in out.feeds
        assert "crowdsec" in out.feeds

    def test_does_not_duplicate_crowdsec_feed(self) -> None:
        # Call enrichment twice — should not add "crowdsec" twice
        c = _client_with("1.2.3.4", "crowdsecurity/ssh-bf")
        r = _result(feeds=["crowdsec"])
        out = enrich_reputation(r, c)
        assert out.feeds.count("crowdsec") == 1

    def test_does_not_mutate_input(self) -> None:
        c = _client_with("1.2.3.4", "crowdsecurity/ssh-bf")
        r = _result(score=0, feeds=["spamhaus_drop"])
        enrich_reputation(r, c)
        # Input unchanged
        assert r.score == 0
        assert r.feeds == ["spamhaus_drop"]
        assert r.is_malicious is False
