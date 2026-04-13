"""Tests for lib.incidents.IncidentStore — SQLite incident persistence."""

from __future__ import annotations

import pytest

from lib.incidents import IncidentStore
from lib.models import FileEvent, Finding, Incident, QuarantineRecord


@pytest.fixture
async def store(tmp_path):
    db = IncidentStore(db_path=tmp_path / "test.db")
    await db.open()
    yield db
    await db.close()


@pytest.fixture
def sample_incident():
    event = FileEvent(
        event_type="create",
        path="/home/user/evil.php",
        username="testuser",
        size=100,
    )
    findings = [Finding(scanner="heuristic", rule="eval_base64", score=40, description="eval+base64")]
    return Incident(
        file_event=event,
        findings=findings,
        total_score=70,
        severity="high",
        action_taken="quarantine",
        username="testuser",
    )


class TestIncidentCRUD:
    async def test_save_and_get(self, store, sample_incident):
        await store.save(sample_incident)
        got = await store.get(sample_incident.id)
        assert got is not None
        assert got.id == sample_incident.id
        assert got.total_score == 70

    async def test_list_incidents(self, store, sample_incident):
        await store.save(sample_incident)
        results = await store.list_incidents(limit=10)
        assert len(results) >= 1

    async def test_list_filter_by_username(self, store, sample_incident):
        await store.save(sample_incident)
        results = await store.list_incidents(username="testuser")
        assert len(results) >= 1
        results2 = await store.list_incidents(username="nobody")
        assert len(results2) == 0

    async def test_list_filter_by_severity(self, store, sample_incident):
        await store.save(sample_incident)
        results = await store.list_incidents(severity="high")
        assert len(results) >= 1
        results2 = await store.list_incidents(severity="low")
        assert len(results2) == 0

    async def test_count_recent(self, store, sample_incident):
        await store.save(sample_incident)
        count = await store.count_recent(hours=24)
        assert count >= 1

    async def test_resolve(self, store, sample_incident):
        await store.save(sample_incident)
        ok = await store.resolve(sample_incident.id, notes="fixed")
        assert ok
        got = await store.get(sample_incident.id)
        assert got.resolved


class TestQuarantineRecords:
    async def test_save_and_list_quarantine(self, store):
        record = QuarantineRecord(
            original_path="/home/user/evil.php",
            quarantine_path="/var/quarantine/evil.php",
            username="user",
            reason="test",
            incident_id="abc123",
            sha256="a" * 64,
        )
        await store.save_quarantine(record)
        records = await store.list_quarantine()
        assert len(records) >= 1
