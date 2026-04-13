"""Tests for lib.response.ResponseEngine — automated incident response."""

from __future__ import annotations

import pytest

from lib.config import JabaliConfig
from lib.incidents import IncidentStore
from lib.models import FileEvent, Finding, ThreatScore
from lib.quarantine import QuarantineManager
from lib.response import ResponseEngine


@pytest.fixture
async def store(tmp_path):
    db = IncidentStore(db_path=tmp_path / "test.db")
    await db.open()
    yield db
    await db.close()


@pytest.fixture
def quarantine(tmp_path):
    q = tmp_path / "quarantine"
    q.mkdir()
    return QuarantineManager(base_dir=str(q))


@pytest.fixture
def config():
    return JabaliConfig(auto_quarantine=True, auto_suspend=False)


@pytest.fixture
def engine(config, quarantine, store):
    return ResponseEngine(config, quarantine, store)


class TestResponseEngine:
    async def test_ignore_action_returns_none(self, engine):
        event = FileEvent(event_type="create", path="/test.php", size=10)
        score = ThreatScore(total=0, findings=[], action="ignore")
        result = await engine.handle(event, score)
        assert result is None

    async def test_log_action_creates_incident(self, engine, store):
        event = FileEvent(event_type="create", path="/test.php", username="user", size=10)
        findings = [Finding(scanner="test", rule="test", score=50, description="test")]
        score = ThreatScore(total=50, findings=findings, action="log")
        incident = await engine.handle(event, score)
        assert incident is not None
        assert incident.action_taken == "log"
        # Verify saved in DB
        got = await store.get(incident.id)
        assert got is not None

    async def test_quarantine_action_moves_file(self, engine, store, tmp_path):
        src = tmp_path / "evil.php"
        src.write_bytes(b"malware")
        event = FileEvent(event_type="create", path=str(src), username="user", size=7)
        findings = [Finding(scanner="test", rule="test", score=80, description="test")]
        score = ThreatScore(total=80, findings=findings, action="quarantine")
        incident = await engine.handle(event, score)
        assert incident is not None
        assert not src.exists()  # file moved to quarantine
